package account

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// AnonymousCode is the country code of the numbers the server makes up:
// +888, the code Telegram's anonymous numbers use. The ITU gave it to the
// UN for disaster relief and no carrier routes calls or SMS to it, so a
// made-up number can never be somebody's real phone.
const AnonymousCode = "888"

// GenerateNumber picks a free +888 number, eight random digits after the
// code, that nothing else signs in with or holds: no account, no number
// kept after a swap, no Fragment listing, no collectible country code.
func (s *Service) GenerateNumber(ctx context.Context, tx pgx.Tx) (string, error) {
	for range 32 {
		n, err := rand.Int(rand.Reader, big.NewInt(100_000_000))
		if err != nil {
			return "", err
		}
		phone := fmt.Sprintf("+%s%08d", AnonymousCode, n)
		var free bool
		err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM users WHERE phone_lookup=$2)
			AND NOT EXISTS(SELECT 1 FROM phone_aliases WHERE lookup=$2)
			AND NOT EXISTS(SELECT 1 FROM fragment_assignments WHERE previous_lookup=$2)
			AND NOT EXISTS(SELECT 1 FROM fragment_items WHERE kind='number'
				AND (title=$1 OR length(country_code) > 3 AND starts_with(substr($1, 2), country_code)))`,
			phone, s.secret.PhoneLookup(phone)).Scan(&free)
		if err != nil {
			return "", err
		}
		if free {
			return phone, nil
		}
	}
	return "", errors.New("account: no free generated number")
}

// The parts of a profile ResetIdentity can wipe, in the order the admin
// panel lists them.
const (
	ResetPhone    = "phone"
	ResetName     = "name"
	ResetBio      = "bio"
	ResetUsername = "username"
	ResetPhotos   = "photos"
	ResetBirthday = "birthday"
	ResetChannel  = "channel"
)

var ResetFields = []string{ResetPhone, ResetName, ResetBio, ResetUsername, ResetPhotos, ResetBirthday, ResetChannel}

// Reset is what ResetIdentity did. OldPhone is masked (MaskPhone): the
// number itself is what the reset is for, it doesn't go into the audit log.
type Reset struct {
	Fields   []string `json:"fields"`
	OldPhone string   `json:"oldPhone,omitempty"`
	NewPhone string   `json:"newPhone,omitempty"`
	Username string   `json:"username,omitempty"`
	Photos   int64    `json:"photos,omitempty"`
	// numbers dropped, for the limiter; never stored
	dropped []string
}

// ResetIdentity wipes the chosen parts of user's profile inside tx and
// leaves the account working: same password, same sessions, same chats.
//
// The phone becomes a made-up +888 number the account signs in with from
// now on, and every number it could sign in with before goes: the numbers
// kept after a Fragment swap and the one remembered under an assigned
// collectible. Collectibles it owns stay its own and still sign it in.
// Sign-ins in progress that carry one of the old numbers are dropped too.
// The name becomes "User <id>". A username reset gives the name up for
// anyone to take and leaves a hidden "user<id>" behind, which still works
// for username sign-in; collectible usernames stay owned but go off the
// profile.
func (s *Service) ResetIdentity(ctx context.Context, tx pgx.Tx, user int64, fields []string) (Reset, error) {
	var res Reset
	for _, f := range ResetFields {
		if slices.Contains(fields, f) {
			res.Fields = append(res.Fields, f)
		}
	}
	if len(res.Fields) == 0 || len(res.Fields) != len(fields) {
		return res, fail(400, "Choose what to reset.")
	}
	u, err := s.scanUser(tx.QueryRow(ctx, "SELECT "+userCols+" FROM users WHERE id=$1 FOR UPDATE", user))
	if errors.Is(err, pgx.ErrNoRows) {
		return res, fail(404, "Account not found.")
	}
	if err != nil {
		return res, err
	}
	for _, f := range res.Fields {
		var err error
		switch f {
		case ResetPhone:
			err = s.resetPhone(ctx, tx, u, &res)
		case ResetName:
			_, err = tx.Exec(ctx, "UPDATE users SET name=$2 WHERE id=$1", user, "User "+strconv.FormatInt(user, 10))
		case ResetBio:
			_, err = tx.Exec(ctx, "UPDATE users SET bio='' WHERE id=$1", user)
		case ResetUsername:
			err = s.resetUsername(ctx, tx, u, &res)
		case ResetPhotos:
			err = tx.QueryRow(ctx, `WITH gone AS (DELETE FROM user_photos WHERE user_id=$1 RETURNING 1),
				cur AS (UPDATE users SET photo_id=NULL WHERE id=$1) SELECT count(*) FROM gone`, user).Scan(&res.Photos)
		case ResetBirthday:
			_, err = tx.Exec(ctx, "UPDATE account_prefs SET birthday_day=NULL, birthday_month=NULL, birthday_year=NULL WHERE user_id=$1", user)
		case ResetChannel:
			_, err = tx.Exec(ctx, "UPDATE account_prefs SET personal_channel_id=NULL WHERE user_id=$1", user)
		}
		if err != nil {
			return res, fmt.Errorf("reset %s: %w", f, err)
		}
	}
	return res, nil
}

func (s *Service) resetPhone(ctx context.Context, tx pgx.Tx, u User, res *Reset) error {
	old := []string{}
	if u.Phone != "" {
		old = append(old, u.Phone)
	}
	rows, err := tx.Query(ctx, "SELECT lookup, phone FROM phone_aliases WHERE user_id=$1", u.ID)
	if err != nil {
		return err
	}
	type alias struct{ lookup, phone string }
	aliases, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (alias, error) {
		var a alias
		return a, r.Scan(&a.lookup, &a.phone)
	})
	if err != nil {
		return err
	}
	for _, a := range aliases {
		p, err := s.secret.DecryptPhone(a.phone, fmt.Sprintf("phone-alias:%d:%s", u.ID, a.lookup))
		if err != nil {
			return err
		}
		old = append(old, p)
	}
	var prev *string
	err = tx.QueryRow(ctx, "SELECT previous_phone FROM fragment_assignments WHERE user_id=$1", u.ID).Scan(&prev)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if prev != nil && *prev != "" {
		p, err := s.secret.DecryptPhone(*prev, fmt.Sprintf("fragment-phone:%d", u.ID))
		if err != nil {
			return err
		}
		old = append(old, p)
	}

	next, err := s.GenerateNumber(ctx, tx)
	if err != nil {
		return err
	}
	enc, err := s.secret.EncryptPhone(next, fmt.Sprintf("user:%d", u.ID))
	if err != nil {
		return err
	}
	for _, q := range []string{
		"DELETE FROM phone_aliases WHERE user_id=$1",
		// the assignment is the collectible shown as primary and the number
		// it replaced; the collectible stays owned and keeps signing in
		"DELETE FROM fragment_assignments WHERE user_id=$1",
	} {
		if _, err := tx.Exec(ctx, q, u.ID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, "UPDATE users SET phone=$2, phone_lookup=$3 WHERE id=$1", u.ID, enc, s.secret.PhoneLookup(next)); err != nil {
		return err
	}

	// sign-ins in progress hold their number encrypted under their own id;
	// the ones with an old number go, finished ones go anyway
	if _, err := tx.Exec(ctx, "DELETE FROM auth_flows WHERE expires_at <= now()"); err != nil {
		return err
	}
	rows, err = tx.Query(ctx, "SELECT id, phone FROM auth_flows WHERE phone IS NOT NULL")
	if err != nil {
		return err
	}
	type flow struct{ id, phone string }
	flows, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (flow, error) {
		var f flow
		return f, r.Scan(&f.id, &f.phone)
	})
	if err != nil {
		return err
	}
	var stale []string
	for _, f := range flows {
		if p, err := s.secret.DecryptPhone(f.phone, "auth-flow:"+f.id); err == nil && slices.Contains(old, p) {
			stale = append(stale, f.id)
		}
	}
	if len(stale) > 0 {
		if _, err := tx.Exec(ctx, "DELETE FROM auth_flows WHERE id = ANY($1)", stale); err != nil {
			return err
		}
	}
	if u.Phone != "" {
		res.OldPhone = MaskPhone(u.Phone)
	}
	res.NewPhone, res.dropped = next, old
	return nil
}

func (s *Service) resetUsername(ctx context.Context, tx pgx.Tx, u User, res *Reset) error {
	name := "user" + strconv.FormatInt(u.ID, 10)
	for i := 0; ; i++ {
		if i == 16 {
			return errors.New("account: no free placeholder username")
		}
		if i > 0 {
			n, _ := rand.Int(rand.Reader, big.NewInt(10000))
			name = fmt.Sprintf("user%d_%04d", u.ID, n)
		}
		var taken bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(username)=$1 AND id<>$2)
			OR EXISTS(SELECT 1 FROM channels WHERE lower(username)=$1 AND NOT deleted)
			OR EXISTS(SELECT 1 FROM fragment_items WHERE kind='username' AND lower(title)='@'||$1)`, name, u.ID).Scan(&taken)
		if err != nil {
			return err
		}
		if !taken && !reservedUsernames[name] {
			break
		}
	}
	if _, err := tx.Exec(ctx, "UPDATE users SET username=$2, username_public=false WHERE id=$1", u.ID, name); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE fragment_items SET active=false WHERE owner_id=$1 AND kind='username'", u.ID); err != nil {
		return err
	}
	res.Username = name
	return nil
}

// ForgetNumbers drops the sign-in limits counted against numbers a reset
// took away: the limiter keys them in clear, in memory.
func (s *Service) ForgetNumbers(r Reset) {
	s.limits.mu.Lock()
	defer s.limits.mu.Unlock()
	for _, p := range r.dropped {
		delete(s.limits.m, "password-phone:"+p)
	}
}

// MaskPhone keeps the first two and last two digits of a number long
// enough to hide something in between: +380501234512 is +38*********12.
func MaskPhone(phone string) string {
	d := digits(phone)
	if len(d) < 8 {
		return "+" + strings.Repeat("*", len(d))
	}
	return "+" + d[:2] + strings.Repeat("*", len(d)-4) + d[len(d)-2:]
}
