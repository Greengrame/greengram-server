package admin

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// The panel's Bots view: the list in GET /api/admin, and admin-bots.js's
// GET/PATCH /api/admin/bots/{id}.

type adminBot struct {
	member
	OwnerID     *int64 `json:"ownerId"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	CanEdit     bool   `json:"canEdit"`
}

// a custom bot BotFather deleted has lost its token; the panel doesn't
// show those, like the web's account_state='deleted' bots
const botCols = memberCols + `, b.owner_id, b.kind, b.description, b.kind = 'custom' AND b.token_hash IS NULL`

// extraRow scans destinations after memberCols.
type extraRow struct {
	row   pgx.Row
	extra []any
}

func (r extraRow) Scan(dest ...any) error { return r.row.Scan(append(dest, r.extra...)...) }

func scanBot(row pgx.Row) (adminBot, bool, error) {
	var b adminBot
	var gone bool
	var err error
	b.member, err = scanMember(extraRow{row, []any{&b.OwnerID, &b.Kind, &b.Description, &gone}})
	if gone {
		b.AccountState = "deleted"
	}
	b.CanEdit = b.Kind == "custom" && b.AccountState != "deleted"
	return b, gone, err
}

func (a *API) botList(ctx context.Context) ([]adminBot, error) {
	rows, err := a.DB.Query(ctx, "SELECT "+botCols+" FROM users u JOIN bots b ON b.user_id = u.id WHERE u.account_state <> 'deleted' ORDER BY u.id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []adminBot{}
	for rows.Next() {
		b, gone, err := scanBot(rows)
		if err != nil {
			return nil, err
		}
		if !gone {
			out = append(out, b)
		}
	}
	return out, rows.Err()
}

func (a *API) loadBot(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id int64, lock string) (adminBot, error) {
	b, gone, err := scanBot(q.QueryRow(ctx, "SELECT "+botCols+" FROM users u JOIN bots b ON b.user_id = u.id WHERE u.id=$1"+lock, id))
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (gone || b.AccountState == "deleted") {
		return b, fail(404, "Bot not found.")
	}
	return b, err
}

func (a *API) botDetail(q *req) (any, error) {
	id, err := q.pathID("id")
	if err != nil {
		return nil, err
	}
	return a.loadBot(q.ctx, a.DB, id, "")
}

var botUsernameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,28}bot$`)

// editBot is editAdminBot(): profile fields and the enabled switch.
func (a *API) editBot(q *req) (any, error) {
	id, err := q.pathID("id")
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{"name": true, "username": true, "bio": true, "description": true, "disabled": true}
	var fields []string
	for k := range q.body {
		if !allowed[k] {
			return nil, fail(400, "Choose bot profile fields or its enabled status.")
		}
		fields = append(fields, k)
	}
	if len(fields) == 0 {
		return nil, fail(400, "Choose bot profile fields or its enabled status.")
	}
	err = a.tx(q.ctx, func(tx pgx.Tx) error {
		if q.has("username") {
			// checked and taken under one lock, like bots.Create
			if _, err := tx.Exec(q.ctx, "LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE"); err != nil {
				return err
			}
		}
		b, err := a.loadBot(q.ctx, tx, id, " FOR UPDATE OF u, b")
		if err != nil {
			return err
		}
		if !b.CanEdit {
			return fail(409, "Built-in service profiles are managed by the instance.")
		}
		name, username, bio, desc := b.Name, b.Username, b.Bio, b.Description
		okName, okBio, okDesc := true, true, true
		if q.has("name") {
			name, okName = q.str("name")
			name = strings.TrimSpace(name)
		}
		if q.has("username") {
			s, _ := q.str("username")
			username = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "@"))
		}
		if q.has("bio") {
			bio, okBio = q.str("bio")
		}
		if q.has("description") {
			desc, okDesc = q.str("description")
		}
		if !okName || !okBio || !okDesc || name == "" || utf8.RuneCountInString(name) > 80 ||
			utf8.RuneCountInString(bio) > 500 || utf8.RuneCountInString(desc) > 500 {
			return fail(400, "Use a name of up to 80 characters and profile text of up to 500 characters.")
		}
		disabled := b.Disabled
		if q.has("disabled") {
			var isBool bool
			if disabled, isBool = q.boolean("disabled"); !isBool {
				return fail(400, "Choose an enabled or disabled status.")
			}
		}
		if username != strings.ToLower(b.Username) {
			if reserved[username] {
				return fail(409, "This username is reserved for a built-in bot.")
			}
			if !botUsernameRe.MatchString(username) {
				return fail(400, "USERNAME_INVALID")
			}
			var taken bool
			if err := tx.QueryRow(q.ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(username)=$1 AND id<>$2)
				OR EXISTS(SELECT 1 FROM channels WHERE lower(username)=$1 AND NOT deleted)
				OR EXISTS(SELECT 1 FROM fragment_items WHERE kind='username' AND lower(title)='@'||$1)`, username, id).Scan(&taken); err != nil {
				return err
			}
			if taken {
				return fail(409, "USERNAME_OCCUPIED")
			}
		}
		if q.has("disabled") && !disabled {
			var ownerOK bool
			if err := tx.QueryRow(q.ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND NOT disabled
				AND account_state <> 'deleted' AND NOT is_system)`, b.OwnerID).Scan(&ownerOK); err != nil {
				return err
			}
			if !ownerOK {
				return fail(409, "Restore the owner account before enabling this bot.")
			}
		}
		if _, err := tx.Exec(q.ctx, "UPDATE users SET name=$2, username=$3, bio=$4 WHERE id=$1", id, name, username, bio); err != nil {
			return err
		}
		if q.has("disabled") {
			state := "active"
			if disabled {
				state = "locked"
			}
			if _, err := tx.Exec(q.ctx, "UPDATE users SET disabled=$2, account_state=$3, is_suspended=false WHERE id=$1", id, disabled, state); err != nil {
				return err
			}
		}
		// info_version moves so the apps refetch the bot's info
		if _, err := tx.Exec(q.ctx, "UPDATE bots SET description=$2, info_version = info_version + 1 WHERE user_id=$1", id, desc); err != nil {
			return err
		}
		return audit(q.ctx, tx, q.me.ID, "admin_bot_updated", id, map[string]any{"fields": fields})
	})
	if err != nil {
		return nil, err
	}
	return a.loadBot(q.ctx, a.DB, id, "")
}
