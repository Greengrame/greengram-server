package channels

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/media"
)

// EditMedia swaps a message's media and, when text isn't nil, its
// caption, under the same rules as Edit.
func (s *Service) EditMedia(ctx context.Context, user, channel int64, id int, ref media.Ref, text *string, entities []tg.MessageEntityClass) (Msg, tg.UpdateClass, []Event, error) {
	var (
		out    Msg
		update tg.UpdateClass
		events []Event
	)
	if text != nil && utf8.RuneCountInString(*text) > MaxLength {
		return out, nil, nil, tgerr.New(400, "MESSAGE_TOO_LONG")
	}
	raw, err := ref.Encode()
	if err != nil {
		return out, nil, nil, err
	}
	err = s.tx(ctx, func(tx pgx.Tx) error {
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
		own := m.From == user && (!m.Post || c.can(me, "post"))
		if m.Action != nil || m.FwdChanID != 0 || !m.Fwd.Empty() || !(own || (m.Post && c.can(me, "edit"))) {
			return tgerr.New(403, "MESSAGE_AUTHOR_REQUIRED")
		}
		if !c.can(me, "media") {
			return tgerr.New(403, "CHAT_SEND_MEDIA_FORBIDDEN")
		}
		m.Media, m.EditDate = ref, s.unix()
		if text != nil {
			m.Text, m.Entities = *text, entities
		}
		ent, err := encodeEntities(m.Entities)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE channel_messages SET media=$3, media_kind=nullif($4, ''), media_id=nullif($5::bigint, 0), edit_date=$6, text=$7, entities=$8
			WHERE channel_id=$1 AND id=$2`, channel, id, raw, ref.Kind, ref.ID, m.EditDate, m.Text, ent); err != nil {
			return err
		}
		u, err := s.journal(ctx, tx, channel, 1, func(pts int) tg.UpdateClass {
			return &tg.UpdateEditChannelMessage{Message: m.TL(), Pts: pts, PtsCount: 1}
		})
		if err != nil {
			return err
		}
		out, update = m, u
		ev, err := toMembers(ctx, tx, channel, u)
		events = append(events, ev)
		return err
	})
	return out, update, events, err
}

// CanEdit says whether user may edit (or stop the poll of) a message.
func (s *Service) CanEdit(ctx context.Context, user, channel int64, id int) (Msg, error) {
	c, ok, err := s.Get(ctx, channel)
	if err != nil {
		return Msg{}, err
	}
	if !ok || c.Deleted {
		return Msg{}, errInvalid
	}
	me, err := s.Member(ctx, channel, user)
	if err != nil {
		return Msg{}, err
	}
	if !InChat(me.Status) {
		return Msg{}, errPrivate
	}
	list, err := s.Messages(ctx, channel, []int{id})
	if err != nil {
		return Msg{}, err
	}
	if len(list) == 0 {
		return Msg{}, tgerr.New(400, "MESSAGE_ID_INVALID")
	}
	m := list[0]
	own := m.From == user && (!m.Post || c.can(me, "post"))
	if m.Action != nil || !(own || (m.Post && c.can(me, "edit"))) {
		return Msg{}, tgerr.New(403, "MESSAGE_AUTHOR_REQUIRED")
	}
	return m, nil
}
