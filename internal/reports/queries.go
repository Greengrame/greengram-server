package reports

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// What the report handlers look up around a report.

// HasIncoming is whether owner's private chat with peer has a message from
// peer: a report on that chat then has to point at messages.
func (s *Service) HasIncoming(ctx context.Context, owner, peer int64) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM messages WHERE owner_id=$1 AND peer_id=$2 AND NOT out AND action IS NULL)", owner, peer).Scan(&ok)
	return ok, err
}

// HasOthers is the same for a group: anything someone else wrote.
func (s *Service) HasOthers(ctx context.Context, channel, user int64) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM channel_messages WHERE channel_id=$1 AND from_id<>$2 AND action IS NULL)", channel, user).Scan(&ok)
	return ok, err
}

// RecentFrom is the newest messages peer sent to owner, newest first.
func (s *Service) RecentFrom(ctx context.Context, owner, peer int64, limit int) ([]int, error) {
	rows, err := s.db.Query(ctx, "SELECT id FROM messages WHERE owner_id=$1 AND peer_id=$2 AND NOT out AND action IS NULL ORDER BY id DESC LIMIT $3", owner, peer, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int])
}

// RecentInChannel is the newest messages in a chat not from user.
func (s *Service) RecentInChannel(ctx context.Context, channel, user int64, limit int) ([]int, error) {
	rows, err := s.db.Query(ctx, "SELECT id FROM channel_messages WHERE channel_id=$1 AND from_id<>$2 AND action IS NULL ORDER BY id DESC LIMIT $3", channel, user, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int])
}

// ReactionsBy is what user reacted with on a message: emoticons, or '#'
// and a custom emoji's document id.
func (s *Service) ReactionsBy(ctx context.Context, channel int64, msg int, shared int64, user int64) ([]string, error) {
	var rows pgx.Rows
	var err error
	if channel != 0 {
		rows, err = s.db.Query(ctx, "SELECT reaction FROM channel_reactions WHERE channel_id=$1 AND msg_id=$2 AND user_id=$3 ORDER BY position", channel, msg, user)
	} else {
		rows, err = s.db.Query(ctx, "SELECT reaction FROM message_reactions WHERE shared_id=$1 AND user_id=$2 ORDER BY position", shared, user)
	}
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Copies finds what is left of private messages, by shared id: one box
// that still holds each, with its id there.
func (s *Service) Copies(ctx context.Context, shared []int64) (map[int64][]int, error) {
	rows, err := s.db.Query(ctx, "SELECT DISTINCT ON (shared_id) owner_id, id FROM messages WHERE shared_id = ANY($1) ORDER BY shared_id, owner_id", shared)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]int{}
	for rows.Next() {
		var owner int64
		var id int
		if err := rows.Scan(&owner, &id); err != nil {
			return nil, err
		}
		out[owner] = append(out[owner], id)
	}
	return out, rows.Err()
}
