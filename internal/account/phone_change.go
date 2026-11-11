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
		if _, err := tx.Exec(ctx, "UPDATE users SET phone=$2, phone_lookup=$3 WHERE id=$1", user, own, lookup); err != nil {
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
