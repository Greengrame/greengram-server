package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/erase"
	"github.com/Greengrame/greengram-server/internal/password"
)

// unlimitedPremium is premium-policy.js's "forever": the last second of 9999.
const unlimitedPremium = 253402300799

// member is one row of the members list, shaped like the web's safeUser().
type member struct {
	ID              int64  `json:"id"`
	Username        string `json:"username"`
	Name            string `json:"name"`
	Role            string `json:"role"`
	Disabled        bool   `json:"disabled"`
	AccountState    string `json:"accountState"`
	IsSystem        bool   `json:"isSystem"`
	IsVerified      bool   `json:"isVerified"`
	Bio             string `json:"bio"`
	UsernamePublic  bool   `json:"usernamePublic"`
	PremiumStartsAt int64  `json:"premiumStartsAt"`
	PremiumUntil    int64  `json:"premiumUntil"`
	Unlimited       bool   `json:"premiumUnlimited"`
	IsPremium       bool   `json:"isPremium"`
	Stars           int64  `json:"stars"`
	CreatedAt       int64  `json:"createdAt"`
	SessionCount    int    `json:"sessionCount"`
	PhoneNumber     string `json:"phoneNumber,omitempty"`
	// a string: photo ids don't fit a JS number
	AvatarPhotoID string `json:"avatarPhotoId,omitempty"`

	isBot bool
}

const memberCols = `u.id, u.username, u.name, u.role, u.disabled, u.account_state, u.is_suspended, u.is_system, u.is_bot,
	EXISTS(SELECT 1 FROM peer_verifications v WHERE v.user_id=u.id), u.bio, u.username_public, u.premium_starts_at,
	u.premium_until, extract(epoch FROM u.created_at)::bigint,
	(SELECT count(*) FROM sessions s WHERE s.user_id=u.id AND s.expires_at > now()), coalesce(u.photo_id, 0),
	(SELECT coalesce(sum(l.amount), 0) FROM star_ledger l WHERE l.user_id=u.id)`

func scanMember(row pgx.Row) (member, error) {
	var m member
	var suspended bool
	var sessions, photo int64
	err := row.Scan(&m.ID, &m.Username, &m.Name, &m.Role, &m.Disabled, &m.AccountState, &suspended, &m.IsSystem, &m.isBot,
		&m.IsVerified, &m.Bio, &m.UsernamePublic, &m.PremiumStartsAt, &m.PremiumUntil, &m.CreatedAt, &sessions, &photo, &m.Stars)
	if photo != 0 {
		m.AvatarPhotoID = strconv.FormatInt(photo, 10)
	}
	if suspended {
		m.AccountState = "suspended"
	}
	now := time.Now().Unix()
	m.Unlimited = m.PremiumUntil == unlimitedPremium
	m.IsPremium = m.PremiumUntil > now && m.PremiumStartsAt <= now
	m.SessionCount = int(sessions)
	return m, err
}

// people is everyone the panel manages as a member: no system identities,
// no bots.
const people = "NOT u.is_system AND NOT u.is_bot"

func (a *API) members(ctx context.Context) ([]member, error) {
	rows, err := a.DB.Query(ctx, "SELECT "+memberCols+" FROM users u WHERE "+people+" ORDER BY u.id")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (member, error) { return scanMember(r) })
}

func (a *API) member(ctx context.Context, q pgx.Row) (member, bool, error) {
	m, err := scanMember(q)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, false, nil
	}
	return m, err == nil, err
}

func (a *API) loadMember(ctx context.Context, tx pgx.Tx, id int64) (member, bool, error) {
	if tx != nil {
		return a.member(ctx, tx.QueryRow(ctx, "SELECT "+memberCols+" FROM users u WHERE u.id=$1 FOR UPDATE OF u", id))
	}
	return a.member(ctx, a.DB.QueryRow(ctx, "SELECT "+memberCols+" FROM users u WHERE u.id=$1", id))
}

