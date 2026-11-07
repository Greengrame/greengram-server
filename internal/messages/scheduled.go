package messages

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/media"
)

// Scheduled is a message waiting for its date, in a private chat (Peer)
// or a channel (Channel). Only Owner sees it until it goes out.
type Scheduled struct {
	ID       int
	Owner    int64
	Peer     int64
	Channel  int64
	Date     int
	Repeat   int // seconds; 0 = once
	EditDate int
	Text     string
	Entities []tg.MessageEntityClass
	ReplyTo  int
	Media    media.Ref
	Grouped  int64
	RandomID int64
	Fwd      Origin
}

// Telegram keeps up to 100 scheduled messages per chat, a year ahead.
const (
	MaxScheduled      = 100
	MaxScheduleAhead  = 366 * 86400
	ScheduleWhenOnline = 0x7ffffffe // "send when online"
)

const schedCols = "id, owner_id, peer_id, channel_id, date, repeat_period, coalesce(edit_date, 0), text, entities, coalesce(reply_to, 0), media, media_kind, media_id, coalesce(grouped_id, 0), coalesce(random_id, 0), " +
	"coalesce(fwd_from_id, 0), coalesce(fwd_from_chan, 0), coalesce(fwd_from_post, 0), coalesce(fwd_from_name, ''), coalesce(fwd_date, 0)"

func scanScheduled(row pgx.Row) (Scheduled, error) {
	var (
		m       Scheduled
		ent     []byte
		raw     []byte
		kind    *string
		mediaID *int64
	)
	if err := row.Scan(&m.ID, &m.Owner, &m.Peer, &m.Channel, &m.Date, &m.Repeat, &m.EditDate, &m.Text, &ent, &m.ReplyTo, &raw, &kind, &mediaID, &m.Grouped, &m.RandomID,
		&m.Fwd.User, &m.Fwd.Chan, &m.Fwd.Post, &m.Fwd.Name, &m.Fwd.Date); err != nil {
		return m, err
	}
	var err error
	if m.Entities, err = decodeEntities(ent); err != nil {
		return m, err
	}
	m.Media, err = media.Load(raw, kind, mediaID)
	return m, err
}

