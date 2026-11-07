package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/reports"
)

// The Reports page: what people reported from the apps, grouped by the
// account or chat it's about, and what admins do about it. Every action
// closes the reports it covered and is in the audit log.

// Moderator does what the page decides through the messenger's own code, so
// people's apps hear about it (internal/api).
type Moderator interface {
	// DeleteForEveryone: chatKind "user" deletes by shared ids, "channel" by
	// the chat's ids. It returns how many messages went.
	DeleteForEveryone(ctx context.Context, chatKind string, chat int64, ids []int, shared []int64) (int, error)
	// BanFromChannel removes user from the chat and keeps them out.
	BanFromChannel(ctx context.Context, channel, user int64) error
}

// errOwner comes back from BanFromChannel for the chat's owner.
var errOwner = fail(409, "The owner of a group or channel can't be banned from it. Freeze the account or delete the chat instead.")

// reportsShown caps one listing.
const reportsShown = 500

func (a *API) registerReports(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/reports", a.handle(a.reportList))
	mux.HandleFunc("POST /api/admin/reports/{id}/dismiss", a.handle(a.dismissReports))
	mux.HandleFunc("POST /api/admin/reports/{id}/resolve", a.handle(a.resolveReports))
	mux.HandleFunc("DELETE /api/admin/messages/{id}", a.handle(a.removeMessage))
	if a.Media != nil {
		mux.HandleFunc("GET /api/admin/report-photos/{id}", a.file(a.reportPhoto))
	}
	a.Extra = append(a.Extra, func(ctx context.Context, out map[string]any) error {
		var n int64
		err := a.DB.QueryRow(ctx, "SELECT count(*) FROM content_reports WHERE status='open'").Scan(&n)
		out["openReports"] = n
		return err
	})
}

type reportPerson struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

