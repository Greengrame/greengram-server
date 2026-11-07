package messages

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// FindKnown is the "my results" half of contacts.search: people in the
// owner's book or chat list whose name, book name or username matches q.
// Strangers are only found by username (account.SearchUsernames).
func (s *Service) FindKnown(ctx context.Context, owner int64, q string, limit int) ([]int64, error) {
	q = strings.TrimPrefix(strings.TrimSpace(q), "@")
	if q == "" {
		return nil, nil
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q) + "%"
	rows, err := s.db.Query(ctx, `SELECT u.id FROM users u
		LEFT JOIN contacts c ON c.owner_id = $1 AND c.user_id = u.id
		LEFT JOIN dialogs d ON d.owner_id = $1 AND d.peer_id = u.id
		WHERE (c.user_id IS NOT NULL OR d.peer_id IS NOT NULL) AND u.id <> $1 AND NOT u.disabled
			AND (u.name ILIKE $2 OR (c.first_name || ' ' || c.last_name) ILIKE $2 OR (u.username_public AND u.username ILIKE $2))
		ORDER BY coalesce(d.top_date, 0) DESC LIMIT $3`, owner, like, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// Top peer categories this server can rank; the rest come back empty.
const (
	TopCorrespondents = "correspondents"
	TopBotsPM         = "bots_pm"
	TopGroups         = "groups"
)

// Telegram's rating: each message adds e^((date - now) / decay), so
// recent chats weigh more. decay is help.getConfig's rating_e_decay.
const ratingDecay = 2419200

// TopPeer is a ranked peer: a user id, or -channel id for groups.
type TopPeer struct {
	Peer   int64
	Rating float64
}

// TopPeers ranks the owner's chats in one category by what they sent
// there in the last 90 days, minus what came before a reset.
func (s *Service) TopPeers(ctx context.Context, owner int64, category string, offset, limit int) ([]TopPeer, error) {
	limit = clampLimit(limit, 100)
	now := s.unix()
	since := now - 90*86400
	var q string
	switch category {
	case TopCorrespondents, TopBotsPM:
		q = `SELECT m.peer_id, sum(exp((m.date - $2)::float8 / ` + itoa(ratingDecay) + `)) FROM messages m JOIN users u ON u.id = m.peer_id
			WHERE m.owner_id = $1 AND m.out AND m.peer_id <> $1 AND m.date > $3 AND NOT u.disabled AND NOT u.is_system
				AND u.is_bot = $6
				AND m.date >= coalesce((SELECT r.since FROM top_peer_resets r WHERE r.owner_id = $1 AND r.category = $7 AND r.peer_id = m.peer_id), 0)
			GROUP BY m.peer_id ORDER BY 2 DESC OFFSET $4 LIMIT $5`
	case TopGroups:
		q = `SELECT -m.channel_id, sum(exp((m.date - $2)::float8 / ` + itoa(ratingDecay) + `)) FROM channel_messages m
			JOIN channels c ON c.id = m.channel_id AND c.megagroup AND NOT c.deleted
			JOIN channel_members cm ON cm.channel_id = m.channel_id AND cm.user_id = $1 AND cm.status IN ('creator', 'admin', 'member', 'restricted')
			WHERE m.from_id = $1 AND m.date > $3 AND $6::bool IS NOT NULL
				AND m.date >= coalesce((SELECT r.since FROM top_peer_resets r WHERE r.owner_id = $1 AND r.category = $7 AND r.peer_id = -m.channel_id), 0)
			GROUP BY m.channel_id ORDER BY 2 DESC OFFSET $4 LIMIT $5`
	default:
		return nil, nil
	}
	rows, err := s.db.Query(ctx, q, owner, now, since, max(offset, 0), limit, category == TopBotsPM, category)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TopPeer, error) {
		var p TopPeer
		err := r.Scan(&p.Peer, &p.Rating)
		return p, err
	})
}

// ResetTopPeer drops what peer earned in category so far.
func (s *Service) ResetTopPeer(ctx context.Context, owner int64, category string, peer int64) error {
	_, err := s.db.Exec(ctx, `INSERT INTO top_peer_resets(owner_id, category, peer_id, since) VALUES ($1, $2, $3, $4)
		ON CONFLICT (owner_id, category, peer_id) DO UPDATE SET since = excluded.since`, owner, category, peer, s.unix()+1)
	return err
}

// Stranger is what the "add contact / block" bar over a private chat
// depends on.
type Stranger struct {
	Contact  bool // owner has peer in their book
	Incoming bool // peer wrote to owner
	Replied  bool // owner wrote back
	Hidden   bool // owner closed the bar
}

func (s *Service) StrangerState(ctx context.Context, owner, peer int64) (Stranger, error) {
	var st Stranger
	err := s.db.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM contacts WHERE owner_id=$1 AND user_id=$2),
		EXISTS(SELECT 1 FROM messages WHERE owner_id=$1 AND peer_id=$2 AND NOT out),
		EXISTS(SELECT 1 FROM messages WHERE owner_id=$1 AND peer_id=$2 AND out),
		EXISTS(SELECT 1 FROM peer_settings_hidden WHERE owner_id=$1 AND peer_id=$2)`, owner, peer).
		Scan(&st.Contact, &st.Incoming, &st.Replied, &st.Hidden)
	return st, err
}

func (s *Service) HidePeerSettings(ctx context.Context, owner, peer int64) error {
	_, err := s.db.Exec(ctx, "INSERT INTO peer_settings_hidden(owner_id, peer_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", owner, peer)
	return err
}