func collectScheduled(rows pgx.Rows) ([]Scheduled, error) {
	defer rows.Close()
	var out []Scheduled
	for rows.Next() {
		m, err := scanScheduled(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// TL is the scheduled message as its owner sees it. A post in a broadcast
// channel speaks as the channel.
func (m Scheduled) TL(post bool) *tg.Message {
	out := &tg.Message{ID: m.ID, Out: true, Date: m.Date, Message: m.Text}
	if m.Channel != 0 {
		out.PeerID = &tg.PeerChannel{ChannelID: m.Channel}
		out.Post = post
		if !post {
			out.SetFromID(&tg.PeerUser{UserID: ids.Client(m.Owner)})
		}
	} else {
		out.PeerID = &tg.PeerUser{UserID: ids.Client(m.Peer)}
		out.SetFromID(&tg.PeerUser{UserID: ids.Client(m.Owner)})
	}
	if len(m.Entities) > 0 {
		out.SetEntities(m.Entities)
	}
	if m.ReplyTo != 0 {
		out.SetReplyTo(&tg.MessageReplyHeader{ReplyToMsgID: m.ReplyTo})
	}
	if m.EditDate != 0 {
		out.SetEditDate(m.EditDate)
	}
	if h, ok := m.Fwd.TL(); ok {
		out.SetFwdFrom(h)
	}
	if !m.Media.Empty() {
		out.SetMedia(m.Media.TL)
	}
	if m.Grouped != 0 {
		out.SetGroupedID(m.Grouped)
	}
	if m.Repeat != 0 {
		out.SetScheduleRepeatPeriod(m.Repeat)
	}
	return out
}

// Schedule stores a message for later. The caller checked that it may be
// sent there.
func (s *Service) Schedule(ctx context.Context, m Scheduled) (Scheduled, error) {
	if m.Text == "" && m.Media.Empty() {
		return m, errEmpty
	}
	if utf8.RuneCountInString(m.Text) > MaxLength {
		return m, errTooLong
	}
	if m.Date > s.unix()+MaxScheduleAhead {
		return m, tgerr.New(400, "SCHEDULE_DATE_TOO_LATE")
	}
	ent, err := encodeEntities(m.Entities)
	if err != nil {
		return m, err
	}
	raw, err := m.Media.Encode()
	if err != nil {
		return m, err
	}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM scheduled_messages WHERE owner_id=$1 AND peer_id=$2 AND channel_id=$3", m.Owner, m.Peer, m.Channel).Scan(&n); err != nil {
			return err
		}
		if n >= MaxScheduled {
			return tgerr.New(400, "SCHEDULE_TOO_MUCH")
		}
		var random *int64
		if m.RandomID != 0 {
			random = &m.RandomID
		}
		err := tx.QueryRow(ctx, `INSERT INTO scheduled_messages(owner_id, peer_id, channel_id, date, repeat_period, text, entities, reply_to, media, media_kind, media_id, grouped_id, random_id,
				fwd_from_id, fwd_from_chan, fwd_from_post, fwd_from_name, fwd_date)
			VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, 0), $9, nullif($10, ''), nullif($11::bigint, 0), nullif($12::bigint, 0), $13,
				nullif($14::bigint, 0), nullif($15::bigint, 0), nullif($16::int, 0), nullif($17, ''), nullif($18::int, 0)) RETURNING id`,
			m.Owner, m.Peer, m.Channel, m.Date, m.Repeat, m.Text, ent, m.ReplyTo, raw, m.Media.Kind, m.Media.ID, m.Grouped, random,
			m.Fwd.User, m.Fwd.Chan, m.Fwd.Post, m.Fwd.Name, m.Fwd.Date).Scan(&m.ID)
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return tgerr.New(400, "RANDOM_ID_DUPLICATE")
		}
		return err
	})
	return m, err
}

// ScheduledIn is the owner's queue for one chat, soonest first; ids
// narrows it to those.
func (s *Service) ScheduledIn(ctx context.Context, owner, peer, channel int64, list []int) ([]Scheduled, error) {
	q := "SELECT " + schedCols + " FROM scheduled_messages WHERE owner_id=$1 AND peer_id=$2 AND channel_id=$3"
	args := []any{owner, peer, channel}
	if list != nil {
		q += " AND id = ANY($4)"
		args = append(args, list)
	}
	rows, err := s.db.Query(ctx, q+" ORDER BY date, id", args...)
	if err != nil {
		return nil, err
	}
	return collectScheduled(rows)
}

// DeleteScheduled drops queued messages and returns the ids that were there.
func (s *Service) DeleteScheduled(ctx context.Context, owner, peer, channel int64, list []int) ([]int, error) {
	rows, err := s.db.Query(ctx, "DELETE FROM scheduled_messages WHERE owner_id=$1 AND peer_id=$2 AND channel_id=$3 AND id = ANY($4) RETURNING id",
		owner, peer, channel, list)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int])
}

