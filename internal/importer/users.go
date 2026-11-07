package importer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Greengrame/greengram-server/internal/account"
)

// importSettings copies the instance settings; the names and JSON values
// are the same on both sides (internal/settings reads what it knows).
func importSettings(ctx context.Context, r *run, w sink) (Counts, error) {
	c := Counts{}
	if !r.src.has("settings") {
		return c, nil
	}
	rows, err := r.src.db.QueryContext(ctx, "SELECT key, value FROM settings ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	b := newBatch(w, "instance_settings", "key", "value")
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if !json.Valid([]byte(v)) {
			c["bad_json"]++
			continue
		}
		if err := b.add(ctx, k, []byte(v)); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	c["settings"] = b.n
	return c, b.flush(ctx)
}

// importUsers keeps ids, password hashes (argon2id PHC, verified as is)
// and the encrypted phones: the instance secret file is the same, and the
// GCM context "user:<id>" still matches because the id does.
func importUsers(ctx context.Context, r *run, w sink) (Counts, error) {
	c := Counts{}
	s := r.src
	profile, bio, public := "", "''", "1"
	if s.has("user_profiles") {
		profile = " LEFT JOIN user_profiles p ON p.user_id = u.id"
		bio, public = s.col("p", "user_profiles", "bio", "''"), s.col("p", "user_profiles", "username_public", "1")
	}
	bot := "0"
	if s.has("bots") {
		bot = "EXISTS(SELECT 1 FROM bots b WHERE b.user_id = u.id)"
	}
	// account_state and is_suspended drive the admin panel; "deleted" is
	// what stops it from restoring an erased account
	q := fmt.Sprintf(`SELECT u.id, u.username, u.name, u.password, u.role, u.disabled, u.premium_until, u.created_at,
		%s, %s, %s, coalesce(%s, ''), coalesce(%s, 1), %s, %s, %s, %s FROM users u%s ORDER BY u.id`,
		s.col("u", "users", "phone", "NULL"), s.col("u", "users", "phone_lookup", "NULL"), s.col("u", "users", "is_system", "0"),
		bio, public, bot, s.col("u", "users", "account_state", "'active'"), s.col("u", "users", "is_suspended", "0"),
		s.col("u", "users", "premium_starts_at", "0"), profile)
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	b := newBatch(w, "users", "id", "username", "name", "password", "role", "disabled", "is_system", "is_bot",
		"phone", "phone_lookup", "premium_until", "bio", "username_public", "created_at", "account_state", "is_suspended", "premium_starts_at")
	var bad []string
	for rows.Next() {
		var (
			id, disabled, premium, created, system, pub, isBot, suspended, premiumFrom int64
			username, name, pw, role, about, state                                     string
			phone, lookup                                                              sql.NullString
		)
		if err := rows.Scan(&id, &username, &name, &pw, &role, &disabled, &premium, &created, &phone, &lookup, &system, &about, &pub, &isBot,
			&state, &suspended, &premiumFrom); err != nil {
			return nil, err
		}
		if role != "admin" {
			role = "member"
		} else {
			c["admins"]++
		}
		var ph, lk any
		if phone.Valid && phone.String != "" {
			ph = phone.String
			c["phones"]++
			if r.secret != nil {
				if why := checkPhone(r, id, phone.String, lookup.String); why != "" {
					c["phones_unreadable"]++
					if len(bad) < 5 {
						bad = append(bad, fmt.Sprintf("user %d: %s", id, why))
					}
				}
			}
		}
		if lookup.Valid && lookup.String != "" {
			lk = lookup.String
		}
		if system != 0 {
			c["system"]++
		}
		if isBot != 0 {
			c["bots"]++
		}
		if err := b.add(ctx, id, username, name, pw, role, disabled != 0, system != 0, isBot != 0, ph, lk, premium, about, pub != 0,
			time.Unix(created, 0).UTC(), state, suspended != 0, premiumFrom); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(bad) > 0 && !r.dry {
		return nil, fmt.Errorf("%d phones don't decrypt with this instance secret (wrong instance-secret.key?): %s", c["phones_unreadable"], strings.Join(bad, "; "))
	}
	c["users"] = b.n
	if err := b.flush(ctx); err != nil {
		return nil, err
	}
	if err := importBuiltinBots(ctx, r, w, c); err != nil {
		return nil, err
	}
	// ids came from the file; new sign-ups continue after them
	return c, w.exec(ctx, "SELECT setval(pg_get_serial_sequence('users', 'id'), (SELECT coalesce(max(id), 0) + 1 FROM users), false)")
}

// importBuiltinBots gives the web's BotFather, Stickers, Wallet and
// Fragment their bots row. Without it bots.EnsureBuiltins finds a plain
// account holding the name and leaves the bot out for good. The texts and
// mini app URLs are filled in at startup.
func importBuiltinBots(ctx context.Context, r *run, w sink, c Counts) error {
	if !r.src.has("bots") {
		return nil
	}
	rows, err := r.src.db.QueryContext(ctx, "SELECT user_id, kind FROM bots WHERE kind IN ('botfather', 'stickers', 'wallet', 'fragment') ORDER BY user_id")
	if err != nil {
		return err
	}
	defer rows.Close()
	b := newBatch(w, "bots", "user_id", "kind")
	for rows.Next() {
		var id int64
		var kind string
		if err := rows.Scan(&id, &kind); err != nil {
			return err
		}
		if !r.users[id] {
			continue
		}
		if err := b.add(ctx, id, kind); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	c["builtin_bots"] = b.n
	return b.flush(ctx)
}

func checkPhone(r *run, id int64, enc, lookup string) string {
	plain, err := r.secret.DecryptPhone(enc, fmt.Sprintf("user:%d", id))
	if err != nil {
		return "decrypt: " + err.Error()
	}
	if lookup != "" && r.secret.PhoneLookup(plain) != lookup {
		return "phone_lookup doesn't match"
	}
	return ""
}

var tokenRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// importSessions keeps live web sign-ins. Both sides store sha256(cookie)
// in hex, so the cookies people have keep working.
func importSessions(ctx context.Context, r *run, w sink) (Counts, error) {
	c := Counts{}
	s := r.src
	meta, agent, created, last := "", "''", "NULL", "NULL"
	if s.has("session_metadata") {
		meta = " LEFT JOIN session_metadata m ON m.token = s.token"
		agent, created, last = "coalesce(m.user_agent, '')", "m.created_at", "m.last_active"
	}
	q := fmt.Sprintf(`SELECT s.token, s.user_id, s.expires_at, %s, %s, %s, u.disabled, %s
		FROM sessions s JOIN users u ON u.id = s.user_id%s ORDER BY s.user_id, s.token`,
		agent, created, last, s.col("u", "users", "is_system", "0"), meta)
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	b := newBatch(w, "sessions", "token_hash", "user_id", "expires_at", "created_at", "last_active", "user_agent")
	now := r.now.Unix()
	for rows.Next() {
		var (
			token, ua               string
			user, expires, off, sys int64
			made, seen              sql.NullInt64
		)
		if err := rows.Scan(&token, &user, &expires, &ua, &made, &seen, &off, &sys); err != nil {
			return nil, err
		}
		switch {
		case expires <= now:
			c["expired"]++
			continue
		case off != 0 || sys != 0:
			c["disabled_account"]++
			continue
		case !tokenRe.MatchString(token):
			c["bad_token"]++
			continue
		}
		if !made.Valid {
			made.Int64 = now
		}
		if !seen.Valid {
			seen.Int64 = made.Int64
		}
		if len(ua) > 256 {
			ua = ua[:256]
		}
		if err := b.add(ctx, token, user, time.Unix(expires, 0).UTC(), time.Unix(made.Int64, 0).UTC(), time.Unix(seen.Int64, 0).UTC(), ua); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	c["sessions"] = b.n
	return c, b.flush(ctx)
}

// importPrivacy takes privacy rules and the address book out of
// account_settings. Rules keep their JSON; chat exceptions are rewritten from
// the web's client peer ids (-100<id> or -<id>) to channel ids.
func importPrivacy(ctx context.Context, r *run, w sink) (Counts, error) {
	c := Counts{}
	if !r.src.has("account_settings") {
		return c, nil
	}
	groups, err := groupIDs(ctx, r.src)
	if err != nil {
		return nil, err
	}
	rows, err := r.src.db.QueryContext(ctx, "SELECT user_id, value FROM account_settings ORDER BY user_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := newBatch(w, "privacy_rules", "user_id", "key", "rules")
	contacts := newBatch(w, "contacts", "owner_id", "user_id", "first_name", "last_name", "created_at")
	for rows.Next() {
		var user int64
		var raw string
		if err := rows.Scan(&user, &raw); err != nil {
			return nil, err
		}
		if !r.users[user] {
			continue
		}
		var v struct {
			Privacy      map[string]map[string]json.RawMessage `json:"privacy"`
			Contacts     []json.RawMessage                     `json:"contacts"`
			ContactNames map[string]string                     `json:"contactNames"`
		}
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			c["bad_json"]++
			continue
		}
		for key, stored := range v.Privacy {
			if !account.PrivacyKeys[key] {
				c["unknown_keys"]++
				continue
			}
			out, dropped, err := privacyJSON(key, stored, groups)
			if err != nil {
				c["bad_rules"]++
				continue
			}
			c["chat_exceptions_dropped"] += dropped
			if err := rules.add(ctx, user, key, out); err != nil {
				return nil, err
			}
		}
		// created_at orders the address book; keep the web's order
		base := r.now.Add(-time.Duration(len(v.Contacts)) * time.Microsecond)
		seen := map[int64]bool{}
		for i, rawID := range v.Contacts {
			var id idText
			id.UnmarshalJSON(rawID)
			who := int64(id)
			if who == user || !r.users[who] || seen[who] {
				c["contacts_skipped"]++
				continue
			}
			seen[who] = true
			first, last, _ := strings.Cut(strings.TrimSpace(v.ContactNames[strconv.FormatInt(who, 10)]), " ")
			if err := contacts.add(ctx, user, who, first, strings.TrimSpace(last), base.Add(time.Duration(i)*time.Microsecond)); err != nil {
				return nil, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	c["privacy_rules"], c["contacts"] = rules.n, contacts.n
	if err := rules.flush(ctx); err != nil {
		return nil, err
	}
	return c, contacts.flush(ctx)
}

// privacyJSON is the web's {...defaultPrivacy(key), ...stored}, with chat
// ids moved to channel ids. Fields Go doesn't know (isUnspecified) stay.
func privacyJSON(key string, stored map[string]json.RawMessage, groups map[int64]bool) ([]byte, int, error) {
	def, _ := json.Marshal(account.DefaultPrivacy(key))
	merged := map[string]json.RawMessage{}
	if err := json.Unmarshal(def, &merged); err != nil {
		return nil, 0, err
	}
	for k, v := range stored {
		merged[k] = v
	}
	dropped := 0
	for _, field := range []string{"allowChatIds", "blockChatIds"} {
		var list []json.RawMessage
		if err := json.Unmarshal(merged[field], &list); err != nil {
			return nil, 0, err
		}
		out := []string{}
		for _, item := range list {
			var id idText
			id.UnmarshalJSON(item)
			chat := oldChatID(int64(id))
			if !groups[chat] {
				dropped++
				continue
			}
			out = append(out, strconv.FormatInt(chat, 10))
		}
		merged[field], _ = json.Marshal(out)
	}
	// check it still reads as account.Privacy
	b, err := json.Marshal(merged)
	if err != nil {
		return nil, 0, err
	}
	var p account.Privacy
	return b, dropped, json.Unmarshal(b, &p)
}

// oldChatID undoes the web's client peer ids for chats.
func oldChatID(peer int64) int64 {
	if peer < -1_000_000_000_000 {
		return -peer - 1_000_000_000_000
	}
	return -peer
}

func groupIDs(ctx context.Context, src *source) (map[int64]bool, error) {
	rows, err := src.db.QueryContext(ctx, "SELECT id FROM chats WHERE kind = 'group'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
