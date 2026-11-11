package account

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/password"
)

// Changing the sign-in number, Telegram's Change Number: ask for a code,
// then send it back with the number. Nothing here can text a phone, so the
// code goes to the account's own apps and only proves the session; the
// number just has to be free. Errors carry Telegram's names in Message.
const (
	phoneChangeTTL   = 10 * time.Minute
	phoneChangeTries = 5
	PhoneCodeLength  = 5
)

// PhoneChange is a started number change: Hash goes back to the app, Code
// to the account's service chat.
type PhoneChange struct {
	Phone, Hash, Code string
}

// phoneFree is whether user may take phone as their number: a sign-up
// number nobody signs in with, holds after a swap or has on Fragment. A
// number user kept after a swap of their own is fine.
func (s *Service) phoneFree(ctx context.Context, tx pgx.Tx, user int64, phone string) error {
	if !registerPhoneRe.MatchString(phone) {
		return fail(400, "PHONE_NUMBER_INVALID")
	}
	if err := collectibleCode(ctx, tx, phone); err != nil {
		var e *Error
		if errors.As(err, &e) {
			return fail(400, "PHONE_NUMBER_INVALID")
		}
		return err
	}
	var taken bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE phone_lookup=$2)
		OR EXISTS(SELECT 1 FROM phone_aliases WHERE lookup=$2 AND user_id<>$3)
		OR EXISTS(SELECT 1 FROM fragment_assignments WHERE previous_lookup=$2 AND user_id<>$3)
		OR EXISTS(SELECT 1 FROM fragment_items WHERE kind='number' AND title=$1)`,
		phone, s.secret.PhoneLookup(phone), user).Scan(&taken)
	if err == nil && taken {
		err = fail(400, "PHONE_NUMBER_OCCUPIED")
	}
	return err
}

// StartPhoneChange checks the number and makes the code for it. An
// instance that hands numbers out at sign-up takes none typed in here.
func (s *Service) StartPhoneChange(ctx context.Context, u User, phone string) (PhoneChange, error) {
	phone = "+" + digits(phone)
	if u.IsBot || u.IsSystem || s.settings.Get(ctx).SignupGeneratedNumbers {
		return PhoneChange{}, fail(400, "PHONE_NUMBER_INVALID")
	}
	if err := s.limits.hit(fmt.Sprintf("phone-change:%d", u.ID), 5, 10*time.Minute, s.now()); err != nil {
		return PhoneChange{}, err
	}
	n, err := rand.Int(rand.Reader, big.NewInt(100_000))
	if err != nil {
		return PhoneChange{}, err
	}
	pc := PhoneChange{Phone: phone, Hash: newToken(16), Code: fmt.Sprintf("%0*d", PhoneCodeLength, n)}
	enc, err := s.secret.EncryptPhone(phone, fmt.Sprintf("phone-change:%d", u.ID))
	if err != nil {
		return PhoneChange{}, err
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if err := s.phoneFree(ctx, tx, u.ID, phone); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM phone_changes WHERE expires_at <= now()"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO phone_changes(user_id, phone, hash, code_hash, expires_at) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (user_id) DO UPDATE SET phone=excluded.phone, hash=excluded.hash, code_hash=excluded.code_hash,
				attempts=0, expires_at=excluded.expires_at`,
			u.ID, enc, pc.Hash, Hash(pc.Hash+":"+pc.Code), s.now().Add(phoneChangeTTL))
		return err
	})
	if err != nil {
		return PhoneChange{}, err
	}
	return pc, nil
}

