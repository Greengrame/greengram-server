package account

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/password"
	"github.com/Greengrame/greengram-server/internal/settings"
)

// Sign-in follows greengram-web's auth.js step for step (the Android app and
// the web client both drive it): start a flow, give a phone number, then a
// password; or a username and password; or create an account; or approve a
// QR code from a signed-in device.

const flowTTL = 10 * time.Minute

type Flow struct {
	Token   string // cookie value
	State   string // qr, phone, phone_register, password, registration, approved
	QR      string // for mode qr: what the QR code encodes
	Expires time.Time
}

// Client is who is signing in, for limits and the sessions list.
type Client struct {
	IP        string
	UserAgent string
}

// Session is a fresh sign-in; Token goes into the session cookie.
type Session struct {
	Token string
	User  User
}

func (s *Service) StartFlow(ctx context.Context, mode, previous string, c Client) (Flow, error) {
	if err := s.limits.hit("flow:"+limitIP(c.IP), 60, time.Minute, s.now()); err != nil {
		return Flow{}, err
	}
	f := Flow{Token: newToken(32), State: "qr", Expires: s.now().Add(time.Duration(s.settings.Get(ctx).QRExpirySeconds) * time.Second)}
	var qrHash *string
	if mode == "phone" {
		f.State, f.Expires = "phone", s.now().Add(flowTTL)
	} else {
		f.QR = newToken(32)
		h := Hash(f.QR)
		qrHash = &h
	}
	_, err := s.db.Exec(ctx, `
		WITH gone AS (DELETE FROM auth_flows WHERE expires_at <= now() OR id = $5)
		INSERT INTO auth_flows(id, state, qr_hash, expires_at) VALUES ($1, $2, $3, $4)`,
		Hash(f.Token), f.State, qrHash, f.Expires, Hash(previous))
	return f, err
}

type flowRow struct {
	id     string
	state  string
	phone  string
	userID *int64
}

func (s *Service) flow(ctx context.Context, tx pgx.Tx, token string) (flowRow, error) {
	var f flowRow
	var phone *string
	err := tx.QueryRow(ctx, "SELECT id, state, phone, user_id FROM auth_flows WHERE id=$1 AND expires_at > now() FOR UPDATE", Hash(token)).
		Scan(&f.id, &f.state, &phone, &f.userID)
	if errors.Is(err, pgx.ErrNoRows) || token == "" {
		return f, fail(410, "The sign-in request expired. Start again.")
	}
	if err != nil {
		return f, err
	}
	if phone != nil {
		if f.phone, err = s.secret.DecryptPhone(*phone, "auth-flow:"+f.id); err != nil {
			return f, err
		}
	}
	return f, nil
}

func (s *Service) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.db, fn)
}

func (s *Service) SetIntent(ctx context.Context, token, intent string) error {
	if intent != "login" && intent != "register" {
		return fail(400, "Choose sign in or create account.")
	}
	if intent == "register" && !s.settings.Get(ctx).RegistrationEnabled {
		return fail(403, "New account registration is closed.")
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		f, err := s.flow(ctx, tx, token)
		if err != nil {
			return err
		}
		if f.state != "phone" && f.state != "phone_register" {
			return fail(409, "Start phone sign-in first.")
		}
		state := "phone"
		if intent == "register" {
			state = "phone_register"
		}
		_, err = tx.Exec(ctx, "UPDATE auth_flows SET state=$2 WHERE id=$1", f.id, state)
		return err
	})
}

var (
	loginPhoneRe    = regexp.MustCompile(`^\+[1-9][0-9]{0,127}$`)
	registerPhoneRe = regexp.MustCompile(`^\+[1-9][0-9]{3,15}$`)
)

