package channels

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/messages"
)

func itoa(n int) string { return strconv.Itoa(n) }

const MaxLength = 4096

const msgCols = `channel_id, id, from_id, date, post, text, entities, action, coalesce(reply_to, 0), coalesce(fwd_from_id, 0),
	coalesce(fwd_date, 0), coalesce(edit_date, 0), coalesce(fwd_channel_id, 0), coalesce(fwd_channel_msg, 0),
	media, media_kind, media_id, coalesce(grouped_id, 0), reply_markup,
	coalesce(fwd_from_chan, 0), coalesce(fwd_from_post, 0), coalesce(fwd_from_name, ''), coalesce(topic_id, 0), pinned, silent, rich, coalesce(via_bot_id, 0)`

func scanMsg(row pgx.Row) (Msg, error) {
	var m Msg
	var ent, action, raw, markup []byte
	var kind *string
	var mediaID *int64
	err := row.Scan(&m.Channel, &m.ID, &m.From, &m.Date, &m.Post, &m.Text, &ent, &action, &m.ReplyTo, &m.Fwd.User, &m.Fwd.Date, &m.EditDate, &m.FwdChanID, &m.FwdChanMs,
		&raw, &kind, &mediaID, &m.Grouped, &markup, &m.Fwd.Chan, &m.Fwd.Post, &m.Fwd.Name, &m.Topic, &m.Pinned, &m.Silent, &m.Rich, &m.ViaBot)
	if err != nil {
		return m, err
	}
	if m.Entities, err = decodeEntities(ent); err != nil {
		return m, err
	}
	if m.ReplyMarkup, err = messages.DecodeMarkup(markup); err != nil {
		return m, err
	}
	if m.Media, err = media.Load(raw, kind, mediaID); err != nil {
		return m, err
	}
	if len(action) > 0 {
		m.Action, err = tg.DecodeMessageAction(&bin.Buffer{Buf: action})
	}
	return m, err
}

