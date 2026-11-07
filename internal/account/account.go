// Package account owns users, sign-in and sessions. The HTTP sign-in API
// (internal/web) and the MTProto handlers (internal/api) both go through it.
package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Greengrame/greengram-server/internal/secret"
	"github.com/Greengrame/greengram-server/internal/settings"
)

// Error is a refusal meant for the person signing in; Status is the HTTP code.
type Error struct {
	Status  int
	Message string
	// Fields go into the JSON error next to the message.
	Fields map[string]any
}

func (e *Error) Error() string { return e.Message }

func fail(status int, msg string) error { return &Error{Status: status, Message: msg} }

type Service struct {
	db       *pgxpool.Pool
	secret   *secret.Key
	settings *settings.Source
	limits   limiter
	now      func() time.Time
}

func New(db *pgxpool.Pool, key *secret.Key, set *settings.Source) *Service {
	return &Service{db: db, secret: key, settings: set, limits: limiter{m: map[string]*bucket{}}, now: time.Now}
}

type User struct {
	ID             int64
	Username       string
	Name           string
	Role           string
	Disabled       bool
	IsSystem       bool
	IsBot          bool
	Phone          string // decrypted, "+15550100"; "" if none
	PremiumUntil   int64
	Bio            string
	UsernamePublic bool
	CreatedAt      time.Time
	PhotoID        int64  // profile photo; 0 = none
	State          string // account_state: active, frozen, locked, banned, deleted
	EmojiStatus    int64  // custom emoji document; 0 = none
	EmojiUntil     int64  // unix time the status ends; 0 = until changed
	Aliases        []Alias
	// a collectible gift worn as the emoji status instead
	Collectible *CollectibleStatus

	password string
}

func (u User) Premium(now time.Time) bool { return u.PremiumUntil > now.Unix() }

// Status is the custom emoji shown next to the name: Premium only, like
// Telegram, and gone once it runs out.
func (u User) Status(now time.Time) (int64, int64, bool) {
	if u.EmojiStatus == 0 || !u.Premium(now) || u.EmojiUntil != 0 && u.EmojiUntil <= now.Unix() {
		return 0, 0, false
	}
	return u.EmojiStatus, u.EmojiUntil, true
}

// SetEmojiStatus sets or (doc 0) clears the custom emoji status. One that
// gets set moves to the front of the user's recents.
func (s *Service) SetEmojiStatus(ctx context.Context, user, doc, until int64) error {
	_, err := s.db.Exec(ctx, `WITH recent AS (
			INSERT INTO recent_emoji_statuses(user_id, document_id) SELECT $1, $2 WHERE $2::bigint <> 0
			ON CONFLICT (user_id, document_id) DO UPDATE SET used_at = now())
		UPDATE users SET emoji_status_id=nullif($2::bigint, 0), emoji_status_until=nullif($3::bigint, 0), emoji_status_collectible=NULL WHERE id=$1`, user, doc, until)
	return err
}

// CollectibleStatus is a collectible gift worn as the emoji status: what
// emojiStatusCollectible says about it.
type CollectibleStatus struct {
	ID           int64  `json:"id"`
	Document     int64  `json:"doc"`
	Pattern      int64  `json:"pattern"`
	Title        string `json:"title"`
	Slug         string `json:"slug"`
	Center       int    `json:"center"`
	Edge         int    `json:"edge"`
	PatternColor int    `json:"patternColor"`
	Text         int    `json:"text"`
}