type reportChat struct {
	Kind  string `json:"kind"` // user, group, channel
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

type reportItem struct {
	ID          int64           `json:"id"`
	Kind        string          `json:"kind"`
	Status      string          `json:"status"`
	Reporter    reportPerson    `json:"reporter"`
	Chat        reportChat      `json:"chat"`
	MsgIDs      []int           `json:"msgIds"`
	StoryID     *int            `json:"storyId"`
	Reason      []string        `json:"reason"`
	ReasonLabel string          `json:"reasonLabel"`
	Comment     string          `json:"comment"`
	Snapshot    json.RawMessage `json:"snapshot"`
	Actions     []string        `json:"actions"`
	ClosedBy    string          `json:"closedBy"`
	ClosedAt    *int64          `json:"closedAt"`
	CreatedAt   int64           `json:"createdAt"`

	chatKind  string
	chatID    int64
	targetKey string
	shared    []int64
}

type reportTarget struct {
	Key           string `json:"key"`
	Kind          string `json:"kind"` // user, bot, group, channel
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Username      string `json:"username"`
	State         string `json:"state,omitempty"` // accounts: active, frozen, ...
	IsAdmin       bool   `json:"isAdmin,omitempty"`
	Gone          bool   `json:"gone,omitempty"` // a deleted chat
	AvatarPhotoID string `json:"avatarPhotoId,omitempty"`
	// what the messenger opens it with: /#<peer id>
	PeerID string `json:"peerId"`

	creator int64 // a chat's owner, who can't be banned from it
}

type reportGroup struct {
	Key       string       `json:"key"`
	Target    reportTarget `json:"target"`
	Open      int          `json:"open"`
	Reporters int          `json:"reporters"`
	LatestAt  int64        `json:"latestAt"`
	Reasons   []string     `json:"reasons"`
	CanDelete bool         `json:"canDelete"`
	CanFreeze bool         `json:"canFreeze"`
	CanBan    bool         `json:"canBan"`
	Reports   []reportItem `json:"reports"`
}

const reportCols = `r.id, r.kind, r.status, r.reporter_id, ru.name, ru.username, r.chat_kind, r.chat_id, r.target_key, r.msg_ids, r.shared_ids,
	r.story_id, r.reason, r.comment, r.snapshot, r.actions, coalesce(cu.name, ''), extract(epoch FROM r.closed_at)::bigint,
	extract(epoch FROM r.created_at)::bigint`

func scanReport(row pgx.Row) (reportItem, error) {
	var v reportItem
	var snap []byte
	err := row.Scan(&v.ID, &v.Kind, &v.Status, &v.Reporter.ID, &v.Reporter.Name, &v.Reporter.Username, &v.chatKind, &v.chatID,
		&v.targetKey, &v.MsgIDs, &v.shared, &v.StoryID, &v.Reason, &v.Comment, &snap, &v.Actions, &v.ClosedBy, &v.ClosedAt, &v.CreatedAt)
	v.Snapshot = snap
	v.ReasonLabel = reports.Label(v.Reason)
	return v, err
}

var reportStatuses = map[string]string{
	"open":   "r.status = 'open'",
	"closed": "r.status <> 'open'",
	"all":    "true",
}

// reportList is GET /api/admin/reports?status=open|closed|all&reason=<top
// key>&target=user|bot|group|channel. Groups come newest first.
func (a *API) reportList(q *req) (any, error) {
	v := q.r.URL.Query()
	status := v.Get("status")
	if status == "" {
		status = "open"
	}
	where, ok := reportStatuses[status]
	if !ok {
		return nil, fail(400, "Choose open, closed or all reports.")
	}
	args := []any{reportsShown}
	if reason := v.Get("reason"); reason != "" && reason != "all" {
		args = append(args, reason)
		where += fmt.Sprintf(" AND r.reason[1] = $%d", len(args))
	}
	if target := v.Get("target"); target != "" && target != "all" {
		args = append(args, target)
		where += fmt.Sprintf(" AND r.target_type = $%d", len(args))
	}
	rows, err := a.DB.Query(q.ctx, `SELECT `+reportCols+` FROM content_reports r JOIN users ru ON ru.id = r.reporter_id
		LEFT JOIN users cu ON cu.id = r.closed_by WHERE `+where+` ORDER BY r.created_at DESC, r.id DESC LIMIT $1`, args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (reportItem, error) { return scanReport(r) })
	if err != nil {
		return nil, err
	}
	groups, err := a.groupReports(q.ctx, list)
	if err != nil {
		return nil, err
	}
	reasons := make([]map[string]string, 0)
	for _, r := range reports.Tops() {
		reasons = append(reasons, map[string]string{"key": r[0], "label": r[1]})
	}
	var open int64
	if err := a.DB.QueryRow(q.ctx, "SELECT count(*) FROM content_reports WHERE status='open'").Scan(&open); err != nil {
		return nil, err
	}
	return map[string]any{"groups": groups, "reasons": reasons, "open": open}, nil
}

func (a *API) groupReports(ctx context.Context, list []reportItem) ([]reportGroup, error) {
	groups := []reportGroup{}
	at := map[string]int{}
	var users, chats []int64
	for _, r := range list {
		i, ok := at[r.targetKey]
		if !ok {
			i = len(groups)
			at[r.targetKey] = i
			groups = append(groups, reportGroup{Key: r.targetKey, LatestAt: r.CreatedAt})
			if id, err := strconv.ParseInt(r.targetKey[1:], 10, 64); err == nil {
				if r.targetKey[0] == 'u' {
					users = append(users, id)
				} else {
					chats = append(chats, id)
				}
			}
		}
		if r.chatKind == "user" {
			users = append(users, r.chatID)
		} else {
			chats = append(chats, r.chatID)
		}
		groups[i].Reports = append(groups[i].Reports, r)
	}
	people, err := a.reportPeople(ctx, users)
	if err != nil {
		return nil, err
	}
	rooms, err := a.reportRooms(ctx, chats)
	if err != nil {
		return nil, err
	}
	for gi := range groups {
		g := &groups[gi]
		id, _ := strconv.ParseInt(g.Key[1:], 10, 64)
		if g.Key[0] == 'u' {
			g.Target = people[id]
		} else {
			g.Target = rooms[id]
		}
		g.Target.Key = g.Key
		reporters := map[int64]bool{}
		for ri := range g.Reports {
			r := &g.Reports[ri]
			if r.chatKind == "user" {
				p := people[r.chatID]
				r.Chat = reportChat{Kind: "user", ID: r.chatID, Title: p.Name}
			} else {
				c := rooms[r.chatID]
				r.Chat = reportChat{Kind: c.Kind, ID: r.chatID, Title: c.Name}
			}
			if r.Status != "open" {
				continue
			}
			g.Open++
			reporters[r.Reporter.ID] = true
			if !slices.Contains(g.Reasons, r.ReasonLabel) {
				g.Reasons = append(g.Reasons, r.ReasonLabel)
			}
			if len(r.MsgIDs) > 0 {
				g.CanDelete = true
			}
			if room := rooms[r.chatID]; r.chatKind == "channel" && g.Key[0] == 'u' && !room.Gone && room.creator != g.Target.ID {
				g.CanBan = true
			}
		}
		g.Reporters = len(reporters)
		person := g.Key[0] == 'u' && g.Target.Kind == "user" && !g.Target.IsAdmin
		g.CanFreeze = g.Open > 0 && person && g.Target.State == "active"
		g.CanBan = g.CanBan && person
	}
	return groups, nil
}

func (a *API) reportPeople(ctx context.Context, list []int64) (map[int64]reportTarget, error) {
	out := map[int64]reportTarget{}
	if len(list) == 0 {
		return out, nil
	}
	rows, err := a.DB.Query(ctx, `SELECT id, name, username, is_bot, is_system, account_state, is_suspended, role = 'admin', coalesce(photo_id, 0)
		FROM users WHERE id = ANY($1)`, list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t reportTarget
		var bot, system, suspended bool
		var photo int64
		if err := rows.Scan(&t.ID, &t.Name, &t.Username, &bot, &system, &t.State, &suspended, &t.IsAdmin, &photo); err != nil {
			return nil, err
		}
		t.Kind = "user"
		if bot || system {
			t.Kind = "bot"
		}
		if suspended {
			t.State = "suspended"
		}
		if photo != 0 {
			t.AvatarPhotoID = strconv.FormatInt(photo, 10)
		}
		t.PeerID = strconv.FormatInt(t.ID, 10)
		out[t.ID] = t
	}
	return out, rows.Err()
}

func (a *API) reportRooms(ctx context.Context, list []int64) (map[int64]reportTarget, error) {
	out := map[int64]reportTarget{}
	if len(list) == 0 {
		return out, nil
	}
	rows, err := a.DB.Query(ctx, "SELECT id, title, coalesce(username, ''), megagroup, deleted, coalesce(photo_id, 0), creator_id FROM channels WHERE id = ANY($1)", list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t reportTarget
		var group bool
		var photo int64
		if err := rows.Scan(&t.ID, &t.Name, &t.Username, &group, &t.Gone, &photo, &t.creator); err != nil {
			return nil, err
		}
		t.Kind = "channel"
		if group {
			t.Kind = "group"
		}
		if photo != 0 {
			t.AvatarPhotoID = strconv.FormatInt(photo, 10)
		}
		// Web A's channel ids: -(10^12 + id)
		t.PeerID = strconv.FormatInt(-(1_000_000_000_000 + t.ID), 10)
		out[t.ID] = t
	}
	return out, rows.Err()
}

// openGroup locks the open reports an action covers: those on the same
// target as id, limited to the ones the admin had on screen if the body
// lists them (a report filed meanwhile stays open for a look).
func (a *API) openGroup(q *req, tx pgx.Tx) (string, []reportItem, error) {
	id, err := q.pathID("id")
	if err != nil {
		return "", nil, fail(404, "Report not found.")
	}
	var key string
	err = tx.QueryRow(q.ctx, "SELECT target_key FROM content_reports WHERE id=$1", id).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, fail(404, "Report not found.")
	}
	if err != nil {
		return "", nil, err
	}
	var only []int64
	if raw, ok := q.body["reports"].([]any); ok {
		for _, v := range raw {
			if f, ok := v.(float64); ok {
				only = append(only, int64(f))
			}
		}
	}
	query := `SELECT ` + reportCols + ` FROM content_reports r JOIN users ru ON ru.id = r.reporter_id LEFT JOIN users cu ON cu.id = r.closed_by
		WHERE r.target_key=$1 AND r.status='open'`
	args := []any{key}
	if q.has("reports") {
		query += " AND r.id = ANY($2)"
		args = append(args, only)
	}
	rows, err := tx.Query(q.ctx, query+" ORDER BY r.id FOR UPDATE OF r", args...)
	if err != nil {
		return "", nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (reportItem, error) { return scanReport(r) })
	if err != nil {
		return "", nil, err
	}
	if len(list) == 0 {
		return "", nil, fail(409, "These reports were already closed.")
	}
	return key, list, nil
}