func collect(rows pgx.Rows) ([]Msg, error) {
	defer rows.Close()
	var out []Msg
	for rows.Next() {
		m, err := scanMsg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func encodeEntities(list []tg.MessageEntityClass) ([]byte, error) {
	if len(list) == 0 {
		return nil, nil
	}
	var b bin.Buffer
	b.PutVectorHeader(len(list))
	for _, e := range list {
		if err := e.Encode(&b); err != nil {
			return nil, err
		}
	}
	return b.Buf, nil
}

func decodeEntities(raw []byte) ([]tg.MessageEntityClass, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	b := &bin.Buffer{Buf: raw}
	n, err := b.VectorHeader()
	if err != nil {
		return nil, err
	}
	out := make([]tg.MessageEntityClass, 0, n)
	for range n {
		e, err := tg.DecodeMessageEntity(b)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

type Draft struct {
	Text     string
	Entities []tg.MessageEntityClass
	ReplyTo  int
	RandomID int64
	Fwd      messages.Origin
	Media    media.Ref // text is the caption then, and may be empty
	Grouped  int64
	// bots only
	ReplyMarkup tg.ReplyMarkupClass
	TopMsgID    int // the forum topic asked for (InputReplyToMessage.top_msg_id)
	Silent      bool
	// users the text names; in groups they get a mention
	Mentions []int64
	// an article: encoded richMessage, Text its plain words (past MaxLength)
	Rich []byte
	// sent through this bot's inline mode; ReplyMarkup is the bot's then
	ViaBot int64
}

// Sent is a new channel message: the journal update and who gets it.
type Sent struct {
	Msg    Msg
	Update tg.UpdateClass
	Events []Event
}

func (c Channel) mayPost(m Membership, withMedia bool) error {
	if !InChat(m.Status) {
		return errPrivate
	}
	if !c.can(m, "post") {
		if !c.Megagroup {
			return errAdminRequire
		}
		return errWriteForbid
	}
	if withMedia && !c.can(m, "media") {
		return tgerr.New(403, "CHAT_SEND_MEDIA_FORBIDDEN")
	}
	return nil
}

// CanPost is Send's rights check alone, for checking before a sender's
// upload is taken in.
func (s *Service) CanPost(ctx context.Context, user, channel int64, withMedia bool) error {
	c, ok, err := s.Get(ctx, channel)
	if err != nil {
		return err
	}
	if !ok || c.Deleted {
		return errInvalid
	}
	m, err := s.Member(ctx, channel, user)
	if err != nil {
		return err
	}
	return c.mayPost(m, withMedia)
}

// CanSendInline is whether user may send results from inline bots here.
func (s *Service) CanSendInline(ctx context.Context, user, channel int64) (bool, error) {
	c, ok, err := s.Get(ctx, channel)
	if err != nil || !ok || c.Deleted {
		return false, err
	}
	m, err := s.Member(ctx, channel, user)
	if err != nil {
		return false, err
	}
	return c.can(m, "inline"), nil
}

// Send posts to a channel or group. In a broadcast channel only admins may
// post and the post is the channel's; a linked discussion group gets a copy.
func (s *Service) Send(ctx context.Context, user, channel int64, d Draft) (Sent, error) {
	var out Sent
	if d.Text == "" && d.Media.Empty() && len(d.Rich) == 0 {
		return out, tgerr.New(400, "MESSAGE_EMPTY")
	}
	if len(d.Rich) == 0 && utf8.RuneCountInString(d.Text) > MaxLength {
		return out, tgerr.New(400, "MESSAGE_TOO_LONG")
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		c, err := lock(ctx, tx, channel)
		if err != nil {
			return err
		}
		me, err := member(ctx, tx, channel, user)
		if err != nil {
			return err
		}
		// a link preview is no media of the sender's
		if err := c.mayPost(me, !d.Media.Empty() && d.Media.Kind != media.KindWebPage); err != nil {
			return err
		}
		var random *int64
		if d.RandomID != 0 {
			random = &d.RandomID
			var dup bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM channel_messages WHERE channel_id=$1 AND from_id=$2 AND random_id=$3)", channel, user, d.RandomID).Scan(&dup); err != nil {
				return err
			}
			if dup {
				return tgerr.New(400, "RANDOM_ID_DUPLICATE")
			}
		}
		if d.ReplyTo != 0 {
			var ok bool
			tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM channel_messages WHERE channel_id=$1 AND id=$2)", channel, d.ReplyTo).Scan(&ok)
			if !ok {
				d.ReplyTo = 0
			}
		}
		topic, err := pickTopic(ctx, tx, c, me, d.TopMsgID, d.ReplyTo)
		if err != nil {
			return err
		}
		m := Msg{Channel: channel, From: user, Post: !c.Megagroup, Text: d.Text, Entities: d.Entities, ReplyTo: d.ReplyTo, Fwd: d.Fwd,
			Media: d.Media, Grouped: d.Grouped, ReplyMarkup: d.ReplyMarkup, Topic: topic, Silent: d.Silent, Rich: d.Rich, ViaBot: d.ViaBot}
		m, u, err := s.addMessage(ctx, tx, m, random)
		if err != nil {
			return err
		}
		out.Msg, out.Update = m, u
		ev, err := toMembers(ctx, tx, channel, u)
		if err != nil {
			return err
		}
		if ev.Mentions, err = mention(ctx, tx, c, m, d.Mentions); err != nil {
			return err
		}
		out.Events = append(out.Events, ev)
		// a post lands in the discussion group too, as the thread to comment in
		if !c.Megagroup && c.LinkedID != 0 {
			g, err := lock(ctx, tx, c.LinkedID)
			if err == nil {
				copyMsg := Msg{Channel: g.ID, From: user, Text: d.Text, Entities: d.Entities, FwdChanID: c.ID, FwdChanMs: m.ID, Media: d.Media, Grouped: d.Grouped, Rich: d.Rich}
				if _, gu, err := s.addMessage(ctx, tx, copyMsg, nil); err == nil {
					gev, err := toMembers(ctx, tx, g.ID, gu)
					if err != nil {
						return err
					}
					out.Events = append(out.Events, gev)
				} else {
					return err
				}
			}
		}
		return nil
	})
	return out, err
}

func (s *Service) Messages(ctx context.Context, channel int64, list []int) ([]Msg, error) {
	rows, err := s.db.Query(ctx, "SELECT "+msgCols+" FROM channel_messages WHERE channel_id=$1 AND id = ANY($2) ORDER BY id DESC", channel, list)
	if err != nil {
		return nil, err
	}
	return collect(rows)
}