// overview is GET /api/admin, everything the panel shows without asking again.
func (a *API) overview(q *req) (any, error) {
	ctx := q.ctx
	out := map[string]any{
		"fragmentInventory": []any{},
		"starTransactions":  []any{},
		"delivery":          map[string]bool{"smsConfigured": false, "consoleEnabled": false},
	}
	users, err := a.members(ctx)
	if err != nil {
		return nil, err
	}
	out["users"] = users
	if out["bots"], err = a.botList(ctx); err != nil {
		return nil, err
	}

	var chats, private, inChannels int64
	if err := a.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM channels WHERE NOT deleted),
		(SELECT count(*) FROM messages WHERE owner_id=from_id),
		(SELECT count(*) FROM channel_messages WHERE action IS NULL)`).Scan(&chats, &private, &inChannels); err != nil {
		return nil, err
	}
	out["chats"], out["messageCount"] = chats, private+inChannels

	if out["customCountries"], err = a.customCountries(ctx); err != nil {
		return nil, err
	}
	if out["recent"], err = a.recent(ctx); err != nil {
		return nil, err
	}
	if out["audit"], err = a.auditLog(ctx, "", 100); err != nil {
		return nil, err
	}
	if out["settings"], err = a.Settings.All(ctx); err != nil {
		return nil, err
	}
	if out["conversations"], err = a.conversations(ctx); err != nil {
		return nil, err
	}
	if out["activity"], err = a.activity(ctx); err != nil {
		return nil, err
	}
	if out["starTransactions"], err = a.starsJournal(ctx); err != nil {
		return nil, err
	}
	for _, f := range a.Extra {
		if err := f(ctx, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// recent is the moderation feed. Private messages count once (the sender's
// copy); ids are "p<shared id>" and "c<channel>_<id>" since the two kinds
// share no id space.
func (a *API) recent(ctx context.Context) ([]map[string]any, error) {
	rows, err := a.DB.Query(ctx, `SELECT * FROM (
		(SELECT 'p' || m.shared_id, m.from_id, u.name, m.text, m.date,
			CASE WHEN m.peer_id = m.owner_id THEN 'Saved Messages' ELSE u.name || ' ↔ ' || coalesce(p.name, '') END
		FROM messages m JOIN users u ON u.id = m.from_id LEFT JOIN users p ON p.id = m.peer_id
		WHERE m.owner_id = m.from_id ORDER BY m.date DESC LIMIT 50)
		UNION ALL
		(SELECT 'c' || cm.channel_id || '_' || cm.id, cm.from_id, coalesce(u.name, c.title), cm.text, cm.date, c.title
		FROM channel_messages cm JOIN channels c ON c.id = cm.channel_id LEFT JOIN users u ON u.id = cm.from_id
		WHERE cm.action IS NULL AND NOT c.deleted ORDER BY cm.date DESC LIMIT 50)
	) r ORDER BY 5 DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) {
		var id, author, body, title string
		var from int64
		var date int
		err := r.Scan(&id, &from, &author, &body, &date, &title)
		return map[string]any{"id": id, "user_id": from, "author": author, "body": body, "created_at": date, "chat_title": title}, err
	})
}