func reportIDs(list []reportItem) []int64 {
	out := make([]int64, 0, len(list))
	for _, r := range list {
		out = append(out, r.ID)
	}
	return out
}

func targetID(key string) int64 {
	id, _ := strconv.ParseInt(key[1:], 10, 64)
	return id
}

func closeReports(ctx context.Context, tx pgx.Tx, list []reportItem, status string, actions []string, actor int64) error {
	if actions == nil {
		actions = []string{}
	}
	_, err := tx.Exec(ctx, "UPDATE content_reports SET status=$2, actions=$3, closed_by=$4, closed_at=now() WHERE id = ANY($1) AND status='open'",
		reportIDs(list), status, actions, actor)
	return err
}

// note reads the optional note that goes in the audit entry (and to the
// account's @SpamBot reason when freezing).
func (q *req) note() (string, error) {
	if !q.has("note") || q.body["note"] == nil {
		return "", nil
	}
	s, ok := q.str("note")
	if !ok || utf8.RuneCountInString(s) > 500 {
		return "", fail(400, "Write a note of up to 500 characters.")
	}
	return strings.TrimSpace(s), nil
}

func (a *API) dismissReports(q *req) (any, error) {
	note, err := q.note()
	if err != nil {
		return nil, err
	}
	var n int
	err = a.tx(q.ctx, func(tx pgx.Tx) error {
		key, list, err := a.openGroup(q, tx)
		if err != nil {
			return err
		}
		n = len(list)
		if err := closeReports(q.ctx, tx, list, "dismissed", nil, q.me.ID); err != nil {
			return err
		}
		return audit(q.ctx, tx, q.me.ID, "reports_dismissed", targetID(key), map[string]any{"target": key, "reports": reportIDs(list), "note": note})
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "closed": n}, nil
}

// resolveReports takes {delete, freeze, ban: bool, note, reports: [ids]}:
// any of the three, in the order ban, delete, freeze. Each one done is in
// the audit log at once, so a failure halfway leaves a true record and the
// reports open for another go.
func (a *API) resolveReports(q *req) (any, error) {
	del, _ := q.boolean("delete")
	freeze, _ := q.boolean("freeze")
	ban, _ := q.boolean("ban")
	if !del && !freeze && !ban {
		return nil, fail(400, "Choose what to do: delete the messages, freeze the account or ban from the chat.")
	}
	note, err := q.note()
	if err != nil {
		return nil, err
	}
	if (del || ban) && a.Moderator == nil {
		return nil, fail(501, "This server can't do that from the panel.")
	}
	// look first, act outside the lock: the messenger's own code takes its
	// own locks
	var key string
	var list []reportItem
	err = a.tx(q.ctx, func(tx pgx.Tx) error {
		key, list, err = a.openGroup(q, tx)
		return err
	})
	if err != nil {
		return nil, err
	}
	target := targetID(key)
	user := key[0] == 'u'
	if (freeze || ban) && !user {
		return nil, fail(400, "Only an account can be frozen or banned. For a group or channel, delete its messages or the chat itself.")
	}
	if freeze || ban {
		t, found, err := a.loadMember(q.ctx, nil, target)
		switch {
		case err != nil:
			return nil, err
		case !found || t.IsSystem || t.isBot:
			return nil, fail(400, "Only a person's account can be frozen or banned here.")
		case t.Role == "admin" || t.ID == q.me.ID:
			return nil, fail(400, "You cannot restrict your own account or another administrator here.")
		}
	}
	var done []string
	if ban {
		var chats []int64
		for _, r := range list {
			if r.chatKind == "channel" && !slices.Contains(chats, r.chatID) {
				chats = append(chats, r.chatID)
			}
		}
		if len(chats) == 0 {
			return nil, fail(400, "These reports aren't from a group or channel, there is nothing to ban them from.")
		}
		var owned int
		if err := a.DB.QueryRow(q.ctx, "SELECT count(*) FROM channels WHERE id = ANY($1) AND creator_id = $2", chats, target).Scan(&owned); err != nil {
			return nil, err
		}
		if owned == len(chats) {
			return nil, errOwner
		}
		for _, chat := range chats {
			if err := a.Moderator.BanFromChannel(q.ctx, chat, target); err != nil {
				// their own chat among others: banned from the rest
				if errors.Is(err, reports.ErrOwner) {
					continue
				}
				return nil, err
			}
			if err := a.tx(q.ctx, func(tx pgx.Tx) error {
				return audit(q.ctx, tx, q.me.ID, "report_ban", target, map[string]any{"channel": chat, "reports": reportIDs(list), "note": note})
			}); err != nil {
				return nil, err
			}
		}
		done = append(done, "ban")
	}
	if del {
		deleted := 0
		for _, r := range list {
			if len(r.MsgIDs) == 0 {
				continue
			}
			n, err := a.Moderator.DeleteForEveryone(q.ctx, r.chatKind, r.chatID, r.MsgIDs, r.shared)
			if err != nil {
				return nil, err
			}
			deleted += n
			if err := a.tx(q.ctx, func(tx pgx.Tx) error {
				return audit(q.ctx, tx, q.me.ID, "report_delete_messages", target, map[string]any{
					"report": r.ID, "chatKind": r.chatKind, "chat": r.chatID, "messages": r.MsgIDs, "deleted": n, "note": note})
			}); err != nil {
				return nil, err
			}
		}
		done = append(done, "delete")
	}
	froze := false
	err = a.tx(q.ctx, func(tx pgx.Tx) error {
		if freeze {
			var state string
			if err := tx.QueryRow(q.ctx, "SELECT account_state FROM users WHERE id=$1 FOR UPDATE", target).Scan(&state); err != nil {
				return err
			}
			// already restricted some other way: that stands
			if state == "active" {
				reason := note
				if reason == "" {
					reason = "reported"
				}
				if err := a.setAccess(q.ctx, tx, q.me.ID, target, "frozen", reason, ""); err != nil {
					return err
				}
				froze = true
			}
			done = append(done, "freeze")
		}
		if err := closeReports(q.ctx, tx, list, "resolved", done, q.me.ID); err != nil {
			return err
		}
		return audit(q.ctx, tx, q.me.ID, "reports_resolved", target, map[string]any{"target": key, "reports": reportIDs(list), "actions": done, "note": note})
	})
	if err != nil {
		return nil, err
	}
	if froze {
		a.tellAccessChanged(target)
	}
	return map[string]any{"ok": true, "closed": len(list), "actions": done}, nil
}

// removeMessage is the Moderation page's Remove message: one message of the
// recent feed, by its feed id ("p<shared id>" or "c<chat>_<id>"), deleted for
// everyone the way Reports does it.
func (a *API) removeMessage(q *req) (any, error) {
	if a.Moderator == nil {
		return nil, fail(501, "This server can't do that from the panel.")
	}
	errGone := fail(404, "Message not found. It may already be deleted.")
	key := q.r.PathValue("id")
	var kind string
	var shared, chat, author int64
	var id int
	var err error
	switch {
	case strings.HasPrefix(key, "p"):
		kind = "user"
		if shared, err = strconv.ParseInt(key[1:], 10, 64); err != nil || shared < 1 {
			return nil, errGone
		}
		err = a.DB.QueryRow(q.ctx, "SELECT from_id FROM messages WHERE shared_id=$1 LIMIT 1", shared).Scan(&author)
	case strings.HasPrefix(key, "c"):
		kind = "channel"
		c, m, found := strings.Cut(key[1:], "_")
		if chat, err = strconv.ParseInt(c, 10, 64); err != nil || !found {
			return nil, errGone
		}
		if id, err = strconv.Atoi(m); err != nil || id < 1 {
			return nil, errGone
		}
		err = a.DB.QueryRow(q.ctx, "SELECT coalesce(from_id, 0) FROM channel_messages WHERE channel_id=$1 AND id=$2", chat, id).Scan(&author)
	default:
		return nil, errGone
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errGone
	}
	if err != nil {
		return nil, err
	}
	var n int
	target := shared // what the panel shows as "Message #"
	if kind == "user" {
		n, err = a.Moderator.DeleteForEveryone(q.ctx, kind, 0, nil, []int64{shared})
	} else {
		n, err = a.Moderator.DeleteForEveryone(q.ctx, kind, chat, []int{id}, nil)
		target = int64(id)
	}
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errGone
	}
	err = a.tx(q.ctx, func(tx pgx.Tx) error {
		return audit(q.ctx, tx, q.me.ID, "delete_message", target, map[string]any{"message": key, "chatKind": kind, "chat": chat, "author": author, "deleted": n})
	})
	if err != nil {
		return nil, err
	}
	return ok(), nil
}

// reportPhoto is a reported picture big enough to judge.
func (a *API) reportPhoto(q *req) ([]byte, string, error) {
	id, err := strconv.ParseInt(q.r.PathValue("id"), 10, 64)
	if err != nil {
		return nil, "", fail(404, "Photo not found.")
	}
	data, err := a.Media.PhotoFile(q.ctx, id, 800)
	if err != nil {
		return nil, "", err
	}
	mime := http.DetectContentType(data)
	if !panelImages[mime] {
		return nil, "", fail(404, "Photo not found.")
	}
	return data, mime, nil
}
