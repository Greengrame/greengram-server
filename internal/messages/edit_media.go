package messages

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/media"
)

// EditMedia swaps the media of one of the user's own messages in both
// boxes (a new photo, a live location that moved) and, when text isn't
// nil, its caption too.
func (s *Service) EditMedia(ctx context.Context, user, peer int64, id int, ref media.Ref, text *string, entities []tg.MessageEntityClass) (Msg, tg.UpdateClass, []Event, error) {
	var (
		mine   Msg
		update tg.UpdateClass
		events []Event
	)
	if text != nil && utf8.RuneCountInString(*text) > MaxLength {
		return mine, nil, nil, errTooLong
	}
	raw, err := ref.Encode()
	if err != nil {
		return mine, nil, nil, err
	}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := lockBoxes(ctx, tx, user, peer); err != nil {
			return err
		}
		var shared int64
		err := tx.QueryRow(ctx, "SELECT shared_id FROM messages WHERE owner_id=$1 AND peer_id=$2 AND id=$3 AND out AND fwd_from_id IS NULL AND fwd_date IS NULL",
			user, peer, id).Scan(&shared)
		if errors.Is(err, pgx.ErrNoRows) {
			return tgerr.New(400, "MESSAGE_AUTHOR_REQUIRED")
		}
		if err != nil {
			return err
		}
		q := "UPDATE messages SET media=$2, media_kind=nullif($3, ''), media_id=nullif($4::bigint, 0), edit_date=$5 WHERE shared_id=$1 RETURNING " + msgCols
		args := []any{shared, raw, ref.Kind, ref.ID, s.unix()}
		if text != nil {
			ent, err := encodeEntities(entities)
			if err != nil {
				return err
			}
			q = "UPDATE messages SET media=$2, media_kind=nullif($3, ''), media_id=nullif($4::bigint, 0), edit_date=$5, text=$6, entities=$7 WHERE shared_id=$1 RETURNING " + msgCols
			args = append(args, *text, ent)
		}
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		copies, err := collect(rows)
		if err != nil {
			return err
		}
		for _, m := range copies {
			u, err := s.journal(ctx, tx, m.Owner, 1, func(pts int) tg.UpdateClass {
				return &tg.UpdateEditMessage{Message: m.TL(), Pts: pts, PtsCount: 1}
			})
			if err != nil {
				return err
			}
			if m.Owner == user {
				mine, update = m, u
			} else {
				events = append(events, Event{m.Owner, u})
			}
		}
		return nil
	})
	return mine, update, events, err
}
