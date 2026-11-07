package bots

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/ids"
)

// builtin-bots.json is greengram-web's resources/builtin-bots.json.
//
//go:embed builtin-bots.json
var builtinJSON []byte

type builtin struct {
	Username    string `json:"username"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Bio         string `json:"bio"`
	Description string `json:"description"`
	ButtonText  string `json:"buttonText"`
}

// AppPaths is where each built-in bot's mini app is served. A bot without
// an entry has no mini app and keeps the commands menu.
var AppPaths = map[string]string{BotFather: "/botfather", Stickers: "/stickers", Wallet: "/wallet", Fragment: "/fragment", BugsBot: "/bugs"}

// EnsureBuiltins creates BotFather, Stickers, Wallet, Fragment, SpamBot and
// BugsBot, or
// refreshes their texts. base is the site URL their mini apps live on.
// A person already holding one of the names keeps it; the bot is skipped.
func (s *Service) EnsureBuiltins(ctx context.Context, base string) error {
	var list []builtin
	if err := json.Unmarshal(builtinJSON, &list); err != nil {
		return err
	}
	base = strings.TrimRight(base, "/")
	for _, d := range list {
		var menu []byte
		var appURL *string
		if path, ok := AppPaths[d.Kind]; ok {
			url := base + path
			menu, _ = json.Marshal(MenuButton{Text: d.ButtonText, URL: url})
			appURL = &url
		}
		err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			var id int64
			var kind *string
			err := tx.QueryRow(ctx, "SELECT u.id, b.kind FROM users u LEFT JOIN bots b ON b.user_id = u.id WHERE lower(u.username)=$1", d.Username).Scan(&id, &kind)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				if err := tx.QueryRow(ctx, "INSERT INTO users(username, name, password, is_bot, bio) VALUES ($1, $2, '', true, $3) RETURNING id",
					d.Username, d.Name, d.Bio).Scan(&id); err != nil {
					return err
				}
				_, err = tx.Exec(ctx, "INSERT INTO bots(user_id, kind, description, menu_button, main_app_url, webapp_key) VALUES ($1, $2, $3, $4, $5, $6)",
					id, d.Kind, d.Description, menu, appURL, randomKey())
				return err
			case err != nil:
				return err
			case kind == nil || *kind != d.Kind:
				slog.Warn("bots: built-in username belongs to an account", "username", d.Username)
				return nil
			}
			if _, err := tx.Exec(ctx, "UPDATE users SET bio=$2 WHERE id=$1", id, d.Bio); err != nil {
				return err
			}
			// the site URL may have moved; texts follow the JSON
			_, err = tx.Exec(ctx, `UPDATE bots SET description=$2, menu_button=$3, main_app_url=$4, webapp_key=coalesce(webapp_key, $5),
				info_version = info_version + CASE WHEN description IS DISTINCT FROM $2 OR menu_button IS DISTINCT FROM $3 THEN 1 ELSE 0 END
				WHERE user_id=$1`, id, d.Description, menu, appURL, randomKey())
			return err
		})
		if err != nil {
			return fmt.Errorf("built-in bot %s: %w", d.Username, err)
		}
	}
	return nil
}

// System account kinds, named as in greengram-web.
const (
	Announcements   = "announcements"
	PremiumPayments = "premium_payments"
	StarsPayments   = "stars_payments"
)

var systemAccounts = []struct{ kind, username, name string }{
	{Announcements, "announcements", "Greengram"},
	{PremiumPayments, "instance_premium_payments", "Premium"},
	{StarsPayments, "instance_stars_payments", "Stars"},
}

// EnsureSystem creates the system accounts and returns them by kind.
// siteName names the announcements account (greengram-web: settings
// siteName); "" keeps whatever it has.
func (s *Service) EnsureSystem(ctx context.Context, siteName string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, a := range systemAccounts {
		var id int64
		err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			err := tx.QueryRow(ctx, "SELECT user_id FROM system_accounts WHERE kind=$1", a.kind).Scan(&id)
			if err == nil || !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			// an import may have brought the account without the row
			err = tx.QueryRow(ctx, "SELECT id FROM users WHERE lower(username)=$1 AND is_system", a.username).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				var taken bool
				if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE lower(username)=$1)", a.username).Scan(&taken); err != nil {
					return err
				}
				if taken {
					return fmt.Errorf("the reserved @%s username belongs to an account", a.username)
				}
				err = tx.QueryRow(ctx, "INSERT INTO users(username, name, password, is_system) VALUES ($1, $2, '', true) RETURNING id", a.username, a.name).Scan(&id)
			}
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, "INSERT INTO system_accounts(kind, user_id) VALUES ($1, $2)", a.kind, id)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("system account %s: %w", a.kind, err)
		}
		out[a.kind] = id
	}
	if siteName != "" {
		if _, err := s.db.Exec(ctx, "UPDATE users SET name=$2 WHERE id=$1", out[Announcements], siteName); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Audiences an announcement can go to (the panel's four).
var Audiences = []string{"all", "active", "premium", "admins"}

// Recipients are the people an announcement for audience reaches: no
// bots, no system accounts, nobody disabled, frozen, banned or deleted.
func (s *Service) Recipients(ctx context.Context, audience string) ([]int64, error) {
	q := "SELECT id FROM users WHERE NOT is_bot AND NOT is_system AND NOT disabled AND account_state='active'"
	switch audience {
	case "all":
	case "admins":
		q += " AND role='admin'"
	case "active":
		q += " AND id IN (SELECT user_id FROM sessions WHERE last_active > now() - interval '7 days')"
	case "premium":
		q += " AND premium_until > extract(epoch FROM now()) AND premium_starts_at <= extract(epoch FROM now())"
	default:
		return nil, fmt.Errorf("unknown audience %q", audience)
	}
	rows, err := s.db.Query(ctx, q+" ORDER BY id")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

type Broadcast struct {
	ID         int64  `json:"id"`
	ActorID    int64  `json:"actorId"`
	Actor      string `json:"actor"` // name, as the panel shows it
	Audience   string `json:"audience"`
	Text       string `json:"text"`
	Recipients int    `json:"recipients"`
	Sent       int    `json:"sent"`
	CreatedAt  int64  `json:"createdAt"`
	Done       bool   `json:"done"`
}

// StartBroadcast records the run along with the admin audit entry
// greengram-web's sendBroadcast wrote.
func (s *Service) StartBroadcast(ctx context.Context, actor int64, audience, text string, recipients int) (int64, error) {
	short := text
	if r := []rune(short); len(r) > 500 {
		short = string(r[:500])
	}
	details, err := json.Marshal(map[string]any{"audience": audience, "recipients": recipients, "text": short})
	if err != nil {
		return 0, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "INSERT INTO broadcasts(actor_id, audience, text, recipients) VALUES ($1, $2, $3, $4) RETURNING id",
			actor, audience, text, recipients).Scan(&id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO audit_log(actor_id, action, target_id, details) VALUES ($1, 'broadcast', $2, $3)",
			actor, ids.Announcements(), details)
		return err
	})
	return id, err
}

func (s *Service) BroadcastProgress(ctx context.Context, id int64, sent int, done bool) error {
	_, err := s.db.Exec(ctx, "UPDATE broadcasts SET sent=$2, finished_at = CASE WHEN $3 THEN now() END WHERE id=$1", id, sent, done)
	return err
}

// Broadcasts lists the newest announcements first.
func (s *Service) Broadcasts(ctx context.Context, limit int) ([]Broadcast, error) {
	rows, err := s.db.Query(ctx, `SELECT b.id, coalesce(b.actor_id, 0), coalesce(u.name, ''), b.audience, b.text, b.recipients, b.sent,
		extract(epoch FROM b.created_at)::bigint, b.finished_at IS NOT NULL
		FROM broadcasts b LEFT JOIN users u ON u.id=b.actor_id ORDER BY b.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Broadcast
	for rows.Next() {
		var b Broadcast
		if err := rows.Scan(&b.ID, &b.ActorID, &b.Actor, &b.Audience, &b.Text, &b.Recipients, &b.Sent, &b.CreatedAt, &b.Done); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
