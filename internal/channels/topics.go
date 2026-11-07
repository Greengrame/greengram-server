package channels

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/ids"
)

// GeneralTopic is the topic every forum has. It isn't a message: its
// messages are the ones with no topic.
const GeneralTopic = 1

// Telegram's six fallback icon colours; anything else gets the first.
var topicColors = []int{0x6FB9F0, 0xFFD67E, 0xCB86DB, 0x8EEE98, 0xFF93B2, 0xFB6F5F}

const maxPinnedTopics = 5

var (
	errNoForum      = tgerr.New(400, "CHANNEL_FORUM_MISSING")
	errTopicInvalid = tgerr.New(400, "TOPIC_ID_INVALID")
)

// Topic is one forum topic, plus how it stands for one member.
type Topic struct {
	ID            int
	Title         string
	TitleMissing  bool
	IconColor     int
	IconEmojiID   int64
	Creator       int64
	Date          int
	Closed        bool
	Hidden        bool
	Pinned        bool
	ReadOutboxMax int
	// for the viewer
	TopID        int // newest message, 0 if none
	ReadInboxMax int
	Unread       int
}

// TL is the topic as viewer sees it.
func (t Topic) TL(channel, viewer int64) *tg.ForumTopic {
	out := &tg.ForumTopic{
		ID:              t.ID,
		Date:            t.Date,
		Peer:            &tg.PeerChannel{ChannelID: channel},
		Title:           t.Title,
		IconColor:       t.IconColor,
		TopMessage:      t.TopID,
		ReadInboxMaxID:  t.ReadInboxMax,
		ReadOutboxMaxID: t.ReadOutboxMax,
		UnreadCount:     t.Unread,
		FromID:          &tg.PeerUser{UserID: ids.Client(t.Creator)},
		NotifySettings:  tg.PeerNotifySettings{},
		My:              t.Creator == viewer,
		Closed:          t.Closed,
		Pinned:          t.Pinned,
		Hidden:          t.Hidden,
		TitleMissing:    t.TitleMissing,
	}
	if t.IconEmojiID != 0 {
		out.SetIconEmojiID(t.IconEmojiID)
	}
	return out
}

// managesTopics is the admin side of the topics right: members may be
// allowed to open topics, but closing, pinning and writing in closed ones
// is for admins with it.
func (c Channel) managesTopics(m Membership) bool {
	return (m.Status == Creator || m.Status == Admin) && c.can(m, "topics")
}

// pickTopic is where a message goes in a forum: the topic of the message
// it replies to, else the one asked for, else General (0). Closed topics
// take messages from topic managers only.
func pickTopic(ctx context.Context, tx pgx.Tx, c Channel, me Membership, top, reply int) (int, error) {
	if !c.Forum {
		return 0, nil
	}
	t := top
	if reply != 0 {
		var of int
		err := tx.QueryRow(ctx, "SELECT coalesce(topic_id, 0) FROM channel_messages WHERE channel_id=$1 AND id=$2", c.ID, reply).Scan(&of)
		if err == nil {
			t = of
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return 0, err
		}
	}
	row := t
	if t == 0 || t == GeneralTopic {
		t, row = 0, GeneralTopic
	}
	var closed bool
	err := tx.QueryRow(ctx, "SELECT closed FROM channel_topics WHERE channel_id=$1 AND id=$2", c.ID, row).Scan(&closed)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if t != 0 {
			return 0, tgerr.New(400, "TOPIC_DELETED")
		}
		return 0, nil
	case err != nil:
		return 0, err
	}
	if closed && !c.managesTopics(me) {
		return 0, tgerr.New(400, "TOPIC_CLOSED")
	}
	return t, nil
}