// Edit changes a message: your own in a group, any post in a channel for
// admins with the edit right. rich makes it an article; nil an ordinary message.
func (s *Service) Edit(ctx context.Context, user, channel int64, id int, text string, entities []tg.MessageEntityClass, rich []byte) (Msg, tg.UpdateClass, []Event, error) {
	var (
		out    Msg
		update tg.UpdateClass
		events []Event
	)
	if len(rich) == 0 && utf8.RuneCountInString(text) > MaxLength {
		return out, nil, nil, tgerr.New(400, "MESSAGE_TOO_LONG")
	}
	ent, err := encodeEntities(entities)
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
		// authorship alone isn't enough once you're out
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
		// a poster fixes its own posts; the edit right is for everyone else's
		own := m.From == user && (!m.Post || c.can(me, "post"))
		if m.Action != nil || m.FwdChanID != 0 || !m.Fwd.Empty() || !(own || (m.Post && c.can(me, "edit"))) {
			return tgerr.New(403, "MESSAGE_AUTHOR_REQUIRED")
		}
		// a photo may lose its caption, a text message can't lose its text
		if text == "" && m.Media.Empty() && len(rich) == 0 {
			return tgerr.New(400, "MESSAGE_EMPTY")
		}
		m.Text, m.Entities, m.EditDate, m.Rich = text, entities, s.unix(), rich
		if _, err := tx.Exec(ctx, "UPDATE channel_messages SET text=$3, entities=$4, edit_date=$5, rich=$6 WHERE channel_id=$1 AND id=$2", channel, id, text, ent, m.EditDate, rich); err != nil {
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

// DeleteMessages removes messages: your own, or any with the delete right.
func (s *Service) DeleteMessages(ctx context.Context, user, channel int64, list []int) (tg.MessagesAffectedMessages, []Event, error) {
	var (
		res    tg.MessagesAffectedMessages
		events []Event
	)
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
		res.Pts = c.Pts
		q := "SELECT id FROM channel_messages WHERE channel_id=$1 AND id = ANY($2)"
		args := []any{channel, list}
		if !c.can(me, "delete") {
			q += " AND from_id=$3 AND NOT post"
			args = append(args, user)
		}
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		gone, err := pgx.CollectRows(rows, pgx.RowTo[int])
		if err != nil {
			return err
		}
		if len(gone) == 0 {
			if len(list) > 0 && !c.can(me, "delete") {
				return tgerr.New(403, "MESSAGE_DELETE_FORBIDDEN")
			}
			return nil
		}
		u, err := s.removeMessages(ctx, tx, c, gone)
		if err != nil {
			return err
		}
		d := u.(*tg.UpdateDeleteChannelMessages)
		res = tg.MessagesAffectedMessages{Pts: d.Pts, PtsCount: d.PtsCount}
		ev, err := toMembers(ctx, tx, channel, u)
		events = append(events, ev)
		return err
	})
	return res, events, err
}

// removeMessages deletes gone (ids that exist) and journals it (inside tx,
// channel locked).
func (s *Service) removeMessages(ctx context.Context, tx pgx.Tx, c Channel, gone []int) (tg.UpdateClass, error) {
	if _, err := tx.Exec(ctx, "DELETE FROM channel_messages WHERE channel_id=$1 AND id = ANY($2)", c.ID, gone); err != nil {
		return nil, err
	}
	var top, date int
	err := tx.QueryRow(ctx, "SELECT id, date FROM channel_messages WHERE channel_id=$1 ORDER BY id DESC LIMIT 1", c.ID).Scan(&top, &date)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if top == 0 {
		date = c.Created // emptied: sort by when it was made
	}
	if _, err := tx.Exec(ctx, "UPDATE channels SET top_id=$2, top_date=$3 WHERE id=$1", c.ID, top, date); err != nil {
		return nil, err
	}
	return s.journal(ctx, tx, c.ID, len(gone), func(pts int) tg.UpdateClass {
		return &tg.UpdateDeleteChannelMessages{ChannelID: c.ID, Messages: gone, Pts: pts, PtsCount: len(gone)}
	})
}