// GivePhone stores the number on the flow and says what comes next:
// "password" to sign in, "registration" to create the account.
func (s *Service) GivePhone(ctx context.Context, token, phone string, c Client) (string, error) {
	phone = "+" + digits(phone)
	// before the transaction: it may need a connection of its own
	open := s.settings.Get(ctx).RegistrationEnabled
	var next string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		f, err := s.flow(ctx, tx, token)
		if err != nil {
			return err
		}
		if f.state != "phone" && f.state != "phone_register" {
			return fail(409, "Start phone sign-in first.")
		}
		next = "password"
		if f.state == "phone_register" {
			next = "registration"
			if !registerPhoneRe.MatchString(phone) {
				return fail(400, "Enter a phone number with 4–16 digits including the country code.")
			}
			if err := collectibleCode(ctx, tx, phone); err != nil {
				return err
			}
			if !open {
				return fail(403, "New account registration is closed.")
			}
		} else if !loginPhoneRe.MatchString(phone) {
			return fail(400, "Enter a valid local sign-in number.")
		}
		if err := s.limits.hit("phone-ip:"+limitIP(c.IP), 30, 10*time.Minute, s.now()); err != nil {
			return err
		}
		enc, err := s.secret.EncryptPhone(phone, "auth-flow:"+f.id)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE auth_flows SET state=$2, phone=$3 WHERE id=$1", f.id, next, enc)
		return err
	})
	return next, err
}

// SignInPhone finishes a phone flow with the password.
func (s *Service) SignInPhone(ctx context.Context, token, pw string, c Client) (Session, error) {
	set := s.settings.Get(ctx)
	var phone string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		f, err := s.flow(ctx, tx, token)
		if err == nil && f.state != "password" {
			err = fail(409, "Enter your phone number first.")
		}
		phone = f.phone
		return err
	})
	if err != nil {
		return Session{}, err
	}
	if err := s.limits.hit("password-ip:"+limitIP(c.IP), set.LoginAttemptsPerMinute, time.Minute, s.now()); err != nil {
		return Session{}, err
	}
	if err := s.limits.hit("password-phone:"+phone, 10, 10*time.Minute, s.now()); err != nil {
		return Session{}, err
	}
	u, ok, err := s.phoneAccount(ctx, phone)
	if err != nil {
		return Session{}, err
	}
	if !s.passwordOK(u, ok, pw) {
		return Session{}, fail(401, "Incorrect phone number or password.")
	}
	return s.complete(ctx, token, u, c)
}

// SignInUsername is for accounts made before phone sign-in.
func (s *Service) SignInUsername(ctx context.Context, token, username, pw string, c Client) (Session, error) {
	username = normUsername(username)
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		f, err := s.flow(ctx, tx, token)
		if err == nil && f.state != "phone" && f.state != "phone_register" {
			err = fail(409, "Start sign-in again.")
		}
		return err
	})
	if err != nil {
		return Session{}, err
	}
	u, err := s.checkUsername(ctx, username, pw, c)
	if err != nil {
		return Session{}, err
	}
	return s.complete(ctx, token, u, c)
}

// Login is the old one-step /api/login, still used by the Android app for
// username accounts.
func (s *Service) Login(ctx context.Context, username, pw string, c Client) (Session, error) {
	username = normUsername(username)
	if !usernameRe.MatchString(username) {
		return Session{}, fail(400, "Use 3–32 letters, numbers, or underscores for your username.")
	}
	if err := password.Validate(pw); err != nil {
		return Session{}, fail(400, err.Error())
	}
	u, err := s.checkUsername(ctx, username, pw, c)
	if err != nil {
		return Session{}, err
	}
	return s.complete(ctx, "", u, c)
}

func (s *Service) checkUsername(ctx context.Context, username, pw string, c Client) (User, error) {
	if err := s.limits.hit("password-ip:"+limitIP(c.IP), s.settings.Get(ctx).LoginAttemptsPerMinute, time.Minute, s.now()); err != nil {
		return User{}, err
	}
	if err := s.limits.hit("password-username:"+username, 10, 10*time.Minute, s.now()); err != nil {
		return User{}, err
	}
	if len(pw) > 256 || !usernameRe.MatchString(username) {
		password.Check(pw, "")
		return User{}, fail(401, "Incorrect username or password.")
	}
	u, ok, err := s.userByUsername(ctx, username)
	if err != nil {
		return User{}, err
	}
	if !s.passwordOK(u, ok, pw) {
		return User{}, fail(401, "Incorrect username or password.")
	}
	return u, nil
}