// ToggleForum turns topics on or off in a group. Messages keep their
// topics either way, so turning it back on finds them where they were.
func (s *Service) ToggleForum(ctx context.Context, actor, channel int64, enabled, tabs bool) ([]Event, error) {
	var events []Event
	err := s.tx(ctx, func(tx pgx.Tx) error {
		c, err := lock(ctx, tx, channel)
		if err != nil {
			return err
		}
		me, err := member(ctx, tx, channel, actor)
		if err != nil {
			return err
		}
		if !c.Megagroup || !c.can(me, "change_info") || me.Status != Creator && me.Status != Admin {
			return errAdminRequire
		}
		// comments under channel posts are threads, not topics
		if enabled && c.LinkedID != 0 {
			return tgerr.New(400, "CHAT_DISCUSSION_UNALLOWED")
		}
		tabs = enabled && tabs
		if c.Forum == enabled && c.ForumTabs == tabs {
			return tgerr.New(400, "CHAT_NOT_MODIFIED")
		}
		if _, err := tx.Exec(ctx, "UPDATE channels SET forum=$2, forum_tabs=$3 WHERE id=$1", channel, enabled, tabs); err != nil {
			return err
		}
		if enabled {
			if _, err := tx.Exec(ctx, `INSERT INTO channel_topics(channel_id, id, title, icon_color, creator_id, date)
				VALUES ($1, $2, 'General', $3, $4, $5) ON CONFLICT DO NOTHING`, channel, GeneralTopic, topicColors[0], c.Creator, c.Created); err != nil {
				return err
			}
		}
		ev, err := toMembers(ctx, tx, channel, updChannel(channel))
		events = append(events, ev)
		return err
	})
	return events, err
}

// SetForumAsMessages is the member's own "view as messages" switch. Telegram
// sends no pts for it, so it only goes to their sessions that are online.
func (s *Service) SetForumAsMessages(ctx context.Context, user, channel int64, on bool) ([]Event, error) {
	c, m, err := s.Access(ctx, channel, user)
	if err != nil {
		return nil, err
	}
	if !c.Forum {
		return nil, errNoForum
	}
	if !InChat(m.Status) {
		return nil, errPrivate
	}
	if _, err := s.db.Exec(ctx, "UPDATE channel_members SET forum_as_messages=$3 WHERE channel_id=$1 AND user_id=$2", channel, user, on); err != nil {
		return nil, err
	}
	return []Event{{Users: []int64{user}, Update: &tg.UpdateChannelViewForumAsMessages{ChannelID: channel, Enabled: on}}}, nil
}

func checkTopicTitle(title string, missing bool) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" && !missing {
		return "", tgerr.New(400, "TOPIC_TITLE_EMPTY")
	}
	if utf8.RuneCountInString(title) > 128 {
		return "", tgerr.New(400, "TOPIC_TITLE_TOO_LONG")
	}
	return title, nil
}

// NewTopic is messages.createForumTopic's input.
type NewTopic struct {
	Title        string
	TitleMissing bool
	IconColor    int
	IconEmojiID  int64
	RandomID     int64
}

// CreateTopic opens a topic with its service message; the message id is
// the topic id.
func (s *Service) CreateTopic(ctx context.Context, actor, channel int64, n NewTopic) (Sent, error) {
	var out Sent
	title, err := checkTopicTitle(n.Title, n.TitleMissing)
	if err != nil {
		return out, err
	}
	if !slices.Contains(topicColors, n.IconColor) {
		n.IconColor = topicColors[0]
	}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		c, err := lock(ctx, tx, channel)
		if err != nil {
			return err
		}
		if !c.Forum {
			return errNoForum
		}
		me, err := member(ctx, tx, channel, actor)
		if err != nil {
			return err
		}
		if !InChat(me.Status) {
			return errPrivate
		}
		if !c.can(me, "topics") {
			return errAdminRequire
		}
		var random *int64
		if n.RandomID != 0 {
			random = &n.RandomID
			var dup bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM channel_messages WHERE channel_id=$1 AND from_id=$2 AND random_id=$3)", channel, actor, n.RandomID).Scan(&dup); err != nil {
				return err
			}
			if dup {
				return tgerr.New(400, "RANDOM_ID_DUPLICATE")
			}
		}
		action := &tg.MessageActionTopicCreate{Title: title, IconColor: n.IconColor, TitleMissing: n.TitleMissing}
		if n.IconEmojiID != 0 {
			action.SetIconEmojiID(n.IconEmojiID)
		}
		m, u, err := s.addMessage(ctx, tx, Msg{Channel: channel, From: actor, Action: action}, random)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO channel_topics(channel_id, id, title, title_missing, icon_color, icon_emoji_id, creator_id, date)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, channel, m.ID, title, n.TitleMissing, n.IconColor, n.IconEmojiID, actor, m.Date); err != nil {
			return err
		}
		out.Msg, out.Update = m, u
		ev, err := toMembers(ctx, tx, channel, u)
		out.Events = append(out.Events, ev)
		return err
	})
	return out, err
}

