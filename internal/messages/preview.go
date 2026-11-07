package messages

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/media"
)

// AttachPreview puts a link preview on a message that still has no media,
// in both copies, journaled as an edit without an edit date: the preview
// comes in after the message went out.
func (s *Service) AttachPreview(ctx context.Context, owner int64, id int, ref media.Ref) ([]Event, error) {
	raw, err := ref.Encode()
	if err != nil {
		return nil, err
	}
	var events []Event
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var shared, peer int64
		err := tx.QueryRow(ctx, "SELECT shared_id, peer_id FROM messages WHERE owner_id=$1 AND id=$2", owner, id).Scan(&shared, &peer)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := lockBoxes(ctx, tx, owner, peer); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "UPDATE messages SET media=$2, media_kind=$3 WHERE shared_id=$1 AND media IS NULL AND action IS NULL RETURNING "+msgCols,
			shared, raw, ref.Kind)
		if err != nil {
			return err
		}
		copies, err := collect(rows)
		if err != nil {
			return err
		}
		for _, m := range copies {
			m := m
			u, err := s.journal(ctx, tx, m.Owner, 1, func(pts int) tg.UpdateClass {
				return &tg.UpdateEditMessage{Message: m.TL(), Pts: pts, PtsCount: 1}
			})
			if err != nil {
				return err
			}
			events = append(events, Event{m.Owner, u})
		}
		return nil
	})
	return events, err
}