// passwordOK always runs a hash, so a missing account isn't faster to spot.
func (s *Service) passwordOK(u User, found bool, pw string) bool {
	hash := ""
	if found {
		hash = u.password
	}
	if len(pw) > 256 {
		password.Check(pw, "")
		return false
	}
	ok := password.Check(pw, hash)
	return ok && found && !u.Disabled && !u.IsSystem && !u.IsBot
}

type Registration struct {
	FirstName, LastName, Username, Password string
}

// Register creates the account at the end of a phone flow. The first
// account on an instance becomes its admin.
func (s *Service) Register(ctx context.Context, token string, r Registration, c Client) (Session, error) {
	set := s.settings.Get(ctx)
	username := normUsername(r.Username)
	first, last := strings.TrimSpace(r.FirstName), strings.TrimSpace(r.LastName)
	name := strings.TrimSpace(first + " " + last)
	switch {
	case !usernameRe.MatchString(username):
		return Session{}, fail(400, "Use 3–32 letters, numbers, or underscores for your username.")
	case reservedUsernames[username]:
		return Session{}, fail(409, "That username is reserved for a built-in bot.")
	case password.Validate(r.Password) != nil:
		return Session{}, fail(400, password.ErrLength.Error())
	case first == "" || utf8.RuneCountInString(name) > 80:
		return Session{}, fail(400, "Enter a name of up to 80 characters.")
	case !set.RegistrationEnabled:
		return Session{}, fail(403, "New account registration is closed.")
	}
	if err := s.limits.hit("register-ip:"+limitIP(c.IP), 10, 10*time.Minute, s.now()); err != nil {
		return Session{}, err
	}
	hash, err := password.Hash(r.Password)
	if err != nil {
		return Session{}, err
	}
	// one transaction: two requests on the same flow must not make two
	// accounts
	var sess Session
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		f, err := s.flow(ctx, tx, token)
		if err != nil {
			return err
		}
		if f.state != "registration" {
			return fail(409, "Choose create account and enter your phone number first.")
		}
		u, err := s.insertUser(ctx, tx, username, name, hash, f.phone, set)
		if err != nil {
			return err
		}
		sess, err = s.completeTx(ctx, tx, token, u, c, set.SessionDays)
		return err
	})
	return sess, err
}

// RegisterUsername is the old /api/register: no phone number.
func (s *Service) RegisterUsername(ctx context.Context, username, name, pw string, c Client) (Session, error) {
	set := s.settings.Get(ctx)
	username, name = normUsername(username), strings.TrimSpace(name)
	switch {
	case !usernameRe.MatchString(username):
		return Session{}, fail(400, "Use 3–32 letters, numbers, or underscores for your username.")
	case password.Validate(pw) != nil:
		return Session{}, fail(400, password.ErrLength.Error())
	case reservedUsernames[username]:
		return Session{}, fail(409, "That username is reserved for a built-in bot.")
	case !set.RegistrationEnabled:
		return Session{}, fail(403, "New account registration is closed.")
	case name == "" || utf8.RuneCountInString(name) > 80:
		return Session{}, fail(400, "Enter a display name of up to 80 characters.")
	}
	if err := s.limits.hit("register-ip:"+limitIP(c.IP), 10, 10*time.Minute, s.now()); err != nil {
		return Session{}, err
	}
	hash, err := password.Hash(pw)
	if err != nil {
		return Session{}, err
	}
	var u User
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		u, err = s.insertUser(ctx, tx, username, name, hash, "", set)
		return err
	})
	if err != nil {
		return Session{}, err
	}
	return s.complete(ctx, "", u, c)
}