func (a *API) conversations(ctx context.Context) ([]map[string]any, error) {
	rows, err := a.DB.Query(ctx, `SELECT c.id, c.title, c.creator_id, EXISTS(SELECT 1 FROM peer_verifications v WHERE v.channel_id=c.id),
		coalesce(u.name, ''), coalesce(c.username, ''), c.megagroup,
		(SELECT count(*) FROM channel_members m WHERE m.channel_id=c.id AND m.status IN ('creator', 'admin', 'member', 'restricted')),
		(SELECT count(*) FROM channel_messages m WHERE m.channel_id=c.id AND m.action IS NULL)
		FROM channels c LEFT JOIN users u ON u.id = c.creator_id WHERE NOT c.deleted ORDER BY c.id DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) {
		var id, creator, members, msgs int64
		var title, owner, username string
		var verified, group bool
		err := r.Scan(&id, &title, &creator, &verified, &owner, &username, &group, &members, &msgs)
		kind, vis := "channel", "private"
		if group {
			kind = "group"
		}
		if username != "" {
			vis = "public"
		}
		return map[string]any{"id": id, "title": title, "created_by": creator, "isVerified": verified, "owner": owner,
			"username": nilIfEmpty(username), "visibility": vis, "kind": kind, "member_count": members, "message_count": msgs}, err
	})
}

func (a *API) activity(ctx context.Context) ([]map[string]any, error) {
	rows, err := a.DB.Query(ctx, `SELECT day, sum(n)::bigint FROM (
		SELECT to_char(to_timestamp(date) AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day, count(*) AS n FROM messages
			WHERE owner_id=from_id AND date >= extract(epoch FROM now())::int - 604800 GROUP BY 1
		UNION ALL
		SELECT to_char(to_timestamp(date) AT TIME ZONE 'UTC', 'YYYY-MM-DD'), count(*) FROM channel_messages
			WHERE action IS NULL AND date >= extract(epoch FROM now())::int - 604800 GROUP BY 1
	) d GROUP BY day ORDER BY day`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) {
		var day string
		var n int64
		err := r.Scan(&day, &n)
		return map[string]any{"day": day, "count": n}, err
	})
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// action is PATCH /api/admin: disable, role, premium, stars.
func (a *API) action(q *req) (any, error) {
	id, ok := q.int("id")
	if !ok {
		return nil, fail(400, "Choose an account.")
	}
	kind, _ := q.str("action")
	if kind == "stars" {
		if err := a.adjustStars(q, id); err != nil {
			return nil, err
		}
		return a.overview(q)
	}
	err := a.tx(q.ctx, func(tx pgx.Tx) error {
		t, found, err := a.loadMember(q.ctx, tx, id)
		if err != nil {
			return err
		}
		if !found {
			return fail(404, "Account not found.")
		}
		if t.IsSystem {
			return fail(400, "System identities cannot be managed as member accounts.")
		}
		switch kind {
		case "disable":
			disabled, ok := q.boolean("disabled")
			switch {
			case id == q.me.ID || t.Role == "admin":
				return fail(400, "Administrator accounts cannot be disabled here.")
			case !ok:
				return fail(400, "Choose a valid status.")
			case t.AccountState == "deleted":
				return fail(409, "Deleted accounts cannot be restored.")
			}
			state := "active"
			if disabled {
				state = "locked"
			}
			if _, err := tx.Exec(q.ctx, "UPDATE users SET disabled=$2, account_state=$3, is_suspended=false WHERE id=$1", id, disabled, state); err != nil {
				return err
			}
			if disabled {
				if _, err := tx.Exec(q.ctx, "DELETE FROM sessions WHERE user_id=$1", id); err != nil {
					return err
				}
			}
		case "role":
			role, _ := q.str("role")
			switch {
			case t.isBot:
				return fail(400, "Bot accounts cannot be administrators.")
			case role != "admin" && role != "member":
				return fail(400, "Choose a valid role.")
			case id == q.me.ID:
				return fail(400, "You cannot change your own administrator role.")
			case t.Disabled && role == "admin":
				return fail(400, "Enable the account before promoting it.")
			}
			if t.Role == "admin" && role != "admin" {
				var n int
				if err := tx.QueryRow(q.ctx, "SELECT count(*) FROM users WHERE role='admin' AND NOT disabled").Scan(&n); err != nil {
					return err
				}
				if n <= 1 {
					return fail(400, "The workspace needs an active administrator.")
				}
			}
			if _, err := tx.Exec(q.ctx, "UPDATE users SET role=$2 WHERE id=$1", id, role); err != nil {
				return err
			}
		case "premium":
			starts, ends, ok := premiumWindow(q, time.Now().Unix())
			if !ok {
				return fail(400, "Choose days, hours and minutes, a valid start/end date range, zero to revoke, or Unlimited. Dated access must end before year 9999.")
			}
			if _, err := tx.Exec(q.ctx, "UPDATE users SET premium_starts_at=$2, premium_until=$3 WHERE id=$1", id, starts, ends); err != nil {
				return err
			}
		default:
			return fail(400, "Unknown admin action.")
		}
		return audit(q.ctx, tx, q.me.ID, kind, id, q.body)
	})
	if err != nil {
		return nil, err
	}
	return a.overview(q)
}

// adjustStars is the stars action; the ledger lives with the stars feature,
// which sets StarsAdjust.
func (a *API) adjustStars(q *req, id int64) error {
	if a.StarsAdjust == nil {
		return fail(501, "Stars are not available on this server yet.")
	}
	amount, _ := q.int("amount")
	ref, _ := q.str("reference")
	note, _ := q.str("note")
	_, _, err := a.StarsAdjust(q.ctx, q.me.ID, id, amount, strings.TrimSpace(note), ref)
	return err
}

// starTransaction is POST /api/admin/star-transactions, the panel's Stars
// dialog; the receipt is what it shows next.
func (a *API) starTransaction(q *req) (any, error) {
	if a.StarsAdjust == nil {
		return nil, fail(501, "Stars are not available on this server yet.")
	}
	recipient, _ := q.int("recipientId")
	amount, _ := q.int("amount")
	note, _ := q.str("note")
	requestID, _ := q.str("requestId")
	id, bal, err := a.StarsAdjust(q.ctx, q.me.ID, recipient, amount, strings.TrimSpace(note), requestID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "transactionId": id, "amount": amount, "recipientId": recipient, "balance": bal}, nil
}

func (a *API) starJournal(q *req) (any, error) { return a.starsJournal(q.ctx) }

// starsJournal is starsJournal(): the newest 200 ledger rows.
func (a *API) starsJournal(ctx context.Context) ([]map[string]any, error) {
	rows, err := a.DB.Query(ctx, `SELECT l.id, l.user_id, l.amount, l.reason, l.reference, l.created_at, l.created_by, u.name, u.username,
		l.transaction_id, l.title, l.note, l.peer_id FROM star_ledger l JOIN users u ON u.id = l.user_id ORDER BY l.id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) {
		var id, user, amount, by int64
		var at int
		var reason, ref, name, username, txID, title, note string
		var peer *int64
		err := r.Scan(&id, &user, &amount, &reason, &ref, &at, &by, &name, &username, &txID, &title, &note, &peer)
		return map[string]any{"id": id, "user_id": user, "amount": amount, "reason": reason, "reference": ref, "created_at": at,
			"created_by": by, "name": name, "username": username, "transactionId": txID, "title": title, "note": note, "peerId": peer}, err
	})
}