// SetCollectibleStatus wears one of user's collectibles as the status; ok is
// false when they don't hold it or it's listed for sale.
func (s *Service) SetCollectibleStatus(ctx context.Context, user, id, until int64) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE users SET emoji_status_collectible=$2, emoji_status_id=NULL, emoji_status_until=nullif($3::bigint, 0)
		WHERE id=$1 AND EXISTS (SELECT 1 FROM gift_receipts WHERE unique_id=$2 AND owner_id=$1 AND NOT converted)
		AND NOT EXISTS (SELECT 1 FROM unique_gifts WHERE id=$2 AND resale_stars IS NOT NULL)`, user, id, until)
	return err == nil && tag.RowsAffected() > 0, err
}

// collectibleCol is the collectible worn as the status, as JSON, NULL for
// none or one they no longer hold. unique_gifts belongs to internal/stars,
// plain SQL like aliasesCol.
func collectibleCol(p string) string {
	return "(SELECT json_build_object('id', cu.id, 'doc', cm.emoji_document_id, 'pattern', cpt.emoji_document_id, 'title', cs.title || ' #' || cu.num, " +
		"'slug', cu.slug, 'center', cb.center_color, 'edge', cb.edge_color, 'patternColor', cb.pattern_color, 'text', cb.text_color)::text " +
		"FROM unique_gifts cu JOIN gift_receipts cr ON cr.unique_id = cu.id AND cr.owner_id = " + p + "id AND NOT cr.converted " +
		"JOIN collectible_sets cs ON cs.id = cu.set_id JOIN collectible_models cm ON cm.set_id = cu.set_id AND cm.name = cu.model " +
		"JOIN collectible_patterns cpt ON cpt.set_id = cu.set_id AND cpt.name = cu.pattern " +
		"JOIN collectible_backdrops cb ON cb.set_id = cu.set_id AND cb.name = cu.backdrop WHERE cu.id = " + p + "emoji_status_collectible)"
}

const maxRecentStatuses = 50

// RecentStatuses are the emoji documents the user has worn, latest first.
func (s *Service) RecentStatuses(ctx context.Context, user int64) ([]int64, error) {
	rows, err := s.db.Query(ctx, "SELECT document_id FROM recent_emoji_statuses WHERE user_id=$1 ORDER BY used_at DESC LIMIT $2", user, maxRecentStatuses)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

func (s *Service) ClearRecentStatuses(ctx context.Context, user int64) error {
	_, err := s.db.Exec(ctx, "DELETE FROM recent_emoji_statuses WHERE user_id=$1", user)
	return err
}

// Frozen accounts can sign in and read, but not change anything.
func (u User) Frozen() bool { return u.State == "frozen" }

var userCols = "id, username, name, password, role, disabled, is_system, is_bot, coalesce(phone, ''), premium_until, bio, username_public, created_at, coalesce(photo_id, 0), account_state, coalesce(emoji_status_id, 0), coalesce(emoji_status_until, 0)" + ", " + aliasesCol("users.") + ", " + collectibleCol("users.")

func (s *Service) scanUser(row pgx.Row) (User, error) {
	var u User
	var phone string
	var aliases []string
	var collectible *string
	err := row.Scan(&u.ID, &u.Username, &u.Name, &u.password, &u.Role, &u.Disabled, &u.IsSystem, &u.IsBot, &phone,
		&u.PremiumUntil, &u.Bio, &u.UsernamePublic, &u.CreatedAt, &u.PhotoID, &u.State, &u.EmojiStatus, &u.EmojiUntil, &aliases, &collectible)
	if err != nil {
		return u, err
	}
	if collectible != nil {
		var c CollectibleStatus
		if json.Unmarshal([]byte(*collectible), &c) == nil {
			u.Collectible = &c
		}
	}
	u.Aliases = parseAliases(aliases, u.Username)
	if phone != "" {
		if u.Phone, err = s.secret.DecryptPhone(phone, fmt.Sprintf("user:%d", u.ID)); err != nil {
			return u, fmt.Errorf("user %d phone: %w", u.ID, err)
		}
	}
	return u, nil
}

func (s *Service) User(ctx context.Context, id int64) (User, bool, error) {
	u, err := s.scanUser(s.db.QueryRow(ctx, "SELECT "+userCols+" FROM users WHERE id=$1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return u, false, nil
	}
	return u, err == nil, err
}

// ErrFrozen is greengram-web's answer to a frozen account's writes.
var ErrFrozen = &Error{Status: 403, Message: "This account is frozen. You can read existing content, but cannot make changes until an administrator unfreezes it."}

// FrozenSince is when an admin last froze the account, from the audit
// log; 0 if it came frozen from an import.
func (s *Service) FrozenSince(ctx context.Context, id int64) (int64, error) {
	var at int64
	err := s.db.QueryRow(ctx, `SELECT coalesce(max(extract(epoch FROM created_at))::bigint, 0) FROM audit_log
		WHERE action='account_frozen' AND target_id=$1`, id).Scan(&at)
	return at, err
}

func (s *Service) userByUsername(ctx context.Context, username string) (User, bool, error) {
	u, err := s.scanUser(s.db.QueryRow(ctx, "SELECT "+userCols+" FROM users WHERE lower(username)=lower($1)", username))
	if errors.Is(err, pgx.ErrNoRows) {
		return u, false, nil
	}
	return u, err == nil, err
}

func (s *Service) userByPhone(ctx context.Context, phone string) (User, bool, error) {
	u, err := s.scanUser(s.db.QueryRow(ctx, "SELECT "+userCols+" FROM users WHERE phone_lookup=$1", s.secret.PhoneLookup(phone)))
	if errors.Is(err, pgx.ErrNoRows) {
		return u, false, nil
	}
	return u, err == nil, err
}

var usernameRe = regexp.MustCompile(`^[a-z0-9_]{3,32}$`)

// Names the built-in bots and system accounts use.
var reservedUsernames = map[string]bool{
	"wallet": true, "botfather": true, "stickers": true, "fragmebot": true, "spambot": true, "bugsbot": true, "greengram_stars": true, "admin": true,
	"instance_premium_payments": true, "instance_stars_payments": true, "instance_announcements": true, "announcements": true,
	"instance_giveaways": true, "instance_gifts": true, "instance_deleted_accounts": true,
}

func normUsername(v string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(v)), "@")
}

func digits(phone string) string {
	var b strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func newToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Hash is how session and flow cookies are stored.
func Hash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// limiter is a fixed-window counter per key. One process serves the
// instance, so memory is enough; a restart forgives everyone.
type limiter struct {
	mu    sync.Mutex
	m     map[string]*bucket
	sweep time.Time // next pass over m for windows that ended
}

type bucket struct {
	n     int
	until time.Time
}

// once a minute, not per hit: a map kept full by a flood would make every
// hit a scan
const limiterSweep = time.Minute

func (l *limiter) hit(key string, max int, window time.Duration, now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !now.Before(l.sweep) {
		for k, b := range l.m {
			if now.After(b.until) {
				delete(l.m, k)
			}
		}
		l.sweep = now.Add(limiterSweep)
	}
	b := l.m[key]
	if b == nil || now.After(b.until) {
		b = &bucket{until: now.Add(window)}
		l.m[key] = b
	}
	b.n++
	if b.n > max {
		return fail(429, "Too many attempts. Try again later.")
	}
	return nil
}

// limitIP is the address a limit counts: IPv6 by /64, since one host gets
// a whole /64 and could rotate through it.
func limitIP(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap()
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}

// Users loads several accounts at once; missing ids are left out.
func (s *Service) Users(ctx context.Context, list []int64) (map[int64]User, error) {
	rows, err := s.db.Query(ctx, "SELECT "+userCols+" FROM users WHERE id = ANY($1)", list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]User{}
	for rows.Next() {
		u, err := s.scanUser(rows)
		if err != nil {
			return nil, err
		}
		out[u.ID] = u
	}
	return out, rows.Err()
}

func (s *Service) UserByUsername(ctx context.Context, username string) (User, bool, error) {
	return s.userByUsername(ctx, normUsername(username))
}

func (s *Service) UserByPhone(ctx context.Context, phone string) (User, bool, error) {
	return s.userByPhone(ctx, "+"+digits(phone))
}

// SearchUsernames finds public usernames starting with prefix, and bots
// with a word of their name starting with it ("fragment" finds @fragmebot,
// named Fragment). People are found by username only, like on Telegram.
func (s *Service) SearchUsernames(ctx context.Context, prefix string, limit int) ([]User, error) {
	prefix = normUsername(prefix)
	if len(prefix) < 2 {
		return nil, nil
	}
	// usernames are [a-z0-9_]; only _ needs escaping
	like := strings.ReplaceAll(prefix, "_", `\_`) + "%"
	rows, err := s.db.Query(ctx, "SELECT "+userCols+` FROM users WHERE NOT disabled AND NOT is_system
		AND (username_public AND (lower(username) LIKE $1 OR is_bot AND (lower(name) LIKE $1 OR lower(name) LIKE '% ' || $1))
			OR EXISTS(SELECT 1 FROM fragment_items f WHERE f.owner_id = users.id AND f.kind = 'username' AND f.active AND NOT f.listed AND lower(f.title) LIKE '@' || $1))
		ORDER BY lower(username) LIKE $1 DESC, length(username), id LIMIT $2`, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := s.scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SessionHashes are the user's live sessions, to drop pushes to ended ones.
func (s *Service) SessionHashes(ctx context.Context, user int64) (map[string]bool, error) {
	rows, err := s.db.Query(ctx, "SELECT token_hash FROM sessions WHERE user_id=$1 AND expires_at > now()", user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out[h] = true
	}
	return out, rows.Err()
}
