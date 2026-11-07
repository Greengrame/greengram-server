package channels

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/messages"
)

const inChatSQL = "('creator', 'admin', 'member', 'restricted')"

const chanCols = `c.id, c.title, c.about, coalesce(c.username, ''), c.megagroup, c.signatures, c.creator_id, c.created_at,
	c.default_banned, coalesce(c.linked_id, 0), c.pts, c.top_id, c.top_date, c.read_outbox_max, c.deleted,
	(SELECT count(*) FROM channel_members m WHERE m.channel_id = c.id AND m.status IN ` + inChatSQL + `), coalesce(c.photo_id, 0), c.forum, c.forum_tabs`

func scanChannel(row pgx.Row) (Channel, error) {
	var c Channel
	var banned []byte
	err := row.Scan(&c.ID, &c.Title, &c.About, &c.Username, &c.Megagroup, &c.Signatures, &c.Creator, &c.Created,
		&banned, &c.LinkedID, &c.Pts, &c.TopID, &c.TopDate, &c.ReadOutboxMax, &c.Deleted, &c.Count, &c.PhotoID, &c.Forum, &c.ForumTabs)
	c.DefaultBanned = decodeBanned(banned)
	return c, err
}

func (s *Service) Get(ctx context.Context, id int64) (Channel, bool, error) {
	c, err := scanChannel(s.db.QueryRow(ctx, "SELECT "+chanCols+" FROM channels c WHERE c.id=$1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}

func (s *Service) GetMany(ctx context.Context, list []int64) (map[int64]Channel, error) {
	rows, err := s.db.Query(ctx, "SELECT "+chanCols+" FROM channels c WHERE c.id = ANY($1)", list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out[c.ID] = c
	}
	return out, rows.Err()
}

func (s *Service) ByUsername(ctx context.Context, name string) (Channel, bool, error) {
	c, err := scanChannel(s.db.QueryRow(ctx, "SELECT "+chanCols+" FROM channels c WHERE lower(c.username)=lower($1) AND NOT c.deleted", name))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}

const memberCols = "user_id, status, admin_rights, rank, banned_rights, coalesce(inviter_id, 0), coalesce(promoted_by, 0), date, read_inbox_max, pinned_at IS NOT NULL, forum_as_messages"

func scanMember(row pgx.Row) (Membership, error) {
	var m Membership
	var admin, banned []byte
	err := row.Scan(&m.UserID, &m.Status, &admin, &m.Rank, &banned, &m.Inviter, &m.PromotedBy, &m.Date, &m.ReadInboxMax, &m.Pinned, &m.ForumAsMessages)
	m.AdminRights, m.BannedRights = decodeAdmin(admin), decodeBanned(banned)
	return m, err
}

func (s *Service) Member(ctx context.Context, channel, user int64) (Membership, error) {
	return member(ctx, s.db, channel, user)
}

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func member(ctx context.Context, q querier, channel, user int64) (Membership, error) {
	m, err := scanMember(q.QueryRow(ctx, "SELECT "+memberCols+" FROM channel_members WHERE channel_id=$1 AND user_id=$2", channel, user))
	if errors.Is(err, pgx.ErrNoRows) {
		n := None
		n.UserID = user
		return n, nil
	}
	return m, err
}

// Access loads a channel and the user's place in it. Reading needs to be in
// the chat, or a public channel.
func (s *Service) Access(ctx context.Context, channel, user int64) (Channel, Membership, error) {
	c, ok, err := s.Get(ctx, channel)
	if err != nil {
		return c, None, err
	}
	if !ok || c.Deleted {
		return c, None, errInvalid
	}
	m, err := s.Member(ctx, channel, user)
	if err != nil {
		return c, m, err
	}
	if m.Status == Kicked || (!InChat(m.Status) && c.Username == "") {
		return c, m, errPrivate
	}
	return c, m, nil
}

// InAny tells whether user is in any of the chats, for privacy exceptions.
func (s *Service) InAny(ctx context.Context, user int64, list []int64) (bool, error) {
	if len(list) == 0 {
		return false, nil
	}
	var ok bool
	err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM channel_members WHERE user_id=$1 AND channel_id = ANY($2) AND status IN "+inChatSQL+")",
		user, list).Scan(&ok)
	return ok, err
}

// InWhich are the chats among list that user is in.
func (s *Service) InWhich(ctx context.Context, user int64, list []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(list) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, "SELECT channel_id FROM channel_members WHERE user_id=$1 AND channel_id = ANY($2) AND status IN "+inChatSQL, user, list)
	if err != nil {
		return nil, err
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	for _, id := range got {
		out[id] = true
	}
	return out, err
}

// MemberOfMany is Member for one user across channels; None where they
// never joined.
func (s *Service) MemberOfMany(ctx context.Context, user int64, list []int64) (map[int64]Membership, error) {
	out := map[int64]Membership{}
	if len(list) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, "SELECT channel_id, "+memberCols+" FROM channel_members WHERE user_id=$1 AND channel_id = ANY($2)", user, list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			m             Membership
			ch            int64
			admin, banned []byte
		)
		if err := rows.Scan(&ch, &m.UserID, &m.Status, &admin, &m.Rank, &banned, &m.Inviter, &m.PromotedBy, &m.Date, &m.ReadInboxMax, &m.Pinned, &m.ForumAsMessages); err != nil {
			return nil, err
		}
		m.AdminRights, m.BannedRights = decodeAdmin(admin), decodeBanned(banned)
		out[ch] = m
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range list {
		if _, ok := out[id]; !ok {
			n := None
			n.UserID = user
			out[id] = n
		}
	}
	return out, nil
}