func (s *Service) insertUser(ctx context.Context, tx pgx.Tx, username, name, hash, phone string, set settings.Settings) (User, error) {
	// serialize sign-ups so exactly one first account becomes admin;
	// built-in bots and system accounts exist from the start and don't count
	if _, err := tx.Exec(ctx, "LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		return User{}, err
	}
	var taken bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE lower(username)=$1)", username).Scan(&taken); err != nil {
		return User{}, err
	}
	if taken {
		return User{}, fail(409, "That username is already taken.")
	}
	if err := reservedOnFragment(ctx, tx, username); err != nil {
		return User{}, err
	}
	var lookup *string
	if phone != "" {
		l := s.secret.PhoneLookup(phone)
		lookup = &l
		if err := collectibleCode(ctx, tx, phone); err != nil {
			return User{}, err
		}
		if err := retainedPhone(ctx, tx, l); err != nil {
			return User{}, err
		}
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE phone_lookup=$1)", l).Scan(&taken); err != nil {
			return User{}, err
		}
		if taken {
			return User{}, fail(409, "This phone number is already registered. Start sign-in again.")
		}
		if err := reservedNumber(ctx, tx, phone); err != nil {
			return User{}, err
		}
	}
	var premium int64
	if set.DefaultPremiumDays > 0 {
		premium = s.now().Add(time.Duration(set.DefaultPremiumDays) * 24 * time.Hour).Unix()
	}
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO users(username, name, password, phone_lookup, premium_until, role)
		VALUES ($1, $2, $3, $4, $5, CASE WHEN EXISTS(SELECT 1 FROM users WHERE NOT is_bot AND NOT is_system) THEN 'member' ELSE 'admin' END)
		RETURNING id`, username, name, hash, lookup, premium).Scan(&id)
	if err != nil {
		return User{}, err
	}
	if phone != "" {
		enc, err := s.secret.EncryptPhone(phone, fmt.Sprintf("user:%d", id))
		if err != nil {
			return User{}, err
		}
		if _, err := tx.Exec(ctx, "UPDATE users SET phone=$2 WHERE id=$1", id, enc); err != nil {
			return User{}, err
		}
	}
	return s.scanUser(tx.QueryRow(ctx, "SELECT "+userCols+" FROM users WHERE id=$1", id))
}

// complete ends the flow (if any) and opens a session.
func (s *Service) complete(ctx context.Context, flowToken string, u User, c Client) (Session, error) {
	days := s.settings.Get(ctx).SessionDays
	var sess Session
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		sess, err = s.completeTx(ctx, tx, flowToken, u, c, days)
		return err
	})
	return sess, err
}

// completeTx is complete inside tx. Deleting the flow row is the lock: of
// two requests finishing one flow, the second waits for the first and then
// finds nothing to delete, so it gets no session. days comes from the caller:
// reading settings here would ask the pool for a second connection.
func (s *Service) completeTx(ctx context.Context, tx pgx.Tx, flowToken string, u User, c Client, days int) (Session, error) {
	if u.Disabled {
		return Session{}, fail(403, "This account is unavailable.")
	}
	if flowToken != "" {
		tag, err := tx.Exec(ctx, "DELETE FROM auth_flows WHERE id=$1 AND expires_at > now()", Hash(flowToken))
		if err != nil {
			return Session{}, err
		}
		if tag.RowsAffected() == 0 {
			return Session{}, fail(410, "The sign-in request expired. Start again.")
		}
	}
	token := newToken(32)
	_, err := tx.Exec(ctx, `INSERT INTO sessions(token_hash, user_id, expires_at, user_agent, ip)
		VALUES ($1, $2, now() + make_interval(days => $3), $4, $5)`,
		Hash(token), u.ID, days, truncate(c.UserAgent, 256), c.IP)
	if err != nil {
		return Session{}, err
	}
	if _, err := tx.Exec(ctx, "UPDATE users SET last_seen_at=now() WHERE id=$1", u.ID); err != nil {
		return Session{}, err
	}
	return Session{Token: token, User: u}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ApproveQR signs the device showing the QR code in as user.
func (s *Service) ApproveQR(ctx context.Context, userID int64, qr string) error {
	qr = strings.TrimPrefix(qr, "tg://login?token=")
	tag, err := s.db.Exec(ctx, `UPDATE auth_flows SET state='approved', user_id=$2, qr_hash=NULL
		WHERE qr_hash=$1 AND state='qr' AND expires_at > now()`, Hash(qr), userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fail(410, "This QR code expired or has already been approved.")
	}
	return nil
}

// PollQR returns the flow state, and a session once the code was approved.
func (s *Service) PollQR(ctx context.Context, token string, c Client) (string, *Session, error) {
	var state string
	var userID *int64
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		f, err := s.flow(ctx, tx, token)
		state, userID = f.state, f.userID
		return err
	})
	if err != nil || state != "approved" || userID == nil {
		return state, nil, err
	}
	u, ok, err := s.User(ctx, *userID)
	if err != nil {
		return "", nil, err
	}
	if !ok {
		return "", nil, fail(403, "This account is unavailable.")
	}
	sess, err := s.complete(ctx, token, u, c)
	if err != nil {
		return "", nil, err
	}
	return "ready", &sess, nil
}

// SetPassword changes the password and ends every other session.
func (s *Service) SetPassword(ctx context.Context, sessionToken, current, next string, c Client) error {
	u, _, err := s.SessionUser(ctx, sessionToken)
	if err != nil {
		return err
	}
	if err := s.limits.hit(fmt.Sprintf("set-password:%d:%s", u.ID, limitIP(c.IP)), 5, 10*time.Minute, s.now()); err != nil {
		return err
	}
	if password.Validate(next) != nil {
		return fail(400, password.ErrLength.Error())
	}
	if strings.HasPrefix(u.password, "$") && !password.Verify(current, u.password) {
		return fail(400, "Enter your current password to change it.")
	}
	hash, err := password.Hash(next)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "UPDATE users SET password=$3 WHERE id=$1 AND password=$2", u.ID, u.password, hash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fail(409, "Your password changed in another session. Sign in again.")
		}
		_, err = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1 AND token_hash<>$2", u.ID, Hash(sessionToken))
		return err
	})
}

// ConfirmDeletion is the password check before account.deleteAccount: the
// password never travels over MTProto, so the apps send it here first and
// the session that passed may then delete the account for DeletionWindow.
// An account without a password (imported, never set) passes as it is.
func (s *Service) ConfirmDeletion(ctx context.Context, sessionToken, pw string, c Client) error {
	u, hash, err := s.SessionUser(ctx, sessionToken)
	if err != nil {
		return err
	}
	if err := s.limits.hit(fmt.Sprintf("delete-account:%d:%s", u.ID, limitIP(c.IP)), 5, 10*time.Minute, s.now()); err != nil {
		return err
	}
	if u.HasPassword() && (len(pw) > 256 || !password.Verify(pw, u.password)) {
		return fail(400, "That password is not correct.")
	}
	_, err = s.db.Exec(ctx, "UPDATE sessions SET delete_confirmed_at=now() WHERE token_hash=$1", hash)
	return err
}

// DeletionWindow is how long a passed ConfirmDeletion lasts.
const DeletionWindow = 5 * time.Minute

// DeletionConfirmed is whether the session passed ConfirmDeletion within
// DeletionWindow, or the account has no password to check.
func (s *Service) DeletionConfirmed(ctx context.Context, u User, sessionHash string) (bool, error) {
	if !u.HasPassword() {
		return true, nil
	}
	var ok bool
	err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM sessions WHERE token_hash=$1 AND user_id=$2 AND delete_confirmed_at > now() - make_interval(secs => $3))",
		sessionHash, u.ID, DeletionWindow.Seconds()).Scan(&ok)
	return ok, err
}

// HasPassword is whether signing in takes a password; imported accounts
// may never have set one.
func (u User) HasPassword() bool { return strings.HasPrefix(u.password, "$") }
