package wallet

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/password"
)

// The wallet passcode is a second factor for spending: argon2id like the
// sign-in password, five wrong tries lock it for five minutes, and an
// unlock lasts five minutes in the one session that entered it.

var passcodeRe = regexp.MustCompile(`^\d{4}(?:\d{2})?$`)

const (
	unlockFor = 300
	maxTries  = 5
)

type security struct {
	hash, kind  string
	failures    int
	lockedUntil int
	version     int
	configured  bool
}

func (s *Service) security(ctx context.Context, q querier, user int64) (security, error) {
	var sec security
	err := q.QueryRow(ctx, "SELECT passcode_hash, passcode_type, failures, locked_until, version FROM wallet_security WHERE user_id=$1", user).
		Scan(&sec.hash, &sec.kind, &sec.failures, &sec.lockedUntil, &sec.version)
	if errors.Is(err, pgx.ErrNoRows) {
		return sec, nil
	}
	sec.configured = err == nil
	return sec, err
}

// PasscodeStatus is walletSecurityStatus(); session is the hash of the
// session cookie, "" when there is none.
func (s *Service) PasscodeStatus(ctx context.Context, user int64, session string) (map[string]any, error) {
	sec, err := s.security(ctx, s.db, user)
	if err != nil {
		return nil, err
	}
	var expires, version int
	if session != "" {
		err := s.db.QueryRow(ctx, "SELECT expires_at, version FROM wallet_unlocks WHERE session_hash=$1 AND user_id=$2", session, user).Scan(&expires, &version)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	var kind any
	if sec.configured {
		kind = sec.kind
	}
	unlocked := sec.configured && expires != 0 && version == sec.version && expires > s.unix()
	return map[string]any{"configured": sec.configured, "passcodeType": kind, "required": true, "recoveryEmailEnabled": false,
		"unlocked": unlocked, "expiresAt": expires, "retryAfter": max(0, sec.lockedUntil-s.unix())}, nil
}

// RequireUnlocked guards everything that moves money: transfers, swaps, Earn and DeFi moves.
func (s *Service) RequireUnlocked(ctx context.Context, user int64, session string) error {
	st, err := s.PasscodeStatus(ctx, user, session)
	if err != nil {
		return err
	}
	if !st["configured"].(bool) {
		return fail(423, "Set a Wallet passcode before using Wallet.")
	}
	if !st["unlocked"].(bool) {
		return fail(423, "Unlock Wallet with your passcode.")
	}
	return nil
}

// The wallet's passcode screens read these codes off the response.
var errTooMany = &account.Error{Status: 429, Message: "Too many incorrect passcodes. Try again later.",
	Fields: map[string]any{"code": "passcode_exceeded_max_attempts", "num_remaining_attempts": 0}}

var errPasscodeMissing = &account.Error{Status: 423, Message: "Unlock Wallet with your passcode.",
	Fields: map[string]any{"code": "passcode_missing"}}

func wrongPasscode(failures int) error {
	return &account.Error{Status: 403, Message: "Incorrect Wallet passcode.",
		Fields: map[string]any{"code": "passcode_wrong", "num_remaining_attempts": max(0, maxTries-failures)}}
}

// verify counts the try before checking it, so parallel guesses can't get
// past the lock.
func (s *Service) verify(ctx context.Context, user int64, code any) (security, error) {
	sec, err := s.security(ctx, s.db, user)
	if err != nil {
		return sec, err
	}
	if !sec.configured {
		return sec, fail(409, "Set a Wallet passcode first.")
	}
	if sec.lockedUntil > s.unix() {
		return sec, errTooMany
	}
	var failures int
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var locked int
		if err := tx.QueryRow(ctx, "SELECT locked_until FROM wallet_security WHERE user_id=$1 FOR UPDATE", user).Scan(&locked); err != nil {
			return err
		}
		if locked > s.unix() {
			return errTooMany
		}
		return tx.QueryRow(ctx, `UPDATE wallet_security SET failures = failures + 1,
			locked_until = CASE WHEN failures + 1 >= $3 THEN $2 ELSE locked_until END WHERE user_id=$1 RETURNING failures`, user, s.unix()+300, maxTries).Scan(&failures)
	})
	if err != nil {
		return sec, err
	}
	pc, _ := code.(string)
	if !passcodeRe.MatchString(pc) || !password.Verify(pc, sec.hash) {
		return sec, wrongPasscode(failures)
	}
	var same bool
	if err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM wallet_security WHERE user_id=$1 AND version=$2 AND passcode_hash=$3)", user, sec.version, sec.hash).Scan(&same); err != nil {
		return sec, err
	}
	if !same {
		return sec, fail(409, "Wallet passcode changed. Try again.")
	}
	_, err = s.db.Exec(ctx, "UPDATE wallet_security SET failures=0, locked_until=0 WHERE user_id=$1", user)
	return sec, err
}

var (
	passcodePath = regexp.MustCompile(`^/local/passcode/(?:status|create|unlock|lock|change)$`)
	appPasscode  = regexp.MustCompile(`^/public-api/(?:passcode/(?:verify|update|remove)?|challenges)$`)
)

// Passcode serves /local/passcode/* and the wallet's own passcode API;
// handled is false for other paths.
func (s *Service) Passcode(ctx context.Context, u account.User, session, method, path string, data map[string]any) (any, bool, error) {
	if method == "POST" && appPasscode.MatchString(path) {
		out, err := s.appPasscode(ctx, u, session, path, data)
		return out, true, err
	}
	if !passcodePath.MatchString(path) {
		return nil, false, nil
	}
	out, err := s.passcode(ctx, u, session, method, path, data)
	return out, true, err
}

