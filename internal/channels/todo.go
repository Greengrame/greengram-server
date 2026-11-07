package channels

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
)

// ChangeTodo changes the checklist in a channel message, journals the edit and
// adds the service message act says, when it isn't nil. No edit_date: ticking
// isn't editing.
func (s *Service) ChangeTodo(ctx context.Context, user, channel int64, id int, act tg.MessageActionClass, change func(m *tg.MessageMediaToDo, own bool) (bool, error)) ([]Event, error) {
	var events []Event
	err := s.tx(ctx, func(tx pgx.Tx) error {
		c, err := lock(ctx, tx, channel)
		if err != nil {
			return err
		}
		me, err := member(ctx, tx, channel, user)
		if err != nil {
			return err
		}
		if !InChat(me.Status) {
			return errPrivate
		}
		m, err := scanMsg(tx.QueryRow(ctx, "SELECT "+msgCols+" FROM channel_messages WHERE channel_id=$1 AND id=$2", channel, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return tgerr.New(400, "MESSAGE_ID_INVALID")
		}
		if err != nil {
			return err
		}
		todo, ok := m.Media.TL.(*tg.MessageMediaToDo)
		if !ok {
			return tgerr.New(400, "MESSAGE_ID_INVALID")
		}
		changed, err := change(todo, m.From == user)
		if err != nil || !changed {
			return err
		}
		raw, err := m.Media.Encode()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE channel_messages SET media=$3 WHERE channel_id=$1 AND id=$2", channel, id, raw); err != nil {
			return err
		}
		u, err := s.journal(ctx, tx, channel, 1, func(pts int) tg.UpdateClass {
			return &tg.UpdateEditChannelMessage{Message: m.TL(), Pts: pts, PtsCount: 1}
		})
		if err != nil {
			return err
		}
		ev, err := toMembers(ctx, tx, channel, u)
		if err != nil {
			return err
		}
		events = append(events, ev)
		if act == nil {
			return nil
		}
		_, su, err := s.addMessage(ctx, tx, Msg{Channel: channel, From: user, Post: !c.Megagroup, Action: act, ReplyTo: id}, nil)
		if err != nil {
			return err
		}
		ev, err = toMembers(ctx, tx, channel, su)
		events = append(events, ev)
		return err
	})
	return events, err
}