// premiumWindow follows premium-policy.js: unlimited, explicit dates, or a
// duration from now (zero revokes).
func premiumWindow(q *req, now int64) (starts, ends int64, ok bool) {
	if q.has("unlimited") {
		u, isBool := q.boolean("unlimited")
		if !isBool {
			return 0, 0, false
		}
		if u {
			return now, unlimitedPremium, true
		}
	}
	if q.has("startsAt") || q.has("endsAt") {
		s, ok1 := q.int("startsAt")
		e, ok2 := q.int("endsAt")
		if q.has("days") || q.has("hours") || q.has("minutes") || !ok1 || !ok2 || s < 0 || e <= s || e >= unlimitedPremium {
			return 0, 0, false
		}
		return s, e, true
	}
	if !q.has("days") && !q.has("hours") && !q.has("minutes") {
		return 0, 0, false
	}
	part := func(key string, max int64) (int64, bool) {
		if !q.has(key) {
			return 0, true
		}
		v, ok := q.int(key)
		return v, ok && v >= 0 && (max == 0 || v <= max)
	}
	d, ok1 := part("days", 0)
	h, ok2 := part("hours", 23)
	m, ok3 := part("minutes", 59)
	if !ok1 || !ok2 || !ok3 || d > 3_000_000 {
		return 0, 0, false
	}
	dur := d*86400 + h*3600 + m*60
	if dur == 0 {
		return 0, 0, true
	}
	if now+dur >= unlimitedPremium {
		return 0, 0, false
	}
	return now, now + dur, true
}

// human is a member account the panel may manage: not a system identity,
// not a bot.
func (a *API) human(ctx context.Context, id int64) (member, error) {
	m, found, err := a.loadMember(ctx, nil, id)
	if err != nil {
		return m, err
	}
	if !found || m.IsSystem || m.isBot {
		return m, fail(404, "Person account not found.")
	}
	return m, nil
}

type userSession struct {
	ID         string `json:"id"`
	UserAgent  string `json:"userAgent"`
	CreatedAt  int64  `json:"createdAt"`
	LastActive int64  `json:"lastActive"`
	ExpiresAt  int64  `json:"expiresAt"`
	IsCurrent  bool   `json:"isCurrent"`
}

// agent is what the panel's deviceInfo() parses: the browser's UA, or for
// apps that signed in over MTProto, the device and app they reported.
const agent = `CASE WHEN s.device_model <> '' THEN s.device_model || ' · ' || s.app_version ELSE coalesce(nullif(s.user_agent, ''), 'Browser') END`

func (a *API) userSessions(ctx context.Context, id int64, current string) ([]userSession, error) {
	rows, err := a.DB.Query(ctx, `SELECT s.token_hash, `+agent+`, extract(epoch FROM s.created_at)::bigint,
		extract(epoch FROM s.last_active)::bigint, extract(epoch FROM s.expires_at)::bigint
		FROM sessions s WHERE s.user_id=$1 AND s.expires_at > now() ORDER BY s.last_active DESC, s.token_hash`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (userSession, error) {
		var s userSession
		err := r.Scan(&s.ID, &s.UserAgent, &s.CreatedAt, &s.LastActive, &s.ExpiresAt)
		s.IsCurrent = s.ID == current
		return s, err
	})
}

func (a *API) userDetail(q *req) (any, error) {
	id, err := q.pathID("id")
	if err != nil {
		return nil, err
	}
	m, err := a.human(q.ctx, id)
	if err != nil {
		return nil, err
	}
	return a.detail(q, m)
}

