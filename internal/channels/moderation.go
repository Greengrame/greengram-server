package channels

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Remove deletes messages for everyone on an admin's say from the panel:
// no member rights are checked, the panel already checked the admin. It
// returns how many there were.
func (s *Service) Remove(ctx context.Context, channel int64, list []int) (int, []Event, error) {
	var (
		n      int
		events []Event
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		c, err := lock(ctx, tx, channel)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT id FROM channel_messages WHERE channel_id=$1 AND id = ANY($2)", channel, list)
		if err != nil {
			return err
		}
		gone, err := pgx.CollectRows(rows, pgx.RowTo[int])
		if err != nil || len(gone) == 0 {
			return err
		}
		u, err := s.removeMessages(ctx, tx, c, gone)
		if err != nil {
			return err
		}
		ev, err := toMembers(ctx, tx, channel, u)
		if err != nil {
			return err
		}
		n, events = len(gone), append(events, ev)
		return nil
	})
	return n, events, err
}
