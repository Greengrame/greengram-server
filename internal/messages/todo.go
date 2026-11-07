package messages

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/media"
)

// TodoRules says what the sender of a checklist lets the rest of the chat do.
func TodoRules(m *tg.MessageMediaToDo, own bool, appending bool) error {
	switch {
	case own:
		return nil
	case appending && m.Todo.OthersCanAppend, !appending && m.Todo.OthersCanComplete:
		return nil
	}
	return tgerr.New(403, "TODO_NOT_ALLOWED")
}

// ChangeTodo changes the checklist in a private message, both copies, and
// journals the edit. It has no edit_date: ticking isn't editing.
func (s *Service) ChangeTodo(ctx context.Context, user, peer int64, id int, change func(m *tg.MessageMediaToDo, own bool) (bool, error)) (tg.UpdateClass, []Event, error) {
	var (
		update tg.UpdateClass
		events []Event
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := lockBoxes(ctx, tx, user, peer); err != nil {
			return err
		}
		var (
			shared int64
			out    bool
			raw    []byte
		)
		err := tx.QueryRow(ctx, "SELECT shared_id, out, media FROM messages WHERE owner_id=$1 AND peer_id=$2 AND id=$3 AND media_kind=$4",
			user, peer, id, media.KindTodo).Scan(&shared, &out, &raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return errMessageInvalid
		}
		if err != nil {
			return err
		}
		kind := media.KindTodo
		ref, err := media.Load(raw, &kind, nil)
		if err != nil {
			return err
		}
		m, ok := ref.TL.(*tg.MessageMediaToDo)
		if !ok {
			return errMessageInvalid
		}
		changed, err := change(m, out)
		if err != nil || !changed {
			return err
		}
		body, err := ref.Encode()
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "UPDATE messages SET media=$2 WHERE shared_id=$1 RETURNING "+msgCols, shared, body)
		if err != nil {
			return err
		}
		copies, err := collect(rows)
		if err != nil {
			return err
		}
		for _, c := range copies {
			u, err := s.journal(ctx, tx, c.Owner, 1, func(pts int) tg.UpdateClass {
				return &tg.UpdateEditMessage{Message: c.TL(), Pts: pts, PtsCount: 1}
			})
			if err != nil {
				return err
			}
			if c.Owner == user {
				update = u
			}
			events = append(events, Event{c.Owner, u})
		}
		return nil
	})
	return update, events, err
}