func (a *API) detail(q *req, m member) (any, error) {
	ctx := q.ctx
	u, _, err := a.Accounts.User(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	m.PhoneNumber = u.Phone
	sessions, err := a.userSessions(ctx, m.ID, q.session)
	if err != nil {
		return nil, err
	}
	m.SessionCount = len(sessions)

	var msgs int64
	if err := a.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM messages WHERE owner_id=$1 AND from_id=$1)
		+ (SELECT count(*) FROM channel_messages WHERE from_id=$1 AND action IS NULL)`, m.ID).Scan(&msgs); err != nil {
		return nil, err
	}
	rows, err := a.DB.Query(ctx, `SELECT c.id, c.title, c.megagroup, coalesce(c.username, ''), m.status
		FROM channel_members m JOIN channels c ON c.id = m.channel_id
		WHERE m.user_id=$1 AND m.status IN ('creator', 'admin', 'member', 'restricted') AND NOT c.deleted ORDER BY c.id`, m.ID)
	if err != nil {
		return nil, err
	}
	memberships, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) {
		var id int64
		var title, username, status string
		var group bool
		err := r.Scan(&id, &title, &group, &username, &status)
		kind, isChannel := "channel", 1
		if group {
			kind, isChannel = "group", 0
		}
		if strings.TrimSpace(title) == "" {
			title = "Chat #" + fmt.Sprint(id)
		}
		return map[string]any{"id": id, "title": title, "kind": kind, "isChannel": isChannel, "username": nilIfEmpty(username), "role": status}, err
	})
	if err != nil {
		return nil, err
	}

	privacy := map[string]any{}
	rows, err = a.DB.Query(ctx, "SELECT key, rules FROM privacy_rules WHERE user_id=$1", m.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		var p struct {
			Visibility string   `json:"visibility"`
			AllowUsers []string `json:"allowUserIds"`
			BlockUsers []string `json:"blockUserIds"`
			AllowChats []string `json:"allowChatIds"`
			BlockChats []string `json:"blockChatIds"`
		}
		json.Unmarshal(raw, &p)
		privacy[key] = map[string]any{"visibility": p.Visibility, "allowedPeople": len(p.AllowUsers), "blockedPeople": len(p.BlockUsers),
			"allowedChats": len(p.AllowChats), "blockedChats": len(p.BlockChats)}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var photos int64
	ttl := 365
	var lastSeen int64
	var deletedAt *int64
	var source *string
	if err := a.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM user_photos WHERE user_id=$1), coalesce((SELECT account_ttl_days FROM account_prefs WHERE user_id=$1), 365),
		extract(epoch FROM u.last_seen_at)::bigint, extract(epoch FROM u.deleted_at)::bigint, u.deletion_source FROM users u WHERE u.id=$1`, m.ID).Scan(&photos, &ttl, &lastSeen, &deletedAt, &source); err != nil {
		return nil, err
	}
	ledger, err := a.memberLedger(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	frag, err := a.fragmentHeld(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"user":           m,
		"ledger":         ledger,
		"messageCount":   msgs,
		"chatCount":      len(memberships),
		"photoCount":     photos,
		"sessions":       sessions,
		"memberships":    memberships,
		"privacy":        privacy,
		"accountTtlDays": ttl,
		"lastSeenAt":     lastSeen,
		"deletedAt":      deletedAt,
		"deletionSource": source,
		"fragment":       frag,
	}, nil
}

// memberLedger is the account's newest 200 Stars ledger rows.
func (a *API) memberLedger(ctx context.Context, id int64) ([]map[string]any, error) {
	rows, err := a.DB.Query(ctx, "SELECT id, amount, reason, created_at, title FROM star_ledger WHERE user_id=$1 ORDER BY id DESC LIMIT 200", id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) {
		var lid, amount int64
		var at int
		var reason, title string
		err := r.Scan(&lid, &amount, &reason, &at, &title)
		return map[string]any{"id": lid, "amount": amount, "reason": reason, "created_at": at, "title": title}, err
	})
}

var usernameRe = regexp.MustCompile(`^[a-z0-9_]{3,32}$`)

var reserved = map[string]bool{
	"wallet": true, "botfather": true, "stickers": true, "fragmebot": true, "greengram_stars": true, "admin": true,
	"instance_premium_payments": true, "instance_stars_payments": true, "instance_announcements": true, "announcements": true,
	"instance_giveaways": true, "instance_gifts": true, "instance_deleted_accounts": true,
}