// OpenMany is Open for many channels with one query.
func (s *Service) OpenMany(ctx context.Context, list []Channel) (map[int64]bool, error) {
	out := map[int64]bool{}
	var linked []int64
	for _, c := range list {
		if c.Username != "" {
			out[c.ID] = true
		} else if c.Megagroup && c.LinkedID != 0 {
			linked = append(linked, c.LinkedID)
		}
	}
	if len(linked) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, "SELECT id FROM channels WHERE id = ANY($1) AND username IS NOT NULL", linked)
	if err != nil {
		return nil, err
	}
	public, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	pub := map[int64]bool{}
	for _, id := range public {
		pub[id] = true
	}
	for _, c := range list {
		if c.Username == "" && c.Megagroup && pub[c.LinkedID] {
			out[c.ID] = true
		}
	}
	return out, nil
}

// MemberIDs are everyone in the chat, for delivering its updates.
func (s *Service) MemberIDs(ctx context.Context, channel int64) ([]int64, error) {
	return memberIDs(ctx, s.db, channel)
}

func memberIDs(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, channel int64) ([]int64, error) {
	rows, err := q.Query(ctx, "SELECT user_id FROM channel_members WHERE channel_id=$1 AND status IN "+inChatSQL, channel)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// Members lists participants: filter is recent, admins, kicked, banned,
// search (q) or contacts-less "recent" for anything else.
func (s *Service) Members(ctx context.Context, channel int64, filter, q string, offset, limit int) ([]Membership, int, error) {
	limit = min(max(limit, 1), 200)
	where := "m.channel_id=$1 AND m.status IN " + inChatSQL
	args := []any{channel}
	switch filter {
	case "admins":
		where = "m.channel_id=$1 AND m.status IN ('creator', 'admin')"
	case "kicked":
		where = "m.channel_id=$1 AND m.status = 'kicked'"
	case "banned":
		where = "m.channel_id=$1 AND m.status = 'restricted'"
	}
	if q = strings.ToLower(strings.TrimSpace(q)); q != "" {
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q)+"%")
		where += " AND EXISTS(SELECT 1 FROM users u WHERE u.id = m.user_id AND (lower(u.username) LIKE $2 OR lower(u.name) LIKE $2))"
	}
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM channel_members m WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	n := len(args)
	args = append(args, offset, limit)
	rows, err := s.db.Query(ctx, "SELECT "+prefixCols("m.")+" FROM channel_members m WHERE "+where+
		" ORDER BY (m.status = 'creator') DESC, (m.status = 'admin') DESC, m.date DESC OFFSET $"+itoa(n+1)+" LIMIT $"+itoa(n+2), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Membership
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	return out, total, rows.Err()
}

func prefixCols(p string) string {
	return p + "user_id, " + p + "status, " + p + "admin_rights, " + p + "rank, " + p + "banned_rights, coalesce(" + p + "inviter_id, 0), coalesce(" +
		p + "promoted_by, 0), " + p + "date, " + p + "read_inbox_max, " + p + "pinned_at IS NOT NULL, " + p + "forum_as_messages"
}

// lock takes the channel row for a write and reloads it.
func lock(ctx context.Context, tx pgx.Tx, channel int64) (Channel, error) {
	c, err := scanChannel(tx.QueryRow(ctx, "SELECT "+chanCols+" FROM channels c WHERE c.id=$1 FOR UPDATE", channel))
	if errors.Is(err, pgx.ErrNoRows) || err == nil && c.Deleted {
		return c, errInvalid
	}
	return c, err
}

