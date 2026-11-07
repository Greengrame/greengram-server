package channels

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/messages"
)

func scanReactions(rows pgx.Rows) (map[int]messages.MsgReactions, error) {
	defer rows.Close()
	out := map[int]messages.MsgReactions{}
	for rows.Next() {
		var id int
		var author int64
		var r messages.Reaction
		if err := rows.Scan(&id, &author, &r.User, &r.Key, &r.Pos, &r.Big, &r.Unread, &r.Date); err != nil {
			return nil, err
		}
		m := out[id]
		m.Author = author
		m.List = append(m.List, r)
		out[id] = m
	}
	return out, rows.Err()
}

const reactionCols = "r.msg_id, r.author_id, r.user_id, r.reaction, r.position, r.big, r.unread, r.date"

// Reactions are the reaction sets of channel messages, by id.
func (s *Service) Reactions(ctx context.Context, channel int64, list []int) (map[int]messages.MsgReactions, error) {
	if len(list) == 0 {
		return map[int]messages.MsgReactions{}, nil
	}
	rows, err := s.db.Query(ctx, "SELECT "+reactionCols+" FROM channel_reactions r WHERE r.channel_id=$1 AND r.msg_id = ANY($2) ORDER BY r.date, r.user_id, r.position",
		channel, list)
	if err != nil {
		return nil, err
	}
	sets, err := scanReactions(rows)
	if err != nil {
		return nil, err
	}
	rows, err = s.db.Query(ctx, "SELECT msg_id, user_id, count, anonymous, date FROM channel_paid_reactions WHERE channel_id=$1 AND msg_id = ANY($2) ORDER BY date, user_id",
		channel, list)
	if err != nil {
		return nil, err
	}
	if err := messages.ScanPaid(rows, sets); err != nil {
		return nil, err
	}
	return sets, nil
}

// PaidTarget checks user may give Stars for a channel message and says who
// wrote it. A post's Stars go to the admin who posted it: nothing here holds
// a balance for a channel.
func (s *Service) PaidTarget(ctx context.Context, user, channel int64, id int) (author int64, err error) {
	var service bool
	var status string
	err = s.db.QueryRow(ctx, `SELECT x.from_id, x.action IS NOT NULL, m.status FROM channel_messages x
		JOIN channel_members m ON m.channel_id = x.channel_id AND m.user_id = $1
		WHERE x.channel_id=$2 AND x.id=$3`, user, channel, id).Scan(&author, &service, &status)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (service || !InChat(status)) {
		return 0, tgerr.New(400, "MESSAGE_ID_INVALID")
	}
	return author, err
}

// AddPaid adds to user's star reaction total, in the transaction moving the
// Stars.
func (s *Service) AddPaid(ctx context.Context, tx pgx.Tx, channel int64, id int, user int64, count int, anon bool) error {
	_, err := tx.Exec(ctx, `INSERT INTO channel_paid_reactions(channel_id, msg_id, user_id, count, anonymous, date) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (channel_id, msg_id, user_id) DO UPDATE SET count = channel_paid_reactions.count + excluded.count, anonymous = excluded.anonymous, date = excluded.date`,
		channel, id, user, count, anon, s.unix())
	return err
}

// SetPaidPrivacy shows or hides who gave the Stars.
func (s *Service) SetPaidPrivacy(ctx context.Context, channel int64, id int, user int64, anon bool) error {
	_, err := s.db.Exec(ctx, "UPDATE channel_paid_reactions SET anonymous=$4 WHERE channel_id=$1 AND msg_id=$2 AND user_id=$3", channel, id, user, anon)
	return err
}

// Reacted is a channel message's reaction set after a change.
type Reacted struct {
	messages.MsgReactions
	Channel Channel
}

// React replaces user's reactions on a channel message; no keys takes
// them back. Anyone in the chat may react, subscribers of a broadcast
// channel too.
func (s *Service) React(ctx context.Context, user, channel int64, id int, keys []string, big bool) (Reacted, error) {
	var out Reacted
	err := s.tx(ctx, func(tx pgx.Tx) error {
		c, err := scanChannel(tx.QueryRow(ctx, "SELECT "+chanCols+" FROM channels c WHERE c.id=$1", channel))
		if errors.Is(err, pgx.ErrNoRows) || err == nil && c.Deleted {
			return errInvalid
		}
		if err != nil {
			return err
		}
		out.Channel = c
		m, err := member(ctx, tx, channel, user)
		if err != nil {
			return err
		}
		if !InChat(m.Status) {
			return errPrivate
		}
		// the row lock keeps two changes by the same user in order
		var post, service bool
		err = tx.QueryRow(ctx, "SELECT from_id, post, action IS NOT NULL FROM channel_messages WHERE channel_id=$1 AND id=$2 FOR UPDATE",
			channel, id).Scan(&out.Author, &post, &service)
		if errors.Is(err, pgx.ErrNoRows) || service {
			return tgerr.New(400, "MESSAGE_ID_INVALID")
		}
		if err != nil {
			return err
		}
		// a post speaks for the channel; nobody gets told about its reactions
		if post {
			out.Author = 0
		}
		if _, err := tx.Exec(ctx, "DELETE FROM channel_reactions WHERE channel_id=$1 AND msg_id=$2 AND user_id=$3", channel, id, user); err != nil {
			return err
		}
		now := s.unix()
		for i, k := range keys {
			_, err := tx.Exec(ctx, `INSERT INTO channel_reactions(channel_id, msg_id, user_id, reaction, position, big, author_id, unread, date)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, channel, id, user, k, i+1, big, out.Author, out.Author != 0 && out.Author != user, now)
			if err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, "SELECT "+reactionCols+" FROM channel_reactions r WHERE r.channel_id=$1 AND r.msg_id=$2 ORDER BY r.date, r.user_id, r.position", channel, id)
		if err != nil {
			return err
		}
		all, err := scanReactions(rows)
		if err != nil {
			return err
		}
		out.List = all[id].List
		rows, err = tx.Query(ctx, "SELECT msg_id, user_id, count, anonymous, date FROM channel_paid_reactions WHERE channel_id=$1 AND msg_id=$2 ORDER BY date, user_id", channel, id)
		if err != nil {
			return err
		}
		if err := messages.ScanPaid(rows, all); err != nil {
			return err
		}
		out.Paid = all[id].Paid
		return nil
	})
	return out, err
}

// ReadReactions marks the reactions to user's messages in a channel seen.
func (s *Service) ReadReactions(ctx context.Context, user, channel int64) error {
	_, err := s.db.Exec(ctx, "UPDATE channel_reactions SET unread = false WHERE channel_id=$1 AND author_id=$2 AND unread", channel, user)
	return err
}

// ReactionList is who reacted to a message, newest first, optionally with
// one reaction only; offset counts rows already shown.
func (s *Service) ReactionList(ctx context.Context, channel int64, id int, key string, offset, limit int) ([]messages.Reaction, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	where := "r.channel_id=$1 AND r.msg_id=$2"
	args := []any{channel, id}
	if key != "" {
		args = append(args, key)
		where += " AND r.reaction=$3"
	}
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM channel_reactions r WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(ctx, "SELECT "+reactionCols+" FROM channel_reactions r WHERE "+where+
		" ORDER BY r.date DESC, r.user_id, r.position OFFSET $"+itoa(len(args)+1)+" LIMIT $"+itoa(len(args)+2), append(args, offset, limit)...)
	if err != nil {
		return nil, 0, err
	}
	got, err := scanReactions(rows)
	return got[id].List, total, err
}
