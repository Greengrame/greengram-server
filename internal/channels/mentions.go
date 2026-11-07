package channels

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
)

// mention records who a new group message mentions: the users named
// (if they're in the chat) and the author of the message it replies to.
// The sender never mentions themselves.
func mention(ctx context.Context, tx pgx.Tx, c Channel, m Msg, users []int64) ([]int64, error) {
	if !c.Megagroup {
		return nil, nil
	}
	if m.ReplyTo != 0 {
		var author int64
		var post bool
		err := tx.QueryRow(ctx, "SELECT from_id, post FROM channel_messages WHERE channel_id=$1 AND id=$2", c.ID, m.ReplyTo).Scan(&author, &post)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if author != 0 && !post {
			users = append(users, author)
		}
	}
	if len(users) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `INSERT INTO channel_mentions(channel_id, msg_id, user_id)
		SELECT $1, $2, user_id FROM channel_members WHERE channel_id=$1 AND user_id = ANY($3) AND user_id <> $4 AND status IN `+inChatSQL+`
		ON CONFLICT DO NOTHING RETURNING user_id`, c.ID, m.ID, users, m.From)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// MarkMentioned flags a new message as mentioning the viewer, on a copy,
// for the members it names.
func MarkMentioned(u tg.UpdateClass, viewer int64, mentions []int64) tg.UpdateClass {
	nm, ok := u.(*tg.UpdateNewChannelMessage)
	if !ok {
		return u
	}
	for _, id := range mentions {
		if id != viewer {
			continue
		}
		if m, ok := nm.Message.(*tg.Message); ok {
			cm := *m
			cm.Mentioned, cm.MediaUnread = true, true
			c := *nm
			c.Message = &cm
			return &c
		}
	}
	return u
}

// Mentions are the messages among list that mention viewer: id -> unread.
func (s *Service) Mentions(ctx context.Context, channel, viewer int64, list []int) (map[int]bool, error) {
	out := map[int]bool{}
	if len(list) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, "SELECT msg_id, unread FROM channel_mentions WHERE channel_id=$1 AND user_id=$2 AND msg_id = ANY($3)", channel, viewer, list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var unread bool
		if err := rows.Scan(&id, &unread); err != nil {
			return nil, err
		}
		out[id] = unread
	}
	return out, rows.Err()
}

// ReadMentions marks the user's mentions seen: the listed messages, or
// all of them for nil. It returns the ones that were unread.
func (s *Service) ReadMentions(ctx context.Context, user, channel int64, list []int) ([]int, error) {
	q := "UPDATE channel_mentions SET unread=false WHERE channel_id=$1 AND user_id=$2 AND unread RETURNING msg_id"
	args := []any{channel, user}
	if list != nil {
		q = "UPDATE channel_mentions SET unread=false WHERE channel_id=$1 AND user_id=$2 AND unread AND msg_id = ANY($3) RETURNING msg_id"
		args = append(args, list)
	}
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int])
}