const topicCols = "id, title, title_missing, icon_color, icon_emoji_id, creator_id, date, closed, hidden, pinned_pos IS NOT NULL, read_outbox_max"

func scanTopic(row pgx.Row) (Topic, error) {
	var t Topic
	err := row.Scan(&t.ID, &t.Title, &t.TitleMissing, &t.IconColor, &t.IconEmojiID, &t.Creator, &t.Date, &t.Closed, &t.Hidden, &t.Pinned, &t.ReadOutboxMax)
	return t, err
}

func lockTopic(ctx context.Context, tx pgx.Tx, channel int64, id int) (Topic, error) {
	t, err := scanTopic(tx.QueryRow(ctx, "SELECT "+topicCols+" FROM channel_topics WHERE channel_id=$1 AND id=$2 FOR UPDATE", channel, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, errTopicInvalid
	}
	return t, err
}

// TopicEdit is what editForumTopic changes; nil leaves a field alone.
type TopicEdit struct {
	Title       *string
	IconEmojiID *int64
	Closed      *bool
	Hidden      *bool
}

// EditTopic changes a topic and says what changed in it. Its creator may
// rename, re-icon and close it; the rest is for topic managers. Only
// General can be hidden, and it has no icon.
func (s *Service) EditTopic(ctx context.Context, actor, channel int64, id int, e TopicEdit) ([]Event, error) {
	var events []Event
	err := s.tx(ctx, func(tx pgx.Tx) error {
		c, err := lock(ctx, tx, channel)
		if err != nil {
			return err
		}
		if !c.Forum {
			return errNoForum
		}
		me, err := member(ctx, tx, channel, actor)
		if err != nil {
			return err
		}
		t, err := lockTopic(ctx, tx, channel, id)
		if err != nil {
			return err
		}
		manager := c.managesTopics(me)
		if !InChat(me.Status) || !manager && (t.Creator != actor || e.Hidden != nil || id == GeneralTopic) {
			return errAdminRequire
		}
		action := &tg.MessageActionTopicEdit{}
		changed := false
		if e.Title != nil {
			title, err := checkTopicTitle(*e.Title, false)
			if err != nil {
				return err
			}
			if title != t.Title {
				t.Title, t.TitleMissing = title, false
				action.SetTitle(title)
				changed = true
			}
		}
		if e.IconEmojiID != nil && id != GeneralTopic && *e.IconEmojiID != t.IconEmojiID {
			t.IconEmojiID = *e.IconEmojiID
			action.SetIconEmojiID(t.IconEmojiID) // 0 takes the icon away
			changed = true
		}
		if e.Closed != nil && *e.Closed != t.Closed {
			t.Closed = *e.Closed
			action.SetClosed(t.Closed)
			changed = true
		}
		if e.Hidden != nil && *e.Hidden != t.Hidden {
			if id != GeneralTopic {
				return errTopicInvalid
			}
			t.Hidden = *e.Hidden
			action.SetHidden(t.Hidden)
			changed = true
		}
		if !changed {
			return tgerr.New(400, "TOPIC_NOT_MODIFIED")
		}
		if _, err := tx.Exec(ctx, "UPDATE channel_topics SET title=$3, title_missing=$4, icon_emoji_id=$5, closed=$6, hidden=$7 WHERE channel_id=$1 AND id=$2",
			channel, id, t.Title, t.TitleMissing, t.IconEmojiID, t.Closed, t.Hidden); err != nil {
			return err
		}
		in := id
		if id == GeneralTopic {
			in = 0
		}
		_, u, err := s.addMessage(ctx, tx, Msg{Channel: channel, From: actor, Action: action, Topic: in}, nil)
		if err != nil {
			return err
		}
		ev, err := toMembers(ctx, tx, channel, u)
		events = append(events, ev)
		return err
	})
	return events, err
}

// PinTopic pins or unpins a topic on top of the list; the newest pin goes
// first.
func (s *Service) PinTopic(ctx context.Context, actor, channel int64, id int, pinned bool) ([]Event, error) {
	var events []Event
	err := s.tx(ctx, func(tx pgx.Tx) error {
		c, err := s.topicManager(ctx, tx, actor, channel)
		if err != nil {
			return err
		}
		t, err := lockTopic(ctx, tx, channel, id)
		if err != nil {
			return err
		}
		if t.Pinned == pinned {
			return tgerr.New(400, "CHAT_NOT_MODIFIED")
		}
		if pinned {
			var n int
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM channel_topics WHERE channel_id=$1 AND pinned_pos IS NOT NULL", channel).Scan(&n); err != nil {
				return err
			}
			if n >= maxPinnedTopics {
				return tgerr.New(400, "PINNED_TOO_MUCH")
			}
			_, err = tx.Exec(ctx, `UPDATE channel_topics SET pinned_pos = (SELECT coalesce(max(pinned_pos), 0) + 1 FROM channel_topics WHERE channel_id=$1)
				WHERE channel_id=$1 AND id=$2`, channel, id)
		} else {
			_, err = tx.Exec(ctx, "UPDATE channel_topics SET pinned_pos = NULL WHERE channel_id=$1 AND id=$2", channel, id)
		}
		if err != nil {
			return err
		}
		u := &tg.UpdatePinnedForumTopic{Peer: &tg.PeerChannel{ChannelID: c.ID}, TopicID: id, Pinned: pinned}
		ev, err := toMembers(ctx, tx, channel, u)
		events = append(events, ev)
		return err
	})
	return events, err
}

