package channels

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/media"
)

// AttachPreview puts a link preview on a message that still has no media,
// journaled as an edit without an edit date.
func (s *Service) AttachPreview(ctx context.Context, channel int64, id int, ref media.Ref) ([]Event, error) {
	raw, err := ref.Encode()
	if err != nil {
		return nil, err
	}
	var events []Event
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := lock(ctx, tx, channel); err != nil {
			return err
		}
		m, err := scanMsg(tx.QueryRow(ctx, "UPDATE channel_messages SET media=$3, media_kind=$4 WHERE channel_id=$1 AND id=$2 AND media IS NULL AND action IS NULL RETURNING "+msgCols,
			channel, id, raw, ref.Kind))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		u, err := s.journal(ctx, tx, channel, 1, func(pts int) tg.UpdateClass {
			return &tg.UpdateEditChannelMessage{Message: m.TL(), Pts: pts, PtsCount: 1}
		})
		if err != nil {
			return err
		}
		ev, err := toMembers(ctx, tx, channel, u)
		events = append(events, ev)
		return err
	})
	return events, err
}
