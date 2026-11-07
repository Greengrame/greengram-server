package messages

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Block is one entry of a user's block list.
type Block struct {
	UserID int64
	Date   int
}

// Block puts user on owner's block list; false if they were on it already.
func (s *Service) Block(ctx context.Context, owner, user int64) (bool, error) {
	if owner == user {
		return false, errPeerInvalid
	}
	tag, err := s.db.Exec(ctx, "INSERT INTO blocked_users(blocker_id, blocked_id, date) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING", owner, user, s.unix())
	return tag.RowsAffected() == 1, err
}

// Unblock takes user off owner's list; false if they weren't on it.
func (s *Service) Unblock(ctx context.Context, owner, user int64) (bool, error) {
	tag, err := s.db.Exec(ctx, "DELETE FROM blocked_users WHERE blocker_id=$1 AND blocked_id=$2", owner, user)
	return tag.RowsAffected() == 1, err
}

// Blocked is one page of owner's block list, newest first, and its length.
func (s *Service) Blocked(ctx context.Context, owner int64, offset, limit int) ([]Block, int, error) {
	limit = clampLimit(limit, 100)
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM blocked_users WHERE blocker_id=$1", owner).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(ctx, "SELECT blocked_id, date FROM blocked_users WHERE blocker_id=$1 ORDER BY date DESC, blocked_id DESC OFFSET $2 LIMIT $3",
		owner, max(offset, 0), limit)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Block, error) {
		var b Block
		err := r.Scan(&b.UserID, &b.Date)
		return b, err
	})
	return list, total, err
}

// Blocks tells whether a blocked b and whether b blocked a.
func (s *Service) Blocks(ctx context.Context, a, b int64) (aBlockedB, bBlockedA bool, err error) {
	err = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM blocked_users WHERE blocker_id=$1 AND blocked_id=$2),
		EXISTS(SELECT 1 FROM blocked_users WHERE blocker_id=$2 AND blocked_id=$1)`, a, b).Scan(&aBlockedB, &bBlockedA)
	return
}

// BlockedBy are the owners among list who blocked user.
func (s *Service) BlockedBy(ctx context.Context, user int64, owners []int64) (map[int64]bool, error) {
	return s.idSet(ctx, "SELECT blocker_id FROM blocked_users WHERE blocked_id=$1 AND blocker_id = ANY($2)", user, owners)
}

// BlockedAmong are the users among list that owner blocked.
func (s *Service) BlockedAmong(ctx context.Context, owner int64, list []int64) (map[int64]bool, error) {
	return s.idSet(ctx, "SELECT blocked_id FROM blocked_users WHERE blocker_id=$1 AND blocked_id = ANY($2)", owner, list)
}

// Watchers are the people who would see user's name, photo or status:
// everyone with a private chat with them or them in their book.
func (s *Service) Watchers(ctx context.Context, user int64) ([]int64, error) {
	rows, err := s.db.Query(ctx, `SELECT owner_id FROM dialogs WHERE peer_id=$1 AND owner_id<>$1
		UNION SELECT owner_id FROM contacts WHERE user_id=$1`, user)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// HaveAsContact are the owners among list who have user in their book.
func (s *Service) HaveAsContact(ctx context.Context, user int64, owners []int64) (map[int64]bool, error) {
	return s.idSet(ctx, "SELECT owner_id FROM contacts WHERE user_id=$1 AND owner_id = ANY($2)", user, owners)
}

func (s *Service) idSet(ctx context.Context, q string, id int64, list []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(list) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, q, id, list)
	if err != nil {
		return nil, err
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	for _, v := range got {
		out[v] = true
	}
	return out, err
}