// ReorderPinnedTopics sets the pinned order. With force the list is the
// whole pinned set (pins the new, unpins the missing); without it only
// topics already pinned move.
func (s *Service) ReorderPinnedTopics(ctx context.Context, actor, channel int64, order []int, force bool) ([]Event, error) {
	var events []Event
	if len(order) > maxPinnedTopics && force {
		return nil, tgerr.New(400, "PINNED_TOO_MUCH")
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		c, err := s.topicManager(ctx, tx, actor, channel)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT id FROM channel_topics WHERE channel_id=$1 AND pinned_pos IS NOT NULL ORDER BY pinned_pos DESC", channel)
		if err != nil {
			return err
		}
		was, err := pgx.CollectRows(rows, pgx.RowTo[int])
		if err != nil {
			return err
		}
		var final []int
		seen := map[int]bool{}
		for _, id := range order {
			if seen[id] || !force && !slices.Contains(was, id) {
				continue
			}
			seen[id] = true
			final = append(final, id)
		}
		if !force {
			// the ones left out keep their place behind
			for _, id := range was {
				if !seen[id] {
					final = append(final, id)
				}
			}
		}
		if _, err := tx.Exec(ctx, "UPDATE channel_topics SET pinned_pos = NULL WHERE channel_id=$1 AND pinned_pos IS NOT NULL", channel); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE channel_topics t SET pinned_pos = $3 - o.n FROM unnest($2::int[]) WITH ORDINALITY AS o(id, n)
			WHERE t.channel_id=$1 AND t.id = o.id`, channel, final, len(final)+1)
		if err != nil {
			return err
		}
		if int(tag.RowsAffected()) != len(final) {
			return errTopicInvalid
		}
		u := &tg.UpdatePinnedForumTopics{Peer: &tg.PeerChannel{ChannelID: c.ID}}
		u.SetOrder(append([]int{}, final...))
		ev, err := toMembers(ctx, tx, channel, u)
		events = append(events, ev)
		return err
	})
	return events, err
}

func (s *Service) topicManager(ctx context.Context, tx pgx.Tx, actor, channel int64) (Channel, error) {
	c, err := lock(ctx, tx, channel)
	if err != nil {
		return c, err
	}
	if !c.Forum {
		return c, errNoForum
	}
	me, err := member(ctx, tx, channel, actor)
	if err != nil {
		return c, err
	}
	if !c.managesTopics(me) {
		return c, errAdminRequire
	}
	return c, nil
}

// deleteBatch caps one deleteTopicHistory call; the client calls again
// while the answer's offset says there's more.
const deleteBatch = 1000

// DeleteTopic removes a topic's messages, newest first, and the topic with
// the last of them. General can't go.
func (s *Service) DeleteTopic(ctx context.Context, actor, channel int64, id int) (tg.MessagesAffectedHistory, []Event, error) {
	var (
		res    tg.MessagesAffectedHistory
		events []Event
	)
	if id == GeneralTopic {
		return res, nil, errTopicInvalid
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		c, err := lock(ctx, tx, channel)
		if err != nil {
			return err
		}
		if !c.Forum {
			return errNoForum
		}
		me, err := member(ctx, tx, channel, actor)
		if err != nil {
			return err
		}
		if !InChat(me.Status) || !c.can(me, "delete") || me.Status != Creator && me.Status != Admin {
			return errAdminRequire
		}
		if _, err := lockTopic(ctx, tx, channel, id); err != nil {
			return err
		}
		res.Pts = c.Pts
		rows, err := tx.Query(ctx, "SELECT id FROM channel_messages WHERE channel_id=$1 AND topic_id=$2 ORDER BY id DESC LIMIT $3", channel, id, deleteBatch+1)
		if err != nil {
			return err
		}
		gone, err := pgx.CollectRows(rows, pgx.RowTo[int])
		if err != nil {
			return err
		}
		if len(gone) > deleteBatch {
			gone, res.Offset = gone[:deleteBatch], 1
		}
		if len(gone) > 0 {
			u, err := s.removeMessages(ctx, tx, c, gone)
			if err != nil {
				return err
			}
			d := u.(*tg.UpdateDeleteChannelMessages)
			res.Pts, res.PtsCount = d.Pts, d.PtsCount
			ev, err := toMembers(ctx, tx, channel, u)
			if err != nil {
				return err
			}
			events = append(events, ev)
		}
		if res.Offset == 0 {
			_, err = tx.Exec(ctx, "DELETE FROM channel_topics WHERE channel_id=$1 AND id=$2", channel, id)
		}
		return err
	})
	return res, events, err
}

// TopicQuery is getForumTopics' paging: after OffsetTopic in the list's
// order, titles containing Q.
type TopicQuery struct {
	Q           string
	OffsetTopic int
	Limit       int
}

// Topics lists a forum's topics as viewer sees them: pinned first, then by
// latest activity. total counts the topics matching Q except General,
// which Telegram's count leaves out (Web A adds it back).
func (s *Service) Topics(ctx context.Context, viewer, channel int64, q TopicQuery) ([]Topic, int, error) {
	all, err := s.topics(ctx, viewer, channel, nil)
	if err != nil {
		return nil, 0, err
	}
	if q.Q = strings.ToLower(strings.TrimSpace(q.Q)); q.Q != "" {
		all = slices.DeleteFunc(all, func(t Topic) bool { return !strings.Contains(strings.ToLower(t.Title), q.Q) })
	}
	slices.SortStableFunc(all, func(a, b Topic) int {
		if a.Pinned != b.Pinned {
			if a.Pinned {
				return -1
			}
			return 1
		}
		return activity(b) - activity(a)
	})
	total := len(all)
	if slices.ContainsFunc(all, func(t Topic) bool { return t.ID == GeneralTopic }) {
		total--
	}
	if q.OffsetTopic != 0 {
		if i := slices.IndexFunc(all, func(t Topic) bool { return t.ID == q.OffsetTopic }); i >= 0 {
			all = all[i+1:]
		}
	}
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, total, nil
}

// activity is the topic's newest message, or its creation for an empty one.
func activity(t Topic) int {
	return max(t.TopID, t.ID)
}

// TopicsByID are the topics asked for that exist, in no particular order.
func (s *Service) TopicsByID(ctx context.Context, viewer, channel int64, list []int) ([]Topic, error) {
	if len(list) == 0 {
		return nil, nil
	}
	return s.topics(ctx, viewer, channel, list)
}

// topics loads a forum's topics (all, or the ones in only) with the
// viewer's read mark, unread count and newest message. General's messages
// are the topic-less ones.
func (s *Service) topics(ctx context.Context, viewer, channel int64, only []int) ([]Topic, error) {
	where := "t.channel_id=$1"
	args := []any{channel, viewer}
	if only != nil {
		where += " AND t.id = ANY($3)"
		args = append(args, only)
	}
	rows, err := s.db.Query(ctx, `WITH mark AS (
			SELECT t.id, greatest(coalesce(m.read_inbox_max, 0), coalesce(r.read_inbox_max, 0)) AS mark
			FROM channel_topics t
			LEFT JOIN channel_members m ON m.channel_id = t.channel_id AND m.user_id = $2
			LEFT JOIN channel_topic_reads r ON r.channel_id = t.channel_id AND r.user_id = $2 AND r.topic_id = t.id
			WHERE `+where+`)
		SELECT `+prefixTopicCols("t.")+`, k.mark,
			coalesce((SELECT max(x.id) FROM channel_messages x WHERE x.channel_id = t.channel_id
				AND (x.topic_id = t.id OR t.id = 1 AND x.topic_id IS NULL)), 0),
			(SELECT count(*) FROM channel_messages x WHERE x.channel_id = t.channel_id
				AND (x.topic_id = t.id OR t.id = 1 AND x.topic_id IS NULL) AND x.id > greatest(k.mark, CASE WHEN t.id = 1 THEN 0 ELSE t.id END)
				AND x.from_id <> $2)
		FROM channel_topics t JOIN mark k ON k.id = t.id
		WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Topic
	for rows.Next() {
		var t Topic
		if err := rows.Scan(&t.ID, &t.Title, &t.TitleMissing, &t.IconColor, &t.IconEmojiID, &t.Creator, &t.Date, &t.Closed, &t.Hidden, &t.Pinned,
			&t.ReadOutboxMax, &t.ReadInboxMax, &t.TopID, &t.Unread); err != nil {
			return nil, err
		}
		// the opening message is never unread, and a mark of 0 draws the
		// "never opened" dot, which stuck on General for its creator
		t.ReadInboxMax = max(t.ReadInboxMax, t.ID)
		out = append(out, t)
	}
	return out, rows.Err()
}

func prefixTopicCols(p string) string {
	cols := strings.Split(topicCols, ", ")
	for i, c := range cols {
		cols[i] = p + c
	}
	return strings.Join(cols, ", ")
}

// TopicRead is what reading a topic produced: the reader's own mark and,
// when it moved the "seen" mark for the writers, that for everyone.
type TopicRead struct {
	Inbox  tg.UpdateClass // nil if nothing moved
	Events []Event
}

// ReadTopic marks a topic read up to maxID for user (messages.readDiscussion
// on a forum).
func (s *Service) ReadTopic(ctx context.Context, user, channel int64, id, maxID int) (TopicRead, error) {
	var out TopicRead
	err := s.tx(ctx, func(tx pgx.Tx) error {
		c, err := lock(ctx, tx, channel)
		if err != nil {
			return err
		}
		if !c.Forum {
			return errNoForum
		}
		m, err := member(ctx, tx, channel, user)
		if err != nil {
			return err
		}
		if !InChat(m.Status) {
			return errUserNotPart
		}
		t, err := lockTopic(ctx, tx, channel, id)
		if err != nil {
			return err
		}
		in := "x.topic_id = $2"
		if id == GeneralTopic {
			in = "x.topic_id IS NULL AND $2 = 1"
		}
		var top int
		if err := tx.QueryRow(ctx, "SELECT coalesce(max(x.id), 0) FROM channel_messages x WHERE x.channel_id=$1 AND "+in, channel, id).Scan(&top); err != nil {
			return err
		}
		if maxID <= 0 || maxID > top {
			maxID = top
		}
		tag, err := tx.Exec(ctx, `INSERT INTO channel_topic_reads(channel_id, user_id, topic_id, read_inbox_max) VALUES ($1, $2, $3, $4)
			ON CONFLICT (channel_id, user_id, topic_id) DO UPDATE SET read_inbox_max = excluded.read_inbox_max
			WHERE channel_topic_reads.read_inbox_max < excluded.read_inbox_max`, channel, user, id, maxID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 || maxID <= m.ReadInboxMax {
			return nil
		}
		out.Inbox = &tg.UpdateReadChannelDiscussionInbox{ChannelID: channel, TopMsgID: id, ReadMaxID: maxID}
		if err := s.noteRead(ctx, tx, c, user, maxID); err != nil {
			return err
		}
		seen, err := seenUpTo(ctx, tx, channel, user, maxID)
		if err != nil {
			return err
		}
		if seen > t.ReadOutboxMax {
			if _, err := tx.Exec(ctx, "UPDATE channel_topics SET read_outbox_max=$3 WHERE channel_id=$1 AND id=$2", channel, id, seen); err != nil {
				return err
			}
			ev, err := toMembers(ctx, tx, channel, &tg.UpdateReadChannelDiscussionOutbox{ChannelID: channel, TopMsgID: id, ReadMaxID: seen})
			if err != nil {
				return err
			}
			out.Events = append(out.Events, ev)
		}
		return nil
	})
	return out, err
}