// EditScheduled changes a queued message: its text, its media, or when it
// goes. A nil field stays.
func (s *Service) EditScheduled(ctx context.Context, owner, peer, channel int64, id int, text *string, entities []tg.MessageEntityClass,
	ref *media.Ref, date int) (Scheduled, error) {
	var out Scheduled
	err := s.tx(ctx, func(tx pgx.Tx) error {
		m, err := scanScheduled(tx.QueryRow(ctx, "SELECT "+schedCols+" FROM scheduled_messages WHERE owner_id=$1 AND peer_id=$2 AND channel_id=$3 AND id=$4 FOR UPDATE",
			owner, peer, channel, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return errMessageInvalid
		}
		if err != nil {
			return err
		}
		if text != nil {
			if utf8.RuneCountInString(*text) > MaxLength {
				return errTooLong
			}
			m.Text, m.Entities = *text, entities
		}
		if ref != nil {
			m.Media = *ref
		}
		if m.Text == "" && m.Media.Empty() {
			return errEmpty
		}
		if date != 0 {
			if date > s.unix()+MaxScheduleAhead {
				return tgerr.New(400, "SCHEDULE_DATE_TOO_LATE")
			}
			m.Date = date
		}
		m.EditDate = s.unix()
		ent, err := encodeEntities(m.Entities)
		if err != nil {
			return err
		}
		raw, err := m.Media.Encode()
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE scheduled_messages SET text=$2, entities=$3, media=$4, media_kind=nullif($5, ''), media_id=nullif($6::bigint, 0),
			date=$7, edit_date=$8, claimed_until=NULL WHERE id=$1`, m.ID, m.Text, ent, raw, m.Media.Kind, m.Media.ID, m.Date, m.EditDate)
		out = m
		return err
	})
	return out, err
}

// claimLease is how long a sender holds a claimed message before another
// may try it (a crash between claim and send).
const claimLease = 60

// ClaimDue takes up to limit messages whose time came. Each is the
// caller's until it calls Sent.
func (s *Service) ClaimDue(ctx context.Context, limit int) ([]Scheduled, error) {
	now := s.unix()
	rows, err := s.db.Query(ctx, `UPDATE scheduled_messages SET claimed_until=$1 + `+itoa(claimLease)+` WHERE id IN (
			SELECT id FROM scheduled_messages WHERE date <= $1 AND (claimed_until IS NULL OR claimed_until < $1)
			ORDER BY date, id LIMIT $2 FOR UPDATE SKIP LOCKED)
		RETURNING `+schedCols, now, limit)
	if err != nil {
		return nil, err
	}
	return collectScheduled(rows)
}

// ClaimNow takes the given messages of one chat to send at once
// (messages.sendScheduledMessages), soonest first.
func (s *Service) ClaimNow(ctx context.Context, owner, peer, channel int64, list []int) ([]Scheduled, error) {
	now := s.unix()
	rows, err := s.db.Query(ctx, `UPDATE scheduled_messages SET claimed_until=$5 + `+itoa(claimLease)+`
		WHERE owner_id=$1 AND peer_id=$2 AND channel_id=$3 AND id = ANY($4) AND (claimed_until IS NULL OR claimed_until < $5)
		RETURNING `+schedCols, owner, peer, channel, list, now)
	if err != nil {
		return nil, err
	}
	out, err := collectScheduled(rows)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b Scheduled) int { return cmp.Or(cmp.Compare(a.Date, b.Date), cmp.Compare(a.ID, b.ID)) })
	return out, nil
}


// SentScheduled is done with a claimed message: gone, or for a repeating
// one, queued again a period later. The returned date is 0 when it's gone.
func (s *Service) SentScheduled(ctx context.Context, m Scheduled, now int) (int, error) {
	if m.Repeat > 0 {
		next := m.Date
		for next <= now {
			next += m.Repeat
		}
		_, err := s.db.Exec(ctx, "UPDATE scheduled_messages SET date=$2, claimed_until=NULL WHERE id=$1", m.ID, next)
		return next, err
	}
	_, err := s.db.Exec(ctx, "DELETE FROM scheduled_messages WHERE id=$1", m.ID)
	return 0, err
}

// ScheduledCount is how many messages the owner has queued in a chat.
func (s *Service) ScheduledCount(ctx context.Context, owner, peer, channel int64) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, "SELECT count(*) FROM scheduled_messages WHERE owner_id=$1 AND peer_id=$2 AND channel_id=$3", owner, peer, channel).Scan(&n)
	return n, err
}
