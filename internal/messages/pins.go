package messages

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/ids"
)

// pinUpdate is what a box's journal gets for (un)pinned messages; the
// peer is the other side of that box's dialog.
func pinUpdate(peer int64, list []int, pinned bool) func(pts int) tg.UpdateClass {
	return func(pts int) tg.UpdateClass {
		return &tg.UpdatePinnedMessages{Pinned: pinned, Peer: &tg.PeerUser{UserID: ids.Client(peer)}, Messages: list, Pts: pts, PtsCount: 1}
	}
}

// PinMessage pins or unpins a message of user's dialog with peer, in both boxes
// unless oneSide. The user's journal entry comes back on its own; events
// include it for their other sessions.
func (s *Service) PinMessage(ctx context.Context, user, peer int64, id int, pinned, oneSide bool) (tg.UpdateClass, []Event, error) {
	var (
		mine   tg.UpdateClass
		events []Event
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := lockBoxes(ctx, tx, user, peer); err != nil {
			return err
		}
		var shared int64
		err := tx.QueryRow(ctx, "SELECT shared_id FROM messages WHERE owner_id=$1 AND peer_id=$2 AND id=$3 AND action IS NULL", user, peer, id).Scan(&shared)
		if errors.Is(err, pgx.ErrNoRows) {
			return errMessageInvalid
		}
		if err != nil {
			return err
		}
		q := "UPDATE messages SET pinned=$2 WHERE shared_id=$1 RETURNING owner_id, id"
		args := []any{shared, pinned}
		if oneSide {
			q = "UPDATE messages SET pinned=$2 WHERE shared_id=$1 AND owner_id=$3 RETURNING owner_id, id"
			args = append(args, user)
		}
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		copies := map[int64]int{}
		for rows.Next() {
			var owner int64
			var mid int
			if err := rows.Scan(&owner, &mid); err != nil {
				rows.Close()
				return err
			}
			copies[owner] = mid
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for owner, mid := range copies {
			other := peer
			if owner != user {
				other = user
			}
			u, err := s.journal(ctx, tx, owner, 1, pinUpdate(other, []int{mid}, pinned))
			if err != nil {
				return err
			}
			if owner == user {
				mine = u
			}
			events = append(events, Event{owner, u})
		}
		return nil
	})
	return mine, events, err
}

// UnpinAll unpins every message of user's dialog with peer, on both sides.
func (s *Service) UnpinAll(ctx context.Context, user, peer int64) (tg.MessagesAffectedHistory, []Event, error) {
	var (
		res    tg.MessagesAffectedHistory
		events []Event
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := lockBoxes(ctx, tx, user, peer); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "UPDATE messages SET pinned=false WHERE owner_id=$1 AND peer_id=$2 AND pinned RETURNING id, shared_id", user, peer)
		if err != nil {
			return err
		}
		var mine []int
		var shared []int64
		for rows.Next() {
			var id int
			var sh int64
			if err := rows.Scan(&id, &sh); err != nil {
				rows.Close()
				return err
			}
			mine, shared = append(mine, id), append(shared, sh)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(mine) == 0 {
			res.Pts, err = s.stateTx(ctx, tx, user)
			return err
		}
		u, err := s.journal(ctx, tx, user, 1, pinUpdate(peer, mine, false))
		if err != nil {
			return err
		}
		p := u.(*tg.UpdatePinnedMessages)
		res = tg.MessagesAffectedHistory{Pts: p.Pts, PtsCount: p.PtsCount}
		events = append(events, Event{user, u})
		if peer == user {
			return nil
		}
		rows, err = tx.Query(ctx, "UPDATE messages SET pinned=false WHERE owner_id=$1 AND shared_id = ANY($2) AND pinned RETURNING id", peer, shared)
		if err != nil {
			return err
		}
		theirs, err := pgx.CollectRows(rows, pgx.RowTo[int])
		if err != nil || len(theirs) == 0 {
			return err
		}
		pu, err := s.journal(ctx, tx, peer, 1, pinUpdate(user, theirs, false))
		events = append(events, Event{peer, pu})
		return err
	})
	return res, events, err
}

// TopPinned is the newest pinned message of a dialog, 0 for none.
func (s *Service) TopPinned(ctx context.Context, owner, peer int64) (int, error) {
	var id int
	err := s.db.QueryRow(ctx, "SELECT coalesce(max(id), 0) FROM messages WHERE owner_id=$1 AND peer_id=$2 AND pinned", owner, peer).Scan(&id)
	return id, err
}
