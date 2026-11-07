package account

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Sessions slide: each use (at most once a minute) pushes expiry to
// SessionDays from now, so only idle sessions run out.
const touchEvery = time.Minute

// SessionUser resolves a session cookie. ok=false means "sign in again".
func (s *Service) SessionUser(ctx context.Context, token string) (User, string, error) {
	if len(token) != 64 {
		return User{}, "", fail(401, "Sign in to continue.")
	}
	return s.sessionByHash(ctx, Hash(token))
}

func (s *Service) sessionByHash(ctx context.Context, hash string) (User, string, error) {
	var lastActive time.Time
	u, err := s.scanUserWith(s.db.QueryRow(ctx, "SELECT "+prefixed("u.")+", s.last_active FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash=$1 AND s.expires_at > now()", hash), &lastActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", fail(401, "Sign in to continue.")
	}
	if err != nil {
		return User{}, "", err
	}
	if u.Disabled || u.IsSystem {
		s.db.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", hash)
		return User{}, "", fail(401, "Sign in to continue.")
	}
	if s.now().Sub(lastActive) > touchEvery {
		// the account's "terminate old sessions" choice can only shorten it
		days := s.settings.Get(ctx).SessionDays
		s.db.Exec(ctx, `UPDATE sessions SET last_active=now(), expires_at=now() + make_interval(days => least($2,
			coalesce((SELECT authorization_ttl_days FROM account_prefs WHERE user_id=$3), $2))) WHERE token_hash=$1`, hash, days, u.ID)
	}
	return u, hash, nil
}

func prefixed(p string) string {
	return p + "id, " + p + "username, " + p + "name, " + p + "password, " + p + "role, " + p + "disabled, " + p + "is_system, " +
		p + "is_bot, coalesce(" + p + "phone, ''), " + p + "premium_until, " + p + "bio, " + p + "username_public, " + p + "created_at, coalesce(" + p + "photo_id, 0), " + p + "account_state, " +
		"coalesce(" + p + "emoji_status_id, 0), coalesce(" + p + "emoji_status_until, 0), " + aliasesCol(p) + ", " + collectibleCol(p)
}

func (s *Service) scanUserWith(row pgx.Row, extra ...any) (User, error) {
	return s.scanUser(scanExtra{row, extra})
}

// scanExtra appends destinations after the user columns.
type scanExtra struct {
	row   pgx.Row
	extra []any
}

func (r scanExtra) Scan(dest ...any) error { return r.row.Scan(append(dest, r.extra...)...) }

func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.db.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", Hash(token))
	return err
}

// AppInfo comes from MTProto initConnection.
type AppInfo struct {
	APIID         int
	DeviceModel   string
	SystemVersion string
	AppVersion    string
	IP            string
}

// StampIP records where a session was last seen from.
func (s *Service) StampIP(ctx context.Context, hash, ip string) error {
	_, err := s.db.Exec(ctx, "UPDATE sessions SET ip=$2 WHERE token_hash=$1 AND ip<>$2", hash, ip)
	return err
}

// BindKey signs an app in: the auth key it talks over gets the session the
// app opened over HTTPS (auth.importAuthorization).
func (s *Service) BindKey(ctx context.Context, keyID int64, token string, userID int64, app AppInfo) (User, error) {
	u, hash, err := s.SessionUser(ctx, token)
	if err != nil {
		return User{}, err
	}
	if u.ID != userID {
		return User{}, fail(400, "USER_ID_INVALID")
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE sessions SET api_id=$2, device_model=$3, system_version=$4, app_version=$5,
			ip=CASE WHEN $6 = '' THEN ip ELSE $6 END WHERE token_hash=$1`,
			hash, app.APIID, truncate(app.DeviceModel, 128), truncate(app.SystemVersion, 128), truncate(app.AppVersion, 64), app.IP); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "UPDATE auth_keys SET session_hash=$2 WHERE id=$1", keyID, hash)
		if err == nil && tag.RowsAffected() == 0 {
			err = errors.New("auth key vanished while binding")
		}
		return err
	})
	return u, err
}

// KeyUser is the account an auth key is signed in as. ok=false: not signed
// in (AUTH_KEY_UNREGISTERED).
func (s *Service) KeyUser(ctx context.Context, keyID int64) (User, string, bool, error) {
	var hash *string
	err := s.db.QueryRow(ctx, "SELECT session_hash FROM auth_keys WHERE id=$1", keyID).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && hash == nil {
		return User{}, "", false, nil
	}
	if err != nil {
		return User{}, "", false, err
	}
	u, h, err := s.sessionByHash(ctx, *hash)
	var e *Error
	if errors.As(err, &e) {
		return User{}, "", false, nil
	}
	return u, h, err == nil, err
}

// UnbindKey signs the app on this key out by ending its session; the
// foreign key clears auth_keys.session_hash.
func (s *Service) UnbindKey(ctx context.Context, keyID int64) error {
	_, err := s.db.Exec(ctx, "DELETE FROM sessions WHERE token_hash = (SELECT session_hash FROM auth_keys WHERE id=$1)", keyID)
	return err
}

type SessionInfo struct {
	Hash          string
	Created       time.Time
	LastActive    time.Time
	UserAgent     string
	IP            string
	APIID         int
	DeviceModel   string
	SystemVersion string
	AppVersion    string
	CallsOff      bool
	SecretOff     bool
}

func (s *Service) Sessions(ctx context.Context, userID int64) ([]SessionInfo, error) {
	rows, err := s.db.Query(ctx, `SELECT token_hash, created_at, last_active, user_agent, ip, api_id, device_model, system_version, app_version,
		calls_disabled, secret_chats_disabled FROM sessions WHERE user_id=$1 AND expires_at > now() ORDER BY last_active DESC`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SessionInfo, error) {
		var i SessionInfo
		err := r.Scan(&i.Hash, &i.Created, &i.LastActive, &i.UserAgent, &i.IP, &i.APIID, &i.DeviceModel, &i.SystemVersion, &i.AppVersion, &i.CallsOff, &i.SecretOff)
		return i, err
	})
}

// EndSession ends one of the user's sessions; false if it wasn't theirs.
func (s *Service) EndSession(ctx context.Context, userID int64, hash string) (bool, error) {
	tag, err := s.db.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1 AND token_hash=$2", userID, hash)
	return tag.RowsAffected() == 1, err
}

// EndOtherSessions keeps only keep.
func (s *Service) EndOtherSessions(ctx context.Context, userID int64, keep string) error {
	_, err := s.db.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1 AND token_hash<>$2", userID, keep)
	return err
}
