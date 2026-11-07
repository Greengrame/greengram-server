package channels

import (
	"context"
	"errors"
	"sort"

	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
)

// Telegram's chat_read_mark_size_threshold and chat_read_mark_expire_period.
const (
	SeenByMaxMembers = 100
	SeenByExpire     = 7 * 24 * 3600
)

// noteRead remembers a member's new read mark, for "seen by".
func (s *Service) noteRead(ctx context.Context, tx pgx.Tx, c Channel, user int64, maxID int) error {
	if !c.Megagroup || c.Count > SeenByMaxMembers {
		return nil
	}
	now := s.unix()
	if _, err := tx.Exec(ctx, "DELETE FROM channel_read_marks WHERE channel_id=$1 AND user_id=$2 AND date < $3", c.ID, user, now-SeenByExpire); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO channel_read_marks(channel_id, user_id, max_id, date) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, c.ID, user, maxID, now)
	return err
}

// seenUpTo is the newest message up to maxID someone else wrote: the author
// reading their own message doesn't make it seen, and neither does the owner
// reading "X joined", which X never had to read anything for.
func seenUpTo(ctx context.Context, tx pgx.Tx, channel, user int64, maxID int) (int, error) {
	var seen int
	err := tx.QueryRow(ctx, "SELECT coalesce(max(id), 0) FROM channel_messages WHERE channel_id=$1 AND id <= $2 AND from_id <> $3 AND action IS NULL", channel, maxID, user).Scan(&seen)
	return seen, err
}

// ReadDate is when someone read a message.
type ReadDate struct {
	User int64
	Date int
}

// ReadParticipants says who has seen user's message in a group, newest
// reader first.
func (s *Service) ReadParticipants(ctx context.Context, user, channel int64, id int) ([]ReadDate, error) {
	c, ok, err := s.Get(ctx, channel)
	if err != nil {
		return nil, err
	}
	if !ok || !c.Megagroup {
		return nil, errInvalid
	}
	if c.Count > SeenByMaxMembers {
		return nil, tgerr.New(400, "CHAT_TOO_BIG")
	}
	var from int64
	var date int
	err = s.db.QueryRow(ctx, "SELECT from_id, date FROM channel_messages WHERE channel_id=$1 AND id=$2", channel, id).Scan(&from, &date)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tgerr.New(400, "MSG_ID_INVALID")
	}
	if err != nil {
		return nil, err
	}
	if from != user {
		return nil, tgerr.New(400, "MSG_ID_INVALID")
	}
	if date < s.unix()-SeenByExpire {
		return nil, tgerr.New(400, "MSG_TOO_OLD")
	}
	rows, err := s.db.Query(ctx, `SELECT r.user_id, min(r.date) FROM channel_read_marks r
		JOIN channel_members m ON m.channel_id = r.channel_id AND m.user_id = r.user_id
		WHERE r.channel_id=$1 AND r.max_id >= $2 AND r.user_id <> $3
		GROUP BY r.user_id ORDER BY 2 DESC, 1 LIMIT $4`, channel, id, user, SeenByMaxMembers)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ReadDate, error) {
		var d ReadDate
		err := r.Scan(&d.User, &d.Date)
		return d, err
	})
}

// Views counts, for each id, the members whose read mark has reached it. There
// is no separate view counter: a read mark is the same fact, and takes one query
// however many posts are asked about.
func (s *Service) Views(ctx context.Context, channel int64, list []int) (map[int]int, error) {
	out := make(map[int]int, len(list))
	if len(list) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, "SELECT read_inbox_max FROM channel_members WHERE channel_id=$1", channel)
	if err != nil {
		return nil, err
	}
	marks, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return nil, err
	}
	sort.Ints(marks)
	for _, id := range list {
		out[id] = len(marks) - sort.SearchInts(marks, id)
	}
	return out, nil
}