// journal moves the channel's pts and stores the update.
func (s *Service) journal(ctx context.Context, tx pgx.Tx, channel int64, count int, build func(pts int) tg.UpdateClass) (tg.UpdateClass, error) {
	var pts int
	if err := tx.QueryRow(ctx, "UPDATE channels SET pts = pts + $2 WHERE id=$1 RETURNING pts", channel, count).Scan(&pts); err != nil {
		return nil, err
	}
	u := build(pts)
	var b bin.Buffer
	if err := u.Encode(&b); err != nil {
		return nil, err
	}
	_, err := tx.Exec(ctx, "INSERT INTO channel_journal(channel_id, pts, pts_count, date, body) VALUES ($1, $2, $3, $4, $5)", channel, pts, count, s.unix(), b.Buf)
	return u, err
}

// addMessage stores m (ID and Date filled in here) and journals it.
func (s *Service) addMessage(ctx context.Context, tx pgx.Tx, m Msg, random *int64) (Msg, tg.UpdateClass, error) {
	if err := tx.QueryRow(ctx, "UPDATE channels SET next_id = next_id + 1 WHERE id=$1 RETURNING next_id - 1", m.Channel).Scan(&m.ID); err != nil {
		return m, nil, err
	}
	// a topic is named after the message that opens it
	if _, ok := m.Action.(*tg.MessageActionTopicCreate); ok {
		m.Topic = m.ID
	}
	m.Date = s.unix()
	ent, err := encodeEntities(m.Entities)
	if err != nil {
		return m, nil, err
	}
	var action []byte
	if m.Action != nil {
		action = encodeTL(m.Action)
	}
	mediaTL, err := m.Media.Encode()
	if err != nil {
		return m, nil, err
	}
	markup, err := messages.EncodeMarkup(m.ReplyMarkup)
	if err != nil {
		return m, nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO channel_messages(channel_id, id, from_id, date, post, text, entities, action, reply_to,
			fwd_from_id, fwd_date, random_id, fwd_channel_id, fwd_channel_msg, media, media_kind, media_id, grouped_id, reply_markup,
			fwd_from_chan, fwd_from_post, fwd_from_name, topic_id, silent, rich, via_bot_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, nullif($9, 0), nullif($10, 0), nullif($11, 0), $12, nullif($13, 0), nullif($14, 0),
			$15, nullif($16, ''), nullif($17::bigint, 0), nullif($18::bigint, 0), $19, nullif($20::bigint, 0), nullif($21::int, 0), nullif($22, ''),
			nullif($23::int, 0), $24, $25, nullif($26::bigint, 0))`,
		m.Channel, m.ID, m.From, m.Date, m.Post, m.Text, ent, action, m.ReplyTo, m.Fwd.User, m.Fwd.Date, random, m.FwdChanID, m.FwdChanMs,
		mediaTL, m.Media.Kind, m.Media.ID, m.Grouped, markup, m.Fwd.Chan, m.Fwd.Post, m.Fwd.Name, m.Topic, m.Silent, m.Rich, m.ViaBot)
	if err != nil {
		return m, nil, err
	}
	if _, err := tx.Exec(ctx, "UPDATE channels SET top_id=$2, top_date=$3 WHERE id=$1", m.Channel, m.ID, m.Date); err != nil {
		return m, nil, err
	}
	u, err := s.journal(ctx, tx, m.Channel, 1, func(pts int) tg.UpdateClass {
		return &tg.UpdateNewChannelMessage{Message: m.TL(), Pts: pts, PtsCount: 1}
	})
	return m, u, err
}

// service posts a service message in a group; broadcast channels skip
// joins and leaves like Telegram does.
func (s *Service) service(ctx context.Context, tx pgx.Tx, c Channel, actor int64, action tg.MessageActionClass) (tg.UpdateClass, error) {
	_, u, err := s.addMessage(ctx, tx, Msg{Channel: c.ID, From: actor, Action: action}, nil)
	return u, err
}

// PostService posts a service message from outside the channel's own actions
// (a giveaway's results) and says who to tell.
func (s *Service) PostService(ctx context.Context, channel, actor int64, action tg.MessageActionClass) ([]Event, error) {
	var out []Event
	err := s.tx(ctx, func(tx pgx.Tx) error {
		u, err := s.service(ctx, tx, Channel{ID: channel}, actor, action)
		if err != nil {
			return err
		}
		e, err := toMembers(ctx, tx, channel, u)
		out = []Event{e}
		return err
	})
	return out, err
}

// PostAsChannel is PostService for a post of the channel's own: in a
// broadcast channel it comes from the channel, not from actor. A channel's
// gifts are announced this way.
func (s *Service) PostAsChannel(ctx context.Context, channel, actor int64, action tg.MessageActionClass) (Msg, []Event, error) {
	var (
		msg Msg
		out []Event
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		m, e, err := s.PostTx(ctx, tx, channel, actor, action, media.Ref{}, 0)
		msg, out = m, []Event{e}
		return err
	})
	return msg, out, err
}

// ChangeAction rewrites a service message's action (a gift post that got
// upgraded or put on the profile) and says who to tell. change returns
// false when there's nothing to change.
func (s *Service) ChangeAction(ctx context.Context, channel int64, id int, change func(tg.MessageActionClass) bool) ([]Event, error) {
	var out []Event
	err := s.tx(ctx, func(tx pgx.Tx) error {
		m, err := scanMsg(tx.QueryRow(ctx, "SELECT "+msgCols+" FROM channel_messages WHERE channel_id=$1 AND id=$2 AND action IS NOT NULL FOR UPDATE", channel, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return tgerr.New(400, "MESSAGE_ID_INVALID")
		}
		if err != nil {
			return err
		}
		if !change(m.Action) {
			return nil
		}
		if _, err := tx.Exec(ctx, "UPDATE channel_messages SET action=$3 WHERE channel_id=$1 AND id=$2", channel, id, encodeTL(m.Action)); err != nil {
			return err
		}
		u, err := s.journal(ctx, tx, channel, 1, func(pts int) tg.UpdateClass {
			return &tg.UpdateEditChannelMessage{Message: m.TL(), Pts: pts, PtsCount: 1}
		})
		if err != nil {
			return err
		}
		e, err := toMembers(ctx, tx, channel, u)
		out = []Event{e}
		return err
	})
	return out, err
}

// PostTx posts a service message (action) or a message with media in the
// caller's transaction, as the channel in a broadcast one and as from in a
// group. A giveaway's messages go out this way, with its money.
func (s *Service) PostTx(ctx context.Context, tx pgx.Tx, channel, from int64, action tg.MessageActionClass, m media.Ref, replyTo int) (Msg, Event, error) {
	var megagroup bool
	if err := tx.QueryRow(ctx, "SELECT megagroup FROM channels WHERE id=$1 AND NOT deleted", channel).Scan(&megagroup); err != nil {
		return Msg{}, Event{}, err
	}
	msg, u, err := s.addMessage(ctx, tx, Msg{Channel: channel, From: from, Action: action, Media: m, Post: !megagroup, ReplyTo: replyTo}, nil)
	if err != nil {
		return msg, Event{}, err
	}
	e, err := toMembers(ctx, tx, channel, u)
	return msg, e, err
}

// toMembers is an Event for everyone in the chat right now (inside tx).
func toMembers(ctx context.Context, tx pgx.Tx, channel int64, u tg.UpdateClass, plus ...int64) (Event, error) {
	list, err := memberIDs(ctx, tx, channel)
	if err != nil {
		return Event{}, err
	}
	return Event{Users: union(list, plus), Update: u}, nil
}

func union(a, b []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, l := range [][]int64{a, b} {
		for _, v := range l {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

func updChannel(id int64) tg.UpdateClass { return &tg.UpdateChannel{ChannelID: id} }

var titleRe = regexp.MustCompile(`\S`)

func checkTitle(t string) error {
	if !titleRe.MatchString(t) || utf8.RuneCountInString(t) > 128 {
		return tgerr.New(400, "CHAT_TITLE_EMPTY")
	}
	return nil
}

// Create makes a channel (megagroup=false) or a group with creator in it.
func (s *Service) Create(ctx context.Context, creator int64, title, about string, megagroup bool) (Channel, []Event, error) {
	var (
		c      Channel
		events []Event
	)
	if err := checkTitle(title); err != nil {
		return c, nil, err
	}
	if utf8.RuneCountInString(about) > 255 {
		return c, nil, tgerr.New(400, "CHAT_ABOUT_TOO_LONG")
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var id int64
		now := s.unix()
		if err := tx.QueryRow(ctx, `INSERT INTO channels(title, about, megagroup, creator_id, created_at, default_banned)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, strings.TrimSpace(title), about, megagroup, creator, now, encodeTL(&tg.ChatBannedRights{})).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO channel_members(channel_id, user_id, status, admin_rights, date) VALUES ($1, $2, 'creator', $3, $4)`,
			id, creator, encodeTL(ptr(fullAdmin())), now); err != nil {
			return err
		}
		var err error
		if c, err = lock(ctx, tx, id); err != nil {
			return err
		}
		u, err := s.service(ctx, tx, c, creator, &tg.MessageActionChannelCreate{Title: c.Title})
		if err != nil {
			return err
		}
		events = append(events, Event{Users: []int64{creator}, Update: updChannel(id)}, Event{Users: []int64{creator}, Update: u})
		c, err = lock(ctx, tx, id)
		return err
	})
	return c, events, err
}

func ptr[T any](v T) *T { return &v }

// joinSQL makes ($2) a member; the history from before ($5 = top id) isn't
// unread for them, like Telegram.
const joinSQL = `INSERT INTO channel_members(channel_id, user_id, status, inviter_id, date, read_inbox_max) VALUES ($1, $2, 'member', $3, $4, $5)
	ON CONFLICT (channel_id, user_id) DO UPDATE SET status='member', inviter_id=$3, date=$4, banned_rights=NULL, admin_rights=NULL,
		read_inbox_max=greatest(channel_members.read_inbox_max, $5)`

// creatorSQL is joinSQL for the creator coming back: it's still theirs.
const creatorSQL = `INSERT INTO channel_members(channel_id, user_id, status, admin_rights, inviter_id, date, read_inbox_max) VALUES ($1, $2, 'creator', $6, $3, $4, $5)
	ON CONFLICT (channel_id, user_id) DO UPDATE SET status='creator', admin_rights=$6, inviter_id=$3, date=$4, banned_rights=NULL,
		read_inbox_max=greatest(channel_members.read_inbox_max, $5)`

// enter makes user a member of c (inside tx); inviter may be nil.
func (s *Service) enter(ctx context.Context, tx pgx.Tx, c Channel, user int64, inviter any) error {
	var err error
	if user == c.Creator {
		_, err = tx.Exec(ctx, creatorSQL, c.ID, user, inviter, s.unix(), c.TopID, encodeTL(ptr(fullAdmin())))
	} else {
		_, err = tx.Exec(ctx, joinSQL, c.ID, user, inviter, s.unix(), c.TopID)
	}
	return err
}

// Invite adds users to the chat. Returns who couldn't be added.
func (s *Service) Invite(ctx context.Context, actor, channel int64, users []int64) ([]Event, []int64, error) {
	var (
		events  []Event
		missing []int64
	)
	err := s.tx(ctx, func(tx pgx.Tx) error {
		c, err := lock(ctx, tx, channel)
		if err != nil {
			return err
		}
		me, err := member(ctx, tx, channel, actor)
		if err != nil {
			return err
		}
		if !c.can(me, "invite") {
			return errAdminRequire
		}
		var added []int64
		for _, u := range users {
			m, err := member(ctx, tx, channel, u)
			if err != nil {
				return err
			}
			if InChat(m.Status) {
				continue
			}
			if m.Status == Kicked && !c.can(me, "ban") {
				missing = append(missing, u)
				continue
			}
			if err := s.enter(ctx, tx, c, u, actor); err != nil {
				return err
			}
			added = append(added, u)
		}
		if len(added) == 0 {
			return nil
		}
		events = append(events, Event{Users: added, Update: updChannel(channel)})
		if c.Megagroup {
			cl := make([]int64, len(added))
			for i, u := range added {
				cl[i] = ids.Client(u)
			}
			u, err := s.service(ctx, tx, c, actor, &tg.MessageActionChatAddUser{Users: cl})
			if err != nil {
				return err
			}
			ev, err := toMembers(ctx, tx, channel, u)
			if err != nil {
				return err
			}
			events = append(events, ev)
		}
		return nil
	})
	return events, missing, err
}

// Join puts user in the chat: a public channel, or through an invite link
// made by inviter (0 = public join).
func (s *Service) Join(ctx context.Context, user, channel, inviter int64) ([]Event, error) {
	var events []Event
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		events, err = s.join(ctx, tx, user, channel, inviter)
		return err
	})
	return events, err
}

func (s *Service) join(ctx context.Context, tx pgx.Tx, user, channel, inviter int64) ([]Event, error) {
	c, err := lock(ctx, tx, channel)
	if err != nil {
		return nil, err
	}
	m, err := member(ctx, tx, channel, user)
	if err != nil {
		return nil, err
	}
	switch {
	case InChat(m.Status):
		return nil, tgerr.New(400, "USER_ALREADY_PARTICIPANT")
	case m.Status == Kicked:
		return nil, errPrivate
	case user == c.Creator:
		// a creator who left may come back, private chat or not
	case inviter == 0 && c.Username == "" && !s.isDiscussionOfPublic(ctx, tx, c):
		return nil, errPrivate
	}
	var inv *int64
	if inviter != 0 {
		inv = &inviter
	}
	if err := s.enter(ctx, tx, c, user, inv); err != nil {
		return nil, err
	}
	events := []Event{{Users: []int64{user}, Update: updChannel(channel)}}
	if c.Megagroup {
		var action tg.MessageActionClass = &tg.MessageActionChatAddUser{Users: []int64{ids.Client(user)}}
		if inviter != 0 {
			action = &tg.MessageActionChatJoinedByLink{InviterID: ids.Client(inviter)}
		}
		u, err := s.service(ctx, tx, c, user, action)
		if err != nil {
			return nil, err
		}
		ev, err := toMembers(ctx, tx, channel, u)
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, nil
}

// a discussion group of a public channel is joinable from the channel
func (s *Service) isDiscussionOfPublic(ctx context.Context, q querier, c Channel) bool {
	if !c.Megagroup || c.LinkedID == 0 {
		return false
	}
	var public bool
	q.QueryRow(ctx, "SELECT username IS NOT NULL FROM channels WHERE id=$1", c.LinkedID).Scan(&public)
	return public
}

// Open is whether people outside the chat may see it: public, or the
// discussion group of a public channel.
func (s *Service) Open(ctx context.Context, c Channel) bool {
	return c.Username != "" || s.isDiscussionOfPublic(ctx, s.db, c)
}

func (s *Service) Leave(ctx context.Context, user, channel int64) ([]Event, error) {
	var events []Event
	err := s.tx(ctx, func(tx pgx.Tx) error {
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
		if _, err := tx.Exec(ctx, "UPDATE channel_members SET status='left', admin_rights=NULL WHERE channel_id=$1 AND user_id=$2", channel, user); err != nil {
			return err
		}
		events = append(events, Event{Users: []int64{user}, Update: updChannel(channel)})
		if c.Megagroup {
			u, err := s.service(ctx, tx, c, user, &tg.MessageActionChatDeleteUser{UserID: ids.Client(user)})
			if err != nil {
				return err
			}
			ev, err := toMembers(ctx, tx, channel, u, user)
			if err != nil {
				return err
			}
			events = append(events, ev)
		}
		return nil
	})
	return events, err
}

func emptyAdmin(r tg.ChatAdminRights) bool {
	r.Flags = 0
	return r == tg.ChatAdminRights{}
}

func emptyBanned(r tg.ChatBannedRights) bool {
	r.Flags, r.UntilDate = 0, 0
	return r == tg.ChatBannedRights{}
}

// EditAdmin promotes (or with empty rights, demotes) a member.
func (s *Service) EditAdmin(ctx context.Context, actor, channel, target int64, rights tg.ChatAdminRights, rank string) ([]Event, error) {
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
		if !c.can(me, "add_admins") {
			return errAdminRequire
		}
		t, err := member(ctx, tx, channel, target)
		if err != nil {
			return err
		}
		if t.Status == Creator {
			return tgerr.New(400, "USER_CREATOR")
		}
		if !InChat(t.Status) {
			return errUserNotPart
		}
		// like Telegram: an admin hands on only rights it has, and edits
		// only the admins it promoted
		if me.Status != Creator {
			if t.Status == Admin && t.PromotedBy != actor {
				return errAdminRequire
			}
			mine := me.AdminRights
			mine.SetFlags()
			rights.SetFlags()
			if rights.Flags&^mine.Flags != 0 {
				return tgerr.New(400, "RIGHT_FORBIDDEN")
			}
		}
		if utf8.RuneCountInString(rank) > 16 {
			return tgerr.New(400, "ADMIN_RANK_INVALID")
		}
		if emptyAdmin(rights) {
			_, err = tx.Exec(ctx, "UPDATE channel_members SET status='member', admin_rights=NULL, rank='', promoted_by=NULL WHERE channel_id=$1 AND user_id=$2", channel, target)
		} else {
			_, err = tx.Exec(ctx, "UPDATE channel_members SET status='admin', admin_rights=$3, rank=$4, promoted_by=$5, banned_rights=NULL WHERE channel_id=$1 AND user_id=$2",
				channel, target, encodeTL(&rights), rank, actor)
		}
		if err != nil {
			return err
		}
		events = append(events, Event{Users: []int64{target, actor}, Update: updChannel(channel)})
		return nil
	})
	return events, err
}

// EditBanned restricts, kicks (rights.ViewMessages) or with empty rights
// lifts both.
func (s *Service) EditBanned(ctx context.Context, actor, channel, target int64, rights tg.ChatBannedRights) ([]Event, error) {
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
		if !c.can(me, "ban") {
			return errAdminRequire
		}
		t, err := member(ctx, tx, channel, target)
		if err != nil {
			return err
		}
		if t.Status == Creator || (t.Status == Admin && me.Status != Creator) {
			return tgerr.New(400, "USER_ADMIN_INVALID")
		}
		status := t.Status
		switch {
		case rights.ViewMessages:
			status = Kicked
		case !emptyBanned(rights):
			if !InChat(t.Status) {
				return errUserNotPart
			}
			status = Restricted
		case t.Status == Kicked:
			status = Left
		case t.Status == Restricted:
			status = Member
		}
		var raw []byte
		if !emptyBanned(rights) {
			raw = encodeTL(&rights)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO channel_members(channel_id, user_id, status, banned_rights, date) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (channel_id, user_id) DO UPDATE SET status=$3, banned_rights=$4, admin_rights=NULL`,
			channel, target, status, raw, s.unix()); err != nil {
			return err
		}
		events = append(events, Event{Users: []int64{target}, Update: updChannel(channel)})
		if status == Kicked && InChat(t.Status) && c.Megagroup {
			u, err := s.service(ctx, tx, c, actor, &tg.MessageActionChatDeleteUser{UserID: ids.Client(target)})
			if err != nil {
				return err
			}
			ev, err := toMembers(ctx, tx, channel, u)
			if err != nil {
				return err
			}
			events = append(events, ev)
		}
		return nil
	})
	return events, err
}