type HistoryQuery struct {
	OffsetID, OffsetDate, AddOffset, Limit, MaxID, MinID int
	// replies to this message only (a comment thread); 0 = whole channel
	Thread int
	// one forum topic (GeneralTopic for General); 0 = whole channel
	Topic int
	// per-viewer filters need the viewer
	Filter messages.Filter
	Viewer int64
}

// History pages through a channel, newest first, with Telegram's offsets.
func (s *Service) History(ctx context.Context, channel int64, q HistoryQuery) ([]Msg, int, error) {
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	where := "channel_id=$1"
	args := []any{channel}
	add := func(cond string, v any) {
		args = append(args, v)
		where += " AND " + strings.ReplaceAll(cond, "?", "$"+itoa(len(args)))
	}
	if q.Thread != 0 {
		add("reply_to = ?", q.Thread)
	}
	switch {
	case q.Topic == GeneralTopic:
		where += " AND topic_id IS NULL"
	case q.Topic != 0:
		add("topic_id = ?", q.Topic)
	}
	switch q.Filter {
	case messages.Pinned:
		where += " AND pinned"
	case messages.UnreadMentions:
		add(`EXISTS (SELECT 1 FROM channel_mentions x WHERE x.channel_id = channel_messages.channel_id AND x.msg_id = channel_messages.id
			AND x.user_id = ? AND x.unread)`, q.Viewer)
	case messages.UnreadReactions:
		add(`EXISTS (SELECT 1 FROM channel_reactions r WHERE r.channel_id = channel_messages.channel_id AND r.msg_id = channel_messages.id
			AND r.author_id = ? AND r.unread)`, q.Viewer)
	}
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM channel_messages WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if q.MaxID > 0 {
		add("id < ?", q.MaxID)
	}
	if q.MinID > 0 {
		add("id > ?", q.MinID)
	}
	anchor := q.OffsetID
	if anchor == 0 && q.OffsetDate > 0 {
		if err := s.db.QueryRow(ctx, "SELECT coalesce(max(id), 0) + 1 FROM channel_messages WHERE channel_id=$1 AND date < $2", channel, q.OffsetDate).Scan(&anchor); err != nil {
			return nil, 0, err
		}
	}
	if anchor == 0 {
		anchor = 1 << 30
	}
	n := len(args)
	var out []Msg
	if q.AddOffset < 0 {
		newer := min(-q.AddOffset, limit)
		rows, err := s.db.Query(ctx, "SELECT "+msgCols+" FROM channel_messages WHERE "+where+" AND id >= $"+itoa(n+1)+" ORDER BY id ASC LIMIT $"+itoa(n+2), append(args, anchor, newer)...)
		if err != nil {
			return nil, 0, err
		}
		got, err := collect(rows)
		if err != nil {
			return nil, 0, err
		}
		slices.Reverse(got)
		out = got
		limit -= len(got)
		q.AddOffset = 0
	}
	if limit > 0 {
		rows, err := s.db.Query(ctx, "SELECT "+msgCols+" FROM channel_messages WHERE "+where+" AND id < $"+itoa(n+1)+" ORDER BY id DESC OFFSET $"+itoa(n+2)+" LIMIT $"+itoa(n+3),
			append(args, anchor, q.AddOffset, limit)...)
		if err != nil {
			return nil, 0, err
		}
		got, err := collect(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, got...)
	}
	return out, total, nil
}

// Search finds messages by text and/or media kind, newest first.
func (s *Service) Search(ctx context.Context, channel int64, text string, offsetID, limit int, kinds ...string) ([]Msg, int, error) {
	return s.SearchTopic(ctx, channel, 0, text, offsetID, limit, kinds...)
}

