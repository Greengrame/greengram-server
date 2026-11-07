// Package legacy answers for greengram-web's ids after the import. An old
// message link (/m/<id>) names one global message id; that message now
// lives in a channel, or as a copy in every participant's own box.
package legacy

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Greengrame/greengram-server/internal/ids"
)

type Links struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) *Links { return &Links{db: db} }

// Link is where an old message is for one viewer, in client terms: PeerID
// is a user id or -100<channel id> (what the web's links used too), and
// MessageID is the id in that viewer's box or in the channel.
type Link struct {
	PeerID    string `json:"peerId"`
	MessageID int    `json:"messageId"`
}

// Resolve finds old message id for viewer. ok=false when it was never
// imported, is gone, or viewer can't read it; callers say "not found" for
// all three.
func (l *Links) Resolve(ctx context.Context, viewer, old int64) (Link, bool, error) {
	var peer int64
	var id int
	err := l.db.QueryRow(ctx, `SELECT m.peer_id, m.id FROM import_box_messages b
		JOIN messages m ON m.owner_id = b.owner_id AND m.id = b.id
		WHERE b.old_id=$1 AND b.owner_id=$2`, old, viewer).Scan(&peer, &id)
	if err == nil {
		return Link{PeerID: strconv.FormatInt(ids.Client(peer), 10), MessageID: id}, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Link{}, false, err
	}
	var channel int64
	err = l.db.QueryRow(ctx, `SELECT x.channel_id, x.id FROM import_channel_messages x
		JOIN channels c ON c.id = x.channel_id AND NOT c.deleted
		JOIN channel_messages m ON m.channel_id = x.channel_id AND m.id = x.id
		LEFT JOIN channel_members cm ON cm.channel_id = c.id AND cm.user_id = $2
		WHERE x.old_id = $1 AND coalesce(cm.status, 'left') <> 'kicked'
			AND (cm.status IN ('creator', 'admin', 'member', 'restricted') OR c.username IS NOT NULL)`, old, viewer).Scan(&channel, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Link{}, false, nil
	}
	if err != nil {
		return Link{}, false, err
	}
	return Link{PeerID: strconv.FormatInt(-1_000_000_000_000-channel, 10), MessageID: id}, true, nil
}
