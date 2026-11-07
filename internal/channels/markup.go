package channels

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/messages"
)

// EditBot is a bot's edit of its message: text, keyboard or both. A
// keyboard-only change leaves the message unmarked, like on Telegram.
func (s *Service) EditBot(ctx context.Context, bot, channel int64, id int, c messages.Change) (Msg, []Event, error) {
	var (
		out    Msg
		events []Event
	)
	if c.Text != nil {
		if *c.Text == "" {
			return out, nil, tgerr.New(400, "MESSAGE_EMPTY")
		}
		if utf8.RuneCountInString(*c.Text) > MaxLength {
			return out, nil, tgerr.New(400, "MESSAGE_TOO_LONG")
		}
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		ch, err := lock(ctx, tx, channel)
		if err != nil {
			return err
		}
		me, err := member(ctx, tx, channel, bot)
		if err != nil {
			return err
		}
		m, err := scanMsg(tx.QueryRow(ctx, "SELECT "+msgCols+" FROM channel_messages WHERE channel_id=$1 AND id=$2", channel, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return tgerr.New(400, "MESSAGE_ID_INVALID")
		}
		if err != nil {
			return err
		}
		// a message sent via the bot is its to edit, wherever it was sent
		own := m.From == bot && !m.Post || m.ViaBot == bot
		if m.Action != nil || m.FwdChanID != 0 || !(own || (m.Post && m.From == bot && ch.can(me, "edit"))) {
			return tgerr.New(403, "MESSAGE_AUTHOR_REQUIRED")
		}
		textChanged := c.Text != nil && (*c.Text != m.Text || !sameEntities(c.Entities, m.Entities))
		markupChanged := c.SetMarkup && !sameMarkup(c.Markup, m.ReplyMarkup)
		if !textChanged && !markupChanged {
			return tgerr.New(400, "MESSAGE_NOT_MODIFIED")
		}
		if textChanged {
			m.Text, m.Entities, m.EditDate = *c.Text, c.Entities, s.unix()
		}
		if markupChanged {
			m.ReplyMarkup = c.Markup
		}
		ent, err := encodeEntities(m.Entities)
		if err != nil {
			return err
		}
		markup, err := messages.EncodeMarkup(m.ReplyMarkup)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE channel_messages SET text=$3, entities=$4, edit_date=nullif($5, 0), reply_markup=$6 WHERE channel_id=$1 AND id=$2",
			channel, id, m.Text, ent, m.EditDate, markup); err != nil {
			return err
		}
		u, err := s.journal(ctx, tx, channel, 1, func(pts int) tg.UpdateClass {
			return &tg.UpdateEditChannelMessage{Message: m.TL(), Pts: pts, PtsCount: 1}
		})
		if err != nil {
			return err
		}
		out = m
		ev, err := toMembers(ctx, tx, channel, u)
		events = append(events, ev)
		return err
	})
	return out, events, err
}

func sameEntities(a, b []tg.MessageEntityClass) bool {
	x, _ := encodeEntities(a)
	y, _ := encodeEntities(b)
	return string(x) == string(y)
}

func sameMarkup(a, b tg.ReplyMarkupClass) bool {
	var x, y bin.Buffer
	if a != nil {
		a.Encode(&x)
	}
	if b != nil {
		b.Encode(&y)
	}
	return string(x.Buf) == string(y.Buf)
}