// SearchTopic is Search inside one forum topic (GeneralTopic for General);
// topic 0 is the whole channel.
func (s *Service) SearchTopic(ctx context.Context, channel int64, topic int, text string, offsetID, limit int, kinds ...string) ([]Msg, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(text) + "%"
	where := "channel_id=$1 AND action IS NULL AND text ILIKE $2"
	args := []any{channel, pattern}
	if cond, arg := messages.KindsSQL(kinds, ""); cond != "" {
		if arg != nil {
			args = append(args, arg)
			cond = strings.ReplaceAll(cond, "$K", "$"+itoa(len(args)))
		}
		where += " AND " + cond
	}
	switch {
	case topic == GeneralTopic:
		where += " AND topic_id IS NULL"
	case topic != 0:
		args = append(args, topic)
		where += " AND topic_id = $" + itoa(len(args))
	}
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM channel_messages WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if offsetID <= 0 {
		offsetID = 1 << 30
	}
	n := len(args)
	rows, err := s.db.Query(ctx, "SELECT "+msgCols+" FROM channel_messages WHERE "+where+" AND id < $"+itoa(n+1)+" ORDER BY id DESC LIMIT $"+itoa(n+2),
		append(args, offsetID, limit)...)
	if err != nil {
		return nil, 0, err
	}
	list, err := collect(rows)
	return list, total, err
}

// ReadHistory marks the user's inbox read; the readers' maximum becomes the
// channel's outbox mark ("seen" ticks for authors).
func (s *Service) ReadHistory(ctx context.Context, user, channel int64, maxID int) (inbox tg.UpdateClass, events []Event, err error) {
	err = s.tx(ctx, func(tx pgx.Tx) error {
		c, err := lock(ctx, tx, channel)
		if err != nil {
			return err
		}
		m, err := member(ctx, tx, channel, user)
		if err != nil {
			return err
		}
		if !InChat(m.Status) {
			return errUserNotPart
		}
		if maxID <= 0 || maxID > c.TopID {
			maxID = c.TopID
		}
		if maxID <= m.ReadInboxMax {
			return nil
		}
		if _, err := tx.Exec(ctx, "UPDATE channel_members SET read_inbox_max=$3 WHERE channel_id=$1 AND user_id=$2", channel, user, maxID); err != nil {
			return err
		}
		var left int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM channel_messages WHERE channel_id=$1 AND id > $2 AND from_id <> $3", channel, maxID, user).Scan(&left); err != nil {
			return err
		}
		inbox = &tg.UpdateReadChannelInbox{ChannelID: channel, MaxID: maxID, StillUnreadCount: left, Pts: c.Pts}
		if err := s.noteRead(ctx, tx, c, user, maxID); err != nil {
			return err
		}
		seen, err := seenUpTo(ctx, tx, channel, user, maxID)
		if err != nil {
			return err
		}
		if seen > c.ReadOutboxMax {
			if _, err := tx.Exec(ctx, "UPDATE channels SET read_outbox_max=$2 WHERE id=$1", channel, seen); err != nil {
				return err
			}
			ev, err := toMembers(ctx, tx, channel, &tg.UpdateReadChannelOutbox{ChannelID: channel, MaxID: seen})
			if err != nil {
				return err
			}
			events = append(events, ev)
		}
		return nil
	})
	return inbox, events, err
}

const differenceLimit = 100

type Difference struct {
	Updates []tg.UpdateClass
	Pts     int
	Final   bool
	TooLong bool
}

// Difference is the channel journal after pts, for getChannelDifference.
func (s *Service) Difference(ctx context.Context, channel int64, pts, limit int) (Difference, error) {
	var d Difference
	if limit <= 0 || limit > differenceLimit {
		limit = differenceLimit
	}
	var head int
	var oldest *int
	if err := s.db.QueryRow(ctx, "SELECT c.pts, (SELECT min(pts - pts_count) FROM channel_journal WHERE channel_id=c.id) FROM channels c WHERE c.id=$1", channel).Scan(&head, &oldest); err != nil {
		return d, err
	}
	d.Pts, d.Final = head, true
	if pts == head {
		return d, nil
	}
	if pts <= 0 || pts > head || oldest != nil && pts < *oldest {
		d.TooLong = true
		return d, nil
	}
	rows, err := s.db.Query(ctx, "SELECT pts, body FROM channel_journal WHERE channel_id=$1 AND pts > $2 ORDER BY pts LIMIT $3", channel, pts, limit+1)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	last := pts
	for rows.Next() {
		var p int
		var body []byte
		if err := rows.Scan(&p, &body); err != nil {
			return d, err
		}
		if len(d.Updates) == limit {
			d.Final = false
			break
		}
		u, err := tg.DecodeUpdate(&bin.Buffer{Buf: body})
		if err != nil {
			return d, err
		}
		d.Updates = append(d.Updates, u)
		last = p
	}
	// as in messages.Difference: rows newer than the head we read count
	if !d.Final || last > d.Pts {
		d.Pts = last
	}
	return d, rows.Err()
}

