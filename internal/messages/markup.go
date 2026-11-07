package messages

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
)

// EncodeMarkup is the stored form of a keyboard; nil stays NULL.
func EncodeMarkup(m tg.ReplyMarkupClass) ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	var b bin.Buffer
	if err := m.Encode(&b); err != nil {
		return nil, err
	}
	return b.Buf, nil
}

func DecodeMarkup(raw []byte) (tg.ReplyMarkupClass, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	return tg.DecodeReplyMarkup(&bin.Buffer{Buf: raw})
}

// Change is a bot's edit: new text, a new keyboard, or both.
type Change struct {
	Text      *string
	Entities  []tg.MessageEntityClass
	SetMarkup bool
	Markup    tg.ReplyMarkupClass // nil with SetMarkup drops the keyboard
}

// EditBot applies a bot's edit to its own message in both boxes. Only a
// text change counts as "edited"; swapping the keyboard doesn't, like on
// Telegram.
func (s *Service) EditBot(ctx context.Context, bot, peer int64, id int, c Change) (Msg, []Event, error) {
	var (
		mine   Msg
		events []Event
	)
	if err := checkChange(c); err != nil {
		return mine, nil, err
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := lockBoxes(ctx, tx, bot, peer); err != nil {
			return err
		}
		m, err := scanMsg(tx.QueryRow(ctx, "SELECT "+msgCols+" FROM messages WHERE owner_id=$1 AND peer_id=$2 AND id=$3 AND out", bot, peer, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return tgerr.New(400, "MESSAGE_ID_INVALID")
		}
		if err != nil {
			return err
		}
		mine, events, err = s.applyBotEdit(ctx, tx, m, bot, c)
		return err
	})
	return mine, events, err
}

// EditInline is a bot's edit of a private message sent via it, named by the id
// both copies share. The bot isn't in that chat, so every copy goes out as an event.
func (s *Service) EditInline(ctx context.Context, bot, shared int64, c Change) ([]Event, error) {
	if err := checkChange(c); err != nil {
		return nil, err
	}
	var events []Event
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var a, b int64
		err := tx.QueryRow(ctx, "SELECT owner_id, peer_id FROM messages WHERE shared_id=$1 AND via_bot_id=$2 LIMIT 1", shared, bot).Scan(&a, &b)
		if errors.Is(err, pgx.ErrNoRows) {
			return tgerr.New(400, "MESSAGE_ID_INVALID")
		}
		if err != nil {
			return err
		}
		if err := lockBoxes(ctx, tx, a, b); err != nil {
			return err
		}
		m, err := scanMsg(tx.QueryRow(ctx, "SELECT "+msgCols+" FROM messages WHERE owner_id=$1 AND shared_id=$2", a, shared))
		if err != nil {
			return err
		}
		_, events, err = s.applyBotEdit(ctx, tx, m, bot, c)
		return err
	})
	return events, err
}

func checkChange(c Change) error {
	if c.Text != nil {
		if *c.Text == "" {
			return errEmpty
		}
		if utf8.RuneCountInString(*c.Text) > MaxLength {
			return errTooLong
		}
	}
	return nil
}

// applyBotEdit writes a change to every copy of m and journals it. The copy in
// the bot's own box, if it has one, comes back as mine; the rest are events.
func (s *Service) applyBotEdit(ctx context.Context, tx pgx.Tx, m Msg, bot int64, c Change) (Msg, []Event, error) {
	var (
		mine   Msg
		events []Event
	)
	text, ent, markup := m.Text, m.Entities, m.ReplyMarkup
	changed := false
	if c.Text != nil && (*c.Text != m.Text || !sameTL(entityList(c.Entities), entityList(m.Entities))) {
		text, ent, changed = *c.Text, c.Entities, true
	}
	markupChanged := false
	if c.SetMarkup && !sameTL(c.Markup, m.ReplyMarkup) {
		markup, markupChanged = c.Markup, true
	}
	if !changed && !markupChanged {
		return mine, nil, tgerr.New(400, "MESSAGE_NOT_MODIFIED")
	}
	rawEnt, err := encodeEntities(ent)
	if err != nil {
		return mine, nil, err
	}
	rawMarkup, err := EncodeMarkup(markup)
	if err != nil {
		return mine, nil, err
	}
	q := "UPDATE messages SET text=$2, entities=$3, reply_markup=$4 WHERE shared_id=$1 RETURNING " + msgCols
	args := []any{m.Shared, text, rawEnt, rawMarkup}
	if changed {
		q = "UPDATE messages SET text=$2, entities=$3, reply_markup=$4, edit_date=$5 WHERE shared_id=$1 RETURNING " + msgCols
		args = append(args, s.unix())
	}
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return mine, nil, err
	}
	copies, err := collect(rows)
	if err != nil {
		return mine, nil, err
	}
	for _, cp := range copies {
		cp := cp
		u, err := s.journal(ctx, tx, cp.Owner, 1, func(pts int) tg.UpdateClass {
			return &tg.UpdateEditMessage{Message: cp.TL(), Pts: pts, PtsCount: 1}
		})
		if err != nil {
			return mine, nil, err
		}
		if cp.Owner == bot {
			mine = cp
		} else {
			events = append(events, Event{cp.Owner, u})
		}
	}
	return mine, events, nil
}

// entityList lets a vector compare like any other TL object.
type entityList []tg.MessageEntityClass

func (l entityList) Encode(b *bin.Buffer) error {
	b.PutVectorHeader(len(l))
	for _, e := range l {
		if err := e.Encode(b); err != nil {
			return err
		}
	}
	return nil
}

func sameTL(a, b bin.Encoder) bool {
	var x, y bin.Buffer
	if a != nil {
		a.Encode(&x)
	}
	if b != nil {
		b.Encode(&y)
	}
	return string(x.Buf) == string(y.Buf)
}

// CopyIn finds the copy of a shared message in owner's box: a press on a
// keyboard names the presser's copy, the bot needs its own.
func (s *Service) CopyIn(ctx context.Context, owner int64, shared int64) (Msg, bool, error) {
	m, err := scanMsg(s.db.QueryRow(ctx, "SELECT "+msgCols+" FROM messages WHERE owner_id=$1 AND shared_id=$2", owner, shared))
	if errors.Is(err, pgx.ErrNoRows) {
		return m, false, nil
	}
	return m, err == nil, err
}