// EditPhoto sets the chat photo (photo nil removes it) and says so in the
// chat.
func (s *Service) EditPhoto(ctx context.Context, actor, channel int64, photoID int64, photo tg.PhotoClass) ([]Event, error) {
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
		if !c.can(me, "change_info") {
			return errAdminRequire
		}
		if photoID == c.PhotoID {
			return tgerr.New(400, "CHAT_NOT_MODIFIED")
		}
		var v *int64
		var action tg.MessageActionClass = &tg.MessageActionChatDeletePhoto{}
		if photoID != 0 {
			v, action = &photoID, &tg.MessageActionChatEditPhoto{Photo: photo}
		}
		if _, err := tx.Exec(ctx, "UPDATE channels SET photo_id=$2 WHERE id=$1", channel, v); err != nil {
			return err
		}
		ev, err := toMembers(ctx, tx, channel, updChannel(channel))
		if err != nil {
			return err
		}
		events = append(events, ev)
		u, err := s.service(ctx, tx, c, actor, action)
		if err != nil {
			return err
		}
		ev, err = toMembers(ctx, tx, channel, u)
		events = append(events, ev)
		return err
	})
	return events, err
}

// EditInfo changes title and/or about.
func (s *Service) EditInfo(ctx context.Context, actor, channel int64, title, about *string) ([]Event, error) {
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
		if !c.can(me, "change_info") {
			return errAdminRequire
		}
		if about != nil {
			if utf8.RuneCountInString(*about) > 255 {
				return tgerr.New(400, "CHAT_ABOUT_TOO_LONG")
			}
			if _, err := tx.Exec(ctx, "UPDATE channels SET about=$2 WHERE id=$1", channel, *about); err != nil {
				return err
			}
		}
		ev, err := toMembers(ctx, tx, channel, updChannel(channel))
		if err != nil {
			return err
		}
		events = append(events, ev)
		if title != nil && strings.TrimSpace(*title) != c.Title {
			if err := checkTitle(*title); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "UPDATE channels SET title=$2 WHERE id=$1", channel, strings.TrimSpace(*title)); err != nil {
				return err
			}
			u, err := s.service(ctx, tx, c, actor, &tg.MessageActionChatEditTitle{Title: strings.TrimSpace(*title)})
			if err != nil {
				return err
			}
			ev, err := toMembers(ctx, tx, channel, u)
			if err != nil {
				return err
			}
			events = append(events, ev)
		}
		return nil
	})
	return events, err
}

var usernameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{3,31}$`)

// UsernameFree checks a name against users, channels and Fragment, where
// any listing (sold or not) keeps the name away from channels.
func (s *Service) UsernameFree(ctx context.Context, name string, except int64) (bool, error) {
	return usernameFree(ctx, s.db, name, except)
}

func usernameFree(ctx context.Context, q querier, name string, except int64) (bool, error) {
	if !usernameRe.MatchString(name) {
		return false, tgerr.New(400, "USERNAME_INVALID")
	}
	var taken bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(username)=lower($1))
		OR EXISTS(SELECT 1 FROM channels WHERE lower(username)=lower($1) AND id<>$2)
		OR EXISTS(SELECT 1 FROM fragment_items WHERE kind='username' AND lower(title)='@'||lower($1))`, name, except).Scan(&taken)
	return !taken, err
}

// UsernameFreeFor is UsernameFree, but a collectible actor owns and isn't
// selling can go on actor's channel whatever its shape.
func (s *Service) UsernameFreeFor(ctx context.Context, actor int64, name string, except int64) (bool, error) {
	return usernameFreeFor(ctx, s.db, actor, name, except)
}

// UsernameFreeInTx is UsernameFreeFor inside the caller's transaction, for
// Fragment moving a channel onto a new name as its old one goes up for sale.
func UsernameFreeInTx(ctx context.Context, tx pgx.Tx, actor int64, name string, except int64) (bool, error) {
	return usernameFreeFor(ctx, tx, actor, name, except)
}