// Dialog is a channel in someone's chat list.
type Dialog struct {
	Channel Channel
	Member  Membership
	Unread  int
	Top     *Msg
	Folder  int // 0 main list, 1 archive
	// own messages with reactions the member hasn't seen
	UnreadReactions int
	UnreadMentions  int
}

// Dialogs are the chats the user is in, newest activity first.
func (s *Service) Dialogs(ctx context.Context, user int64) ([]Dialog, error) {
	rows, err := s.db.Query(ctx, "SELECT "+chanCols+", "+prefixCols("m.")+`,
		(SELECT count(*) FROM channel_messages x
			LEFT JOIN channel_topic_reads r ON r.channel_id = x.channel_id AND r.user_id = m.user_id AND r.topic_id = coalesce(x.topic_id, 1)
			WHERE x.channel_id = c.id AND x.id > greatest(m.read_inbox_max, coalesce(r.read_inbox_max, 0)) AND x.from_id <> m.user_id), m.folder_id,
		(SELECT count(DISTINCT r.msg_id) FROM channel_reactions r WHERE r.channel_id = c.id AND r.author_id = m.user_id AND r.unread),
		(SELECT count(*) FROM channel_mentions x WHERE x.channel_id = c.id AND x.user_id = m.user_id AND x.unread)
		FROM channel_members m JOIN channels c ON c.id = m.channel_id
		WHERE m.user_id=$1 AND m.status IN `+inChatSQL+` AND NOT c.deleted
		ORDER BY c.top_date DESC, c.id DESC`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Dialog
	for rows.Next() {
		var d Dialog
		var banned, admin, mbanned []byte
		c := &d.Channel
		m := &d.Member
		if err := rows.Scan(&c.ID, &c.Title, &c.About, &c.Username, &c.Megagroup, &c.Signatures, &c.Creator, &c.Created,
			&banned, &c.LinkedID, &c.Pts, &c.TopID, &c.TopDate, &c.ReadOutboxMax, &c.Deleted, &c.Count, &c.PhotoID, &c.Forum, &c.ForumTabs,
			&m.UserID, &m.Status, &admin, &m.Rank, &mbanned, &m.Inviter, &m.PromotedBy, &m.Date, &m.ReadInboxMax, &m.Pinned, &m.ForumAsMessages,
			&d.Unread, &d.Folder, &d.UnreadReactions, &d.UnreadMentions); err != nil {
			return nil, err
		}
		c.DefaultBanned, m.AdminRights, m.BannedRights = decodeBanned(banned), decodeAdmin(admin), decodeBanned(mbanned)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var chs []int64
	var tops []int
	for _, d := range out {
		if d.Channel.TopID != 0 {
			chs, tops = append(chs, d.Channel.ID), append(tops, d.Channel.TopID)
		}
	}
	if len(chs) == 0 {
		return out, nil
	}
	rows, err = s.db.Query(ctx, "SELECT "+msgCols+" FROM channel_messages WHERE (channel_id, id) IN (SELECT * FROM unnest($1::bigint[], $2::int[]))", chs, tops)
	if err != nil {
		return nil, err
	}
	list, err := collect(rows)
	if err != nil {
		return nil, err
	}
	byChan := make(map[int64]*Msg, len(list))
	for i := range list {
		byChan[list[i].Channel] = &list[i]
	}
	for i := range out {
		if m := byChan[out[i].Channel.ID]; m != nil && m.ID == out[i].Channel.TopID {
			out[i].Top = m
		}
	}
	return out, nil
}

func (s *Service) Pin(ctx context.Context, user, channel int64, pinned bool) (bool, error) {
	q := "UPDATE channel_members SET pinned_at = NULL WHERE channel_id=$1 AND user_id=$2"
	if pinned {
		q = "UPDATE channel_members SET pinned_at = extract(epoch FROM clock_timestamp())::int WHERE channel_id=$1 AND user_id=$2"
	}
	tag, err := s.db.Exec(ctx, q, channel, user)
	return tag.RowsAffected() == 1, err
}