// appPasscode is the same passcode under the wallet's own API, for its own
// screens. An update needs the current passcode even when the app sends an
// email code instead: nothing here can check one, so it fails as a wrong try.
func (s *Service) appPasscode(ctx context.Context, u account.User, session, path string, data map[string]any) (any, error) {
	local := func(action string, body map[string]any) (any, error) {
		return s.passcode(ctx, u, session, "POST", "/local/passcode/"+action, body)
	}
	switch path {
	case "/public-api/passcode/":
		return local("create", map[string]any{"passcode": data["newPasscode"], "confirmation": data["newPasscode"]})
	case "/public-api/passcode/verify":
		return local("unlock", map[string]any{"passcode": data["passcode"]})
	case "/public-api/passcode/update":
		return local("change", map[string]any{"passcode": data["newPasscode"], "confirmation": data["newPasscode"], "currentPasscode": data["passcode"]})
	case "/public-api/passcode/remove":
		if err := s.removePasscode(ctx, u.ID, data["passcode"]); err != nil {
			return nil, err
		}
		return s.PasscodeStatus(ctx, u.ID, session)
	}
	// A challenge with a passcode is an unlock. The token is only there for
	// the app to pass along: spending is guarded by the unlock, see
	// RequireUnlocked, so nothing checks it.
	if code, _ := data["passcode"].(string); code != "" {
		if _, err := local("unlock", map[string]any{"passcode": code}); err != nil {
			return nil, err
		}
	} else if st, err := s.PasscodeStatus(ctx, u.ID, session); err != nil {
		return nil, err
	} else if !st["unlocked"].(bool) {
		return nil, errPasscodeMissing
	}
	return map[string]any{"operationToken": "unlocked"}, nil
}

// removePasscode turns the passcode off. Wallet stays guarded: RequireUnlocked
// refuses to spend until a new one is set.
func (s *Service) removePasscode(ctx context.Context, user int64, code any) error {
	sec, err := s.verify(ctx, user, code)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM wallet_security WHERE user_id=$1 AND version=$2", user, sec.version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fail(409, "Wallet passcode changed. Try again.")
		}
		_, err = tx.Exec(ctx, "DELETE FROM wallet_unlocks WHERE user_id=$1", user)
		return err
	})
}

func (s *Service) passcode(ctx context.Context, u account.User, session, method, path string, data map[string]any) (any, error) {
	if method == "GET" && strings.HasSuffix(path, "/status") {
		return s.PasscodeStatus(ctx, u.ID, session)
	}
	if method != "POST" {
		return nil, fail(405, "Use POST for Wallet security changes.")
	}
	var sec security
	var err error
	switch {
	case strings.HasSuffix(path, "/lock"):
		if _, err := s.db.Exec(ctx, "DELETE FROM wallet_unlocks WHERE session_hash=$1 AND user_id=$2", session, u.ID); err != nil {
			return nil, err
		}
		return s.PasscodeStatus(ctx, u.ID, session)
	case strings.HasSuffix(path, "/create"), strings.HasSuffix(path, "/change"):
		pc, _ := data["passcode"].(string)
		if !passcodeRe.MatchString(pc) {
			return nil, fail(400, "Use a four or six digit Wallet passcode.")
		}
		if conf, _ := data["confirmation"].(string); conf != pc {
			return nil, fail(400, "The passcodes do not match.")
		}
		kind := "4-digit"
		if len(pc) == 6 {
			kind = "6-digit"
		}
		changing := strings.HasSuffix(path, "/change")
		if changing {
			if sec, err = s.verify(ctx, u.ID, data["currentPasscode"]); err != nil {
				return nil, err
			}
		} else if cur, err := s.security(ctx, s.db, u.ID); err != nil {
			return nil, err
		} else if cur.configured {
			return nil, fail(409, "A Wallet passcode is already set.")
		}
		hash, err := password.Hash(pc)
		if err != nil {
			return nil, err
		}
		err = s.tx(ctx, func(tx pgx.Tx) error {
			if changing {
				tag, err := tx.Exec(ctx, `UPDATE wallet_security SET passcode_hash=$2, passcode_type=$3, version=version+1, failures=0, locked_until=0
					WHERE user_id=$1 AND version=$4`, u.ID, hash, kind, sec.version)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					return fail(409, "Wallet passcode changed. Try again.")
				}
			} else {
				tag, err := tx.Exec(ctx, "INSERT INTO wallet_security(user_id, passcode_hash, passcode_type) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING", u.ID, hash, kind)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					return fail(409, "A Wallet passcode is already set.")
				}
			}
			_, err := tx.Exec(ctx, "DELETE FROM wallet_unlocks WHERE user_id=$1", u.ID)
			return err
		})
		if err != nil {
			return nil, err
		}
		if changing {
			sec.version++
		} else {
			sec.version = 1
		}
	case strings.HasSuffix(path, "/unlock"):
		if sec, err = s.verify(ctx, u.ID, data["passcode"]); err != nil {
			return nil, err
		}
	default:
		return nil, fail(404, "Wallet security action not found.")
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO wallet_unlocks(session_hash, user_id, expires_at, version)
		SELECT $1, user_id, $3, version FROM wallet_security WHERE user_id=$2 AND version=$4
		ON CONFLICT (session_hash) DO UPDATE SET expires_at = excluded.expires_at, version = excluded.version`,
		session, u.ID, s.unix()+unlockFor, sec.version)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, fail(409, "Wallet passcode changed. Unlock it again.")
	}
	return s.PasscodeStatus(ctx, u.ID, session)
}