func usernameFreeFor(ctx context.Context, q querier, actor int64, name string, except int64) (bool, error) {
	var owned, held bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fragment_items WHERE kind='username' AND lower(title)='@'||lower($1) AND owner_id=$2 AND NOT listed),
		EXISTS(SELECT 1 FROM users WHERE lower(username)=lower($1))
		OR EXISTS(SELECT 1 FROM channels WHERE lower(username)=lower($1) AND id<>$3 AND NOT deleted)
		OR EXISTS(SELECT 1 FROM fragment_items WHERE kind='username' AND lower(title)='@'||lower($1) AND active)`, name, actor, except).Scan(&owned, &held)
	if err != nil {
		return false, err
	}
	if owned {
		return !held, nil
	}
	return usernameFree(ctx, q, name, except)
}

// UsernameChanged is the update telling a channel's members to look again,
// for a change that happened outside this package (a Fragment sale).
func (s *Service) UsernameChanged(ctx context.Context, channel int64) ([]Event, error) {
	var events []Event
	err := s.tx(ctx, func(tx pgx.Tx) error {
		ev, err := toMembers(ctx, tx, channel, updChannel(channel))
		events = append(events, ev)
		return err
	})
	return events, err
}

// SetUsername makes the channel public ("" makes it private again).
func (s *Service) SetUsername(ctx context.Context, actor, channel int64, name string) ([]Event, error) {
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
		if me.Status != Creator {
			return errAdminRequire
		}
		var v *string
		if name != "" {
			free, err := usernameFreeFor(ctx, tx, actor, name, c.ID)
			if err != nil {
				return err
			}
			if !free {
				return tgerr.New(400, "USERNAME_OCCUPIED")
			}
			v = &name
		}
		if _, err := tx.Exec(ctx, "UPDATE channels SET username=$2 WHERE id=$1", channel, v); err != nil {
			return err
		}
		ev, err := toMembers(ctx, tx, channel, updChannel(channel))
		events = append(events, ev)
		return err
	})
	return events, err
}

func (s *Service) SetDefaultBanned(ctx context.Context, actor, channel int64, rights tg.ChatBannedRights) ([]Event, error) {
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
		if !c.Megagroup || !c.can(me, "ban") {
			return errAdminRequire
		}
		if _, err := tx.Exec(ctx, "UPDATE channels SET default_banned=$2 WHERE id=$1", channel, encodeTL(&rights)); err != nil {
			return err
		}
		ev, err := toMembers(ctx, tx, channel, updChannel(channel))
		events = append(events, ev)
		return err
	})
	return events, err
}

// Delete removes the channel for everyone; creator only.
func (s *Service) Delete(ctx context.Context, actor, channel int64) ([]Event, error) {
	var events []Event
	err := s.tx(ctx, func(tx pgx.Tx) error {
		c, err := lock(ctx, tx, channel)
		if err != nil {
			return err
		}
		if c.Creator != actor {
			return errAdminRequire
		}
		ev, err := toMembers(ctx, tx, channel, updChannel(channel))
		if err != nil {
			return err
		}
		events = append(events, ev)
		// two Execs: pgx can't prepare several statements with parameters
		if _, err := tx.Exec(ctx, "UPDATE channels SET deleted=true, username=NULL, linked_id=NULL WHERE id=$1", channel); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE channels SET linked_id=NULL WHERE linked_id=$1", channel)
		return err
	})
	return events, err
}

// PremiumMembers counts the chat's members whose Premium is running.
func (s *Service) PremiumMembers(ctx context.Context, channel int64) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM channel_members m JOIN users u ON u.id = m.user_id
		WHERE m.channel_id=$1 AND m.status IN `+inChatSQL+` AND NOT u.disabled AND u.premium_until > $2 AND u.premium_starts_at <= $2`, channel, s.unix()).Scan(&n)
	return n, err
}