func (a *API) editUser(q *req) (any, error) {
	id, err := q.pathID("id")
	if err != nil {
		return nil, err
	}
	m, err := a.human(q.ctx, id)
	if err != nil {
		return nil, err
	}
	if m.AccountState == "deleted" {
		return nil, fail(409, "Deleted account profiles cannot be restored or edited.")
	}
	allowed := map[string]bool{"name": true, "username": true, "bio": true, "usernamePublic": true, "removePhoto": true}
	var fields []string
	for k := range q.body {
		if !allowed[k] {
			return nil, fail(400, "Only name, username, bio, usernamePublic and removePhoto can be edited here.")
		}
		fields = append(fields, k)
	}
	if len(fields) == 0 {
		return nil, fail(400, "Provide profile settings.")
	}
	name, bio, username, public := m.Name, m.Bio, m.Username, m.UsernamePublic
	if q.has("name") {
		s, _ := q.str("name")
		name = strings.TrimSpace(s)
	}
	bioOK := true
	if q.has("bio") {
		bio, bioOK = q.str("bio")
	}
	if q.has("username") {
		s, _ := q.str("username")
		username = strings.ToLower(strings.TrimSpace(s))
	}
	limit := 70
	if m.IsPremium {
		limit = 140
	}
	if name == "" || utf8.RuneCountInString(name) > 80 || !bioOK || utf8.RuneCountInString(bio) > limit {
		return nil, fail(400, fmt.Sprintf("Enter a name of up to 80 characters and a bio of up to %d characters.", limit))
	}
	if q.has("usernamePublic") {
		var isBool bool
		if public, isBool = q.boolean("usernamePublic"); !isBool {
			return nil, fail(400, "Profile visibility and photo removal must be boolean.")
		}
	}
	if q.has("removePhoto") {
		if _, isBool := q.boolean("removePhoto"); !isBool {
			return nil, fail(400, "Profile visibility and photo removal must be boolean.")
		}
	}
	err = a.tx(q.ctx, func(tx pgx.Tx) error {
		if q.has("username") && username != strings.ToLower(m.Username) {
			if reserved[username] {
				return fail(409, "This username is reserved for a built-in bot.")
			}
			// a short collectible the account owns is fine, the shape rule is for new names
			var owned bool
			if err := tx.QueryRow(q.ctx, "SELECT EXISTS(SELECT 1 FROM fragment_items WHERE kind='username' AND lower(title)='@'||$1 AND owner_id=$2 AND NOT listed)", username, id).Scan(&owned); err != nil {
				return err
			}
			if !owned && !usernameRe.MatchString(username) {
				return fail(400, "USERNAME_INVALID")
			}
			// reservedFragmentUsername: a listing holds the name unless
			// this account owns it and it isn't up for sale
			var taken bool
			if err := tx.QueryRow(q.ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(username)=$1 AND id<>$2)
				OR EXISTS(SELECT 1 FROM channels WHERE lower(username)=$1 AND NOT deleted)
				OR EXISTS(SELECT 1 FROM fragment_items WHERE kind='username' AND lower(title)='@'||$1
					AND (owner_id IS DISTINCT FROM $2 OR listed))`, username, id).Scan(&taken); err != nil {
				return err
			}
			if taken {
				return fail(409, "USERNAME_OCCUPIED")
			}
		}
		if _, err := tx.Exec(q.ctx, "UPDATE users SET name=$2, username=$3, bio=$4, username_public=$5 WHERE id=$1", id, name, username, bio, public); err != nil {
			return err
		}
		removed, _ := q.boolean("removePhoto")
		if removed {
			if _, err := tx.Exec(q.ctx, "UPDATE users SET photo_id=NULL WHERE id=$1", id); err != nil {
				return err
			}
		}
		return audit(q.ctx, tx, q.me.ID, "admin_profile_updated", id, map[string]any{"fields": fields, "removedPhoto": removed})
	})
	if err != nil {
		return nil, err
	}
	if m, err = a.human(q.ctx, id); err != nil {
		return nil, err
	}
	d, err := a.detail(q, m)
	if err != nil {
		return nil, err
	}
	out := d.(map[string]any)
	out["ok"] = true
	return out, nil
}

var accessStates = map[string]bool{"active": true, "frozen": true, "locked": true, "banned": true, "suspended": true, "deleted": true}

// access is the moderation state machine from account-controls.js.
func (a *API) access(q *req) (any, error) {
	id, err := q.pathID("id")
	if err != nil {
		return nil, err
	}
	state, _ := q.str("state")
	reason, reasonOK := "", true
	if q.has("reason") && q.body["reason"] != nil {
		reason, reasonOK = q.str("reason")
	}
	if !accessStates[state] || !reasonOK || utf8.RuneCountInString(reason) > 500 {
		return nil, fail(400, "Choose a valid account state and a reason of up to 500 characters.")
	}
	confirmation, _ := q.str("confirmation")
	var erased *erase.Result
	err = a.tx(q.ctx, func(tx pgx.Tx) error {
		if state == "deleted" {
			res, err := a.eraseMember(q.ctx, tx, q.me.ID, id, reason, confirmation)
			erased = &res
			return err
		}
		return a.setAccess(q.ctx, tx, q.me.ID, id, state, reason, confirmation)
	})
	if err != nil {
		return nil, err
	}
	if erased != nil && a.Erased != nil {
		a.Erased(*erased)
	}
	a.tellAccessChanged(id)
	m, _, err := a.loadMember(q.ctx, nil, id)
	return m, err
}

// setAccess is the state change behind access, with its audit entry, in tx.
// The Reports page freezes through it too.
func (a *API) setAccess(ctx context.Context, tx pgx.Tx, actor, id int64, state, reason, confirmation string) error {
	t, found, err := a.loadMember(ctx, tx, id)
	if err != nil {
		return err
	}
	switch {
	case !found:
		return fail(404, "Account not found.")
	case t.IsSystem:
		return fail(400, "System identities cannot be restricted as member accounts.")
	case t.ID == actor || t.Role == "admin":
		return fail(400, "You cannot restrict your own account or another administrator here.")
	case t.isBot:
		return fail(400, "Use bot management for bot accounts.")
	case t.AccountState == "deleted":
		return fail(409, "Deleted accounts cannot be restored.")
	}
	if state == "deleted" {
		return fail(400, "Delete accounts through eraseMember.")
	}
	blocked := state == "locked" || state == "banned" || state == "suspended" || state == "deleted"
	stored := state
	if state == "suspended" {
		stored = "locked"
	}
	if _, err := tx.Exec(ctx, "UPDATE users SET account_state=$2, disabled=$3, is_suspended=$4 WHERE id=$1",
		id, stored, blocked, state == "suspended"); err != nil {
		return err
	}
	if blocked {
		if _, err := tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", id); err != nil {
			return err
		}
	}
	if state != "frozen" {
		// settled by hand; nothing left for the appeal to decide
		if err := closeAppeal(ctx, tx, id, actor, state == "active"); err != nil {
			return err
		}
	}
	return audit(ctx, tx, actor, "account_"+state, id, map[string]any{
		"previousState": t.AccountState, "state": state, "reason": strings.TrimSpace(reason), "erased": false})
}

// eraseMember is Delete Account: the same checks as any access change plus
// the typed username, then the deletion every path shares (internal/erase),
// which writes its own audit row.
func (a *API) eraseMember(ctx context.Context, tx pgx.Tx, actor, id int64, reason, confirmation string) (erase.Result, error) {
	t, found, err := a.loadMember(ctx, tx, id)
	if err != nil {
		return erase.Result{}, err
	}
	switch {
	case !found:
		return erase.Result{}, fail(404, "Account not found.")
	case t.IsSystem:
		return erase.Result{}, fail(400, "System identities cannot be restricted as member accounts.")
	case t.ID == actor || t.Role == "admin":
		return erase.Result{}, fail(400, "You cannot restrict your own account or another administrator here.")
	case t.isBot:
		return erase.Result{}, fail(400, "Use bot management for bot accounts.")
	case t.AccountState == "deleted":
		return erase.Result{}, fail(409, "Deleted accounts cannot be restored.")
	case confirmation != t.Username:
		return erase.Result{}, fail(400, "Type the account username to confirm deletion.")
	}
	return erase.Erase(ctx, tx, erase.Request{User: id, Actor: actor, Source: erase.SourceAdmin, Reason: reason}, time.Now())
}

func (a *API) tellAccessChanged(id int64) {
	if a.AccessChanged != nil {
		a.AccessChanged(id)
	}
}

func (a *API) endUserSessions(q *req) (any, error) {
	id, err := q.pathID("id")
	if err != nil {
		return nil, err
	}
	if _, err := a.human(q.ctx, id); err != nil {
		return nil, err
	}
	selected := q.r.PathValue("session")
	var n int64
	err = a.tx(q.ctx, func(tx pgx.Tx) error {
		scope := "all_other"
		if selected != "" {
			scope = "selected"
			if selected == q.session {
				return fail(400, "Keep this administrator session active. Use Log Out to end it.")
			}
			tag, err := tx.Exec(q.ctx, "DELETE FROM sessions WHERE user_id=$1 AND token_hash=$2 AND expires_at > now()", id, selected)
			if err != nil {
				return err
			}
			if n = tag.RowsAffected(); n == 0 {
				return fail(404, "Session not found.")
			}
		} else {
			tag, err := tx.Exec(q.ctx, "DELETE FROM sessions WHERE user_id=$1 AND token_hash<>$2", id, q.session)
			if err != nil {
				return err
			}
			n = tag.RowsAffected()
		}
		return audit(q.ctx, tx, q.me.ID, "admin_sessions_revoked", id, map[string]any{"revoked": n, "scope": scope})
	})
	if err != nil {
		return nil, err
	}
	sessions, err := a.userSessions(q.ctx, id, q.session)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "revoked": n, "sessions": sessions}, nil
}

func (a *API) verification(q *req) (any, error) {
	kind, _ := q.str("kind")
	id, idOK := q.int("id")
	verified, vOK := q.boolean("verified")
	if kind != "user" && kind != "chat" || !idOK || id < 1 || !vOK {
		return nil, fail(400, "Choose a person, bot, group or channel and a verification status.")
	}
	col, check := "user_id", "SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND NOT disabled)"
	if kind == "chat" {
		col, check = "channel_id", "SELECT EXISTS(SELECT 1 FROM channels WHERE id=$1 AND NOT deleted)"
	}
	err := a.tx(q.ctx, func(tx pgx.Tx) error {
		var exists, previous bool
		if err := tx.QueryRow(q.ctx, check, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fail(404, "Account or conversation not found.")
		}
		if err := tx.QueryRow(q.ctx, "SELECT EXISTS(SELECT 1 FROM peer_verifications WHERE "+col+"=$1)", id).Scan(&previous); err != nil {
			return err
		}
		var err error
		if verified {
			_, err = tx.Exec(q.ctx, "INSERT INTO peer_verifications("+col+", verified_by) VALUES ($1, $2) ON CONFLICT ("+col+") DO UPDATE SET verified_by=excluded.verified_by, verified_at=now()", id, q.me.ID)
		} else {
			_, err = tx.Exec(q.ctx, "DELETE FROM peer_verifications WHERE "+col+"=$1", id)
		}
		if err != nil {
			return err
		}
		return audit(q.ctx, tx, q.me.ID, "peer_verification_updated", id, map[string]any{"kind": kind, "verified": verified, "previous": previous})
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "kind": kind, "id": id, "isVerified": verified}, nil
}

func (a *API) deleteConversation(q *req) (any, error) {
	id, err := q.pathID("id")
	if err != nil {
		return nil, fail(404, "Group or channel not found.")
	}
	var title string
	var creator int64
	err = a.DB.QueryRow(q.ctx, "SELECT title, creator_id FROM channels WHERE id=$1 AND NOT deleted", id).Scan(&title, &creator)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fail(404, "Group or channel not found.")
	}
	if err != nil {
		return nil, err
	}
	// as the creator, so the channel goes the same way a client delete
	// does; members pick it up on their next getDifference
	if _, err := a.Channels.Delete(q.ctx, creator, id); err != nil {
		return nil, err
	}
	err = a.tx(q.ctx, func(tx pgx.Tx) error {
		return audit(q.ctx, tx, q.me.ID, "delete_conversation", id, map[string]any{"title": title})
	})
	if err != nil {
		return nil, err
	}
	return ok(), nil
}

// resetPassword sets a member's password from the panel and signs out every
// device, so someone who got in with the old one is out too. Administrators
// change their own password themselves: one admin shouldn't be able to take
// over another. The password never reaches the audit log.
func (a *API) resetPassword(q *req) (any, error) {
	id, err := q.pathID("id")
	if err != nil {
		return nil, err
	}
	next, _ := q.str("password")
	if password.Validate(next) != nil {
		return nil, fail(400, password.ErrLength.Error())
	}
	hash, err := password.Hash(next)
	if err != nil {
		return nil, err
	}
	var ended int64
	err = a.tx(q.ctx, func(tx pgx.Tx) error {
		t, found, err := a.loadMember(q.ctx, tx, id)
		if err != nil {
			return err
		}
		switch {
		case !found || t.IsSystem || t.isBot:
			return fail(404, "Person account not found.")
		case t.Role == "admin":
			return fail(400, "Administrators change their own password in their account settings.")
		case t.AccountState == "deleted":
			return fail(409, "Deleted accounts cannot be restored.")
		}
		if _, err := tx.Exec(q.ctx, "UPDATE users SET password=$2 WHERE id=$1", id, hash); err != nil {
			return err
		}
		tag, err := tx.Exec(q.ctx, "DELETE FROM sessions WHERE user_id=$1", id)
		if err != nil {
			return err
		}
		ended = tag.RowsAffected()
		return audit(q.ctx, tx, q.me.ID, "admin_password_reset", id, map[string]any{"sessionsEnded": ended})
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "sessionsEnded": ended}, nil
}

// resetIdentity is Reset Info: the chosen parts of a member's profile are
// wiped (account.ResetIdentity) and the account carries on, signed in where
// it was. Same checks as Delete Account, the typed username included. The
// audit row names the fields and keeps the old phone masked, never whole.
func (a *API) resetIdentity(q *req) (any, error) {
	id, err := q.pathID("id")
	if err != nil {
		return nil, err
	}
	raw, _ := q.body["fields"].([]any)
	var fields []string
	for _, v := range raw {
		f, ok := v.(string)
		if !ok {
			return nil, fail(400, "Choose what to reset.")
		}
		fields = append(fields, f)
	}
	confirmation, _ := q.str("confirmation")
	var res account.Reset
	err = a.tx(q.ctx, func(tx pgx.Tx) error {
		t, found, err := a.loadMember(q.ctx, tx, id)
		if err != nil {
			return err
		}
		switch {
		case !found || t.IsSystem || t.isBot:
			return fail(404, "Person account not found.")
		case t.ID == q.me.ID || t.Role == "admin":
			return fail(400, "You cannot reset your own account or another administrator here.")
		case t.AccountState == "deleted":
			return fail(409, "Deleted accounts cannot be restored.")
		case confirmation != t.Username:
			return fail(400, "Type the account username to confirm the reset.")
		}
		if res, err = a.Accounts.ResetIdentity(q.ctx, tx, id, fields); err != nil {
			return err
		}
		return audit(q.ctx, tx, q.me.ID, "admin_identity_reset", id, res)
	})
	if err != nil {
		return nil, err
	}
	a.Accounts.ForgetNumbers(res)
	if a.IdentityReset != nil {
		a.IdentityReset(id)
	}
	m, err := a.human(q.ctx, id)
	if err != nil {
		return nil, err
	}
	d, err := a.detail(q, m)
	if err != nil {
		return nil, err
	}
	out := d.(map[string]any)
	out["ok"] = true
	out["reset"] = res
	return out, nil
}