// ChangePhone finishes StartPhoneChange. The old number is given up for
// anyone to take; an old collectible stays owned and keeps signing in, it
// just stops being the number shown. Sessions stay as they are.
func (s *Service) ChangePhone(ctx context.Context, user int64, phone, hash, code string) (User, error) {
	phone = "+" + digits(phone)
	var u User
	var wrong error
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		u, err = s.scanUser(tx.QueryRow(ctx, "SELECT "+userCols+" FROM users WHERE id=$1 FOR UPDATE", user))
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(404, "Account not found.")
		}
		if err != nil {
			return err
		}
		var enc, wantHash, codeHash string
		var attempts int
		err = tx.QueryRow(ctx, "SELECT phone, hash, code_hash, attempts FROM phone_changes WHERE user_id=$1 AND expires_at > $2 FOR UPDATE", user, s.now()).
			Scan(&enc, &wantHash, &codeHash, &attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(400, "PHONE_CODE_EXPIRED")
		}
		if err != nil {
			return err
		}
		asked, err := s.secret.DecryptPhone(enc, fmt.Sprintf("phone-change:%d", user))
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare([]byte(hash), []byte(wantHash)) != 1 || asked != phone {
			return fail(400, "PHONE_CODE_EXPIRED")
		}
		if subtle.ConstantTimeCompare([]byte(Hash(hash+":"+code)), []byte(codeHash)) != 1 {
			// committed, so the tries add up; the last one ends the change
			q := "UPDATE phone_changes SET attempts=attempts+1 WHERE user_id=$1"
			if attempts+1 >= phoneChangeTries {
				q = "DELETE FROM phone_changes WHERE user_id=$1"
			}
			_, err := tx.Exec(ctx, q, user)
			wrong = fail(400, "PHONE_CODE_INVALID")
			return err
		}
		if err := s.phoneFree(ctx, tx, user, phone); err != nil {
			return err
		}
		if err := s.setPhone(ctx, tx, user, phone); err != nil {
			return err
		}
		u.Phone = phone
		return nil
	})
	if err == nil {
		err = wrong
	}
	return u, err
}

// setPhone makes phone the account's number and clears what the old one
// left behind. The caller has checked phoneFree.
func (s *Service) setPhone(ctx context.Context, tx pgx.Tx, user int64, phone string) error {
	lookup := s.secret.PhoneLookup(phone)
	if _, err := tx.Exec(ctx, "DELETE FROM phone_changes WHERE user_id=$1", user); err != nil {
		return err
	}
	// a kept number that becomes the number again is no alias
	if _, err := tx.Exec(ctx, "DELETE FROM phone_aliases WHERE user_id=$1 AND lookup=$2", user, lookup); err != nil {
		return err
	}
	// the collectible shown as the number, and the number it replaced
	if _, err := tx.Exec(ctx, "DELETE FROM fragment_assignments WHERE user_id=$1", user); err != nil {
		return err
	}
	own, err := s.secret.EncryptPhone(phone, fmt.Sprintf("user:%d", user))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE users SET phone=$2, phone_lookup=$3 WHERE id=$1", user, own, lookup)
	return err
}

// phoneChangeText is what the website says for the names the apps get.
var phoneChangeText = map[string]string{
	"PHONE_NUMBER_INVALID":  "Enter a phone number with 4–16 digits including the country code.",
	"PHONE_NUMBER_OCCUPIED": "This number is already in use.",
}

// ChangePhoneWithPassword is the website's Change Number: the session's
// password stands in for the code, over HTTPS like every password here. An
// account without one (imported, never set) passes as it is.
func (s *Service) ChangePhoneWithPassword(ctx context.Context, sessionToken, phone, pw string, c Client) (User, error) {
	u, _, err := s.SessionUser(ctx, sessionToken)
	if err != nil {
		return User{}, err
	}
	phone = "+" + digits(phone)
	if u.IsBot || u.IsSystem {
		return User{}, fail(403, "This account can't change its number.")
	}
	if s.settings.Get(ctx).SignupGeneratedNumbers {
		return User{}, fail(403, "Numbers are handed out by this instance and can't be changed.")
	}
	if u.State != "active" {
		return User{}, fail(403, "This account can't change its number.")
	}
	if err := s.limits.hit(fmt.Sprintf("phone-change:%d:%s", u.ID, limitIP(c.IP)), 5, 10*time.Minute, s.now()); err != nil {
		return User{}, err
	}
	if u.HasPassword() && (len(pw) > 256 || !password.Verify(pw, u.password)) {
		return User{}, fail(400, "That password is not correct.")
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		var hash string
		if err := tx.QueryRow(ctx, "SELECT password FROM users WHERE id=$1 FOR UPDATE", u.ID).Scan(&hash); err != nil {
			return err
		}
		if hash != u.password {
			return fail(409, "Your password changed in another session. Sign in again.")
		}
		if err := s.phoneFree(ctx, tx, u.ID, phone); err != nil {
			var e *Error
			if errors.As(err, &e) && phoneChangeText[e.Message] != "" {
				return fail(e.Status, phoneChangeText[e.Message])
			}
			return err
		}
		return s.setPhone(ctx, tx, u.ID, phone)
	})
	if err != nil {
		return User{}, err
	}
	u.Phone = phone
	return u, nil
}
