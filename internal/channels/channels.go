// Package channels is groups and channels. Every group is a supergroup, so
// both are channels: one message list per channel with the channel's ids,
// a per-channel pts journal, members with admin and banned rights, invite
// links, and discussion groups linked to broadcast channels.
package channels

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/messages"
)

type Service struct {
	db  *pgxpool.Pool
	now func() time.Time
}

func New(db *pgxpool.Pool) *Service { return &Service{db: db, now: time.Now} }

func (s *Service) unix() int { return int(s.now().Unix()) }

func (s *Service) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.db, fn)
}

// Event is an update for a set of users. Channel message updates are the
// same for every member except the out flag; Deliver fixes that per viewer.
type Event struct {
	Users  []int64
	Update tg.UpdateClass
	// those of Users a new message mentions (see MarkMentioned)
	Mentions []int64
}

const (
	Creator    = "creator"
	Admin      = "admin"
	Member     = "member"
	Restricted = "restricted" // still in the chat, some rights taken
	Kicked     = "kicked"
	Left       = "left"
)

// InChat is whether a status can read the channel.
func InChat(status string) bool {
	return status == Creator || status == Admin || status == Member || status == Restricted
}

type Channel struct {
	ID            int64
	Title         string
	About         string
	Username      string
	Megagroup     bool
	Signatures    bool
	Creator       int64
	Created       int
	DefaultBanned tg.ChatBannedRights
	LinkedID      int64
	Pts           int
	TopID         int
	TopDate       int
	ReadOutboxMax int
	Deleted       bool
	Count         int   // members in the chat
	PhotoID       int64 // 0 = none
	Forum         bool  // a group with topics
	ForumTabs     bool
}

type Membership struct {
	UserID       int64
	Status       string
	AdminRights  tg.ChatAdminRights
	Rank         string
	BannedRights tg.ChatBannedRights
	Inviter      int64
	PromotedBy   int64
	Date         int
	ReadInboxMax int
	Pinned       bool
	// forum shown as one flat chat instead of the topic list
	ForumAsMessages bool
}

// None is the membership of someone who never joined.
var None = Membership{Status: Left}

var (
	errInvalid      = tgerr.New(400, "CHANNEL_INVALID")
	errPrivate      = tgerr.New(406, "CHANNEL_PRIVATE")
	errAdminRequire = tgerr.New(400, "CHAT_ADMIN_REQUIRED")
	errWriteForbid  = tgerr.New(403, "CHAT_WRITE_FORBIDDEN")
	errUserNotPart  = tgerr.New(400, "USER_NOT_PARTICIPANT")
)

// AccessHash is stable per channel; access is checked on every call.
func AccessHash(id int64) int64 {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(id))
	sum := sha256.Sum256(append([]byte("greengram-channel"), b[:]...))
	return int64(binary.LittleEndian.Uint64(sum[:8]) >> 1)
}

func fullAdmin() tg.ChatAdminRights {
	return tg.ChatAdminRights{ChangeInfo: true, PostMessages: true, EditMessages: true, DeleteMessages: true, BanUsers: true,
		InviteUsers: true, PinMessages: true, AddAdmins: true, ManageCall: true, Other: true, ManageTopics: true,
		PostStories: true, EditStories: true, DeleteStories: true}
}

// can is the right check for one action. Creators can do everything; in a
// group plain members get what the default rights don't forbid.
func (c Channel) can(m Membership, right string) bool {
	if m.Status == Creator {
		return true
	}
	a := m.AdminRights
	if m.Status == Admin {
		switch right {
		case "change_info":
			return a.ChangeInfo
		case "post", "media", "inline":
			return a.PostMessages || c.Megagroup
		case "edit":
			return a.EditMessages
		case "delete":
			return a.DeleteMessages
		case "ban":
			return a.BanUsers
		case "invite":
			return a.InviteUsers
		case "pin":
			return a.PinMessages || c.Megagroup
		case "add_admins":
			return a.AddAdmins
		case "topics":
			return a.ManageTopics
		}
		return false
	}
	if !c.Megagroup || !InChat(m.Status) {
		return false
	}
	d, b := c.DefaultBanned, m.BannedRights
	switch right {
	case "post":
		return !d.SendMessages && !d.SendPlain && !b.SendMessages && !b.SendPlain
	case "media":
		return !d.SendMedia && !b.SendMedia
	case "inline":
		return !d.SendInline && !b.SendInline
	case "invite":
		return !d.InviteUsers && !b.InviteUsers
	case "pin":
		return !d.PinMessages && !b.PinMessages
	case "change_info":
		return !d.ChangeInfo && !b.ChangeInfo
	case "topics":
		return !d.ManageTopics && !b.ManageTopics
	}
	return false
}

func encodeTL(o bin.Encoder) []byte {
	var b bin.Buffer
	if err := o.Encode(&b); err != nil {
		return nil
	}
	return b.Buf
}

func decodeAdmin(raw []byte) tg.ChatAdminRights {
	var r tg.ChatAdminRights
	if len(raw) > 0 {
		r.Decode(&bin.Buffer{Buf: raw})
	}
	return r
}

func decodeBanned(raw []byte) tg.ChatBannedRights {
	var r tg.ChatBannedRights
	if len(raw) > 0 {
		r.Decode(&bin.Buffer{Buf: raw})
	}
	return r
}

// TL is the channel as viewer (with membership m) sees it.
func (c Channel) TL(m Membership) tg.ChatClass {
	if m.Status == Kicked || c.Deleted {
		return &tg.ChannelForbidden{ID: c.ID, AccessHash: AccessHash(c.ID), Title: c.Title, Megagroup: c.Megagroup, Broadcast: !c.Megagroup}
	}
	out := &tg.Channel{
		ID:         c.ID,
		AccessHash: AccessHash(c.ID),
		Title:      c.Title,
		Photo:      &tg.ChatPhotoEmpty{},
		Date:       c.Created,
		Megagroup:  c.Megagroup,
		Broadcast:  !c.Megagroup,
		Creator:    m.Status == Creator,
		Left:       !InChat(m.Status),
		Signatures: c.Signatures,
		HasLink:    c.LinkedID != 0,
		Forum:      c.Megagroup && c.Forum,
		ForumTabs:  c.Megagroup && c.Forum && c.ForumTabs,
	}
	if c.Username != "" {
		out.SetUsername(c.Username)
	}
	out.SetParticipantsCount(c.Count)
	if c.Megagroup {
		out.SetDefaultBannedRights(c.DefaultBanned)
	}
	switch m.Status {
	case Creator:
		out.SetAdminRights(fullAdmin())
	case Admin:
		out.SetAdminRights(m.AdminRights)
	case Restricted:
		out.SetBannedRights(m.BannedRights)
	}
	return out
}

// Msg is one channel message.
type Msg struct {
	Channel   int64
	ID        int
	From      int64
	Date      int
	Post      bool
	Text      string
	Entities  []tg.MessageEntityClass
	Action    tg.MessageActionClass
	ReplyTo   int
	Fwd       messages.Origin
	EditDate  int
	FwdChanID int64 // discussion copy of a channel post
	FwdChanMs int
	Media     media.Ref
	Grouped   int64 // album
	// a bot's keyboard
	ReplyMarkup tg.ReplyMarkupClass
	Topic       int // forum topic; 0 = General
	Pinned      bool
	Silent      bool
	// an article: encoded richMessage, Text is its plain words
	Rich []byte
	// sent through this bot's inline mode
	ViaBot int64
}

// TL is the message without the per-viewer out flag (see SetOut).
func (m Msg) TL() tg.MessageClass {
	peer := &tg.PeerChannel{ChannelID: m.Channel}
	if m.Action != nil {
		s := &tg.MessageService{ID: m.ID, PeerID: peer, Date: m.Date, Action: m.Action, Silent: m.Silent, Post: m.Post}
		// in a broadcast channel it's the channel speaking
		if !m.Post {
			s.SetFromID(&tg.PeerUser{UserID: ids.Client(m.From)})
		}
		if h, ok := m.replyHeader(); ok {
			s.SetReplyTo(h)
		}
		return s
	}
	out := &tg.Message{ID: m.ID, PeerID: peer, Date: m.Date, Message: m.Text, Post: m.Post, Pinned: m.Pinned, Silent: m.Silent}
	if m.Post {
		// Web A keeps the copy it saw first (the dialog's top message, a push) and
		// only asks getMessagesViews about messages that have the field at all
		out.SetViews(1)
	}
	switch {
	case m.FwdChanID != 0:
		// a post's copy in the discussion group speaks as the channel
		out.SetFromID(&tg.PeerChannel{ChannelID: m.FwdChanID})
	case !m.Post:
		out.SetFromID(&tg.PeerUser{UserID: ids.Client(m.From)})
	}
	if m.ViaBot != 0 {
		out.SetViaBotID(ids.Client(m.ViaBot))
	}
	if len(m.Entities) > 0 {
		out.SetEntities(m.Entities)
	}
	if h, ok := m.replyHeader(); ok {
		out.SetReplyTo(h)
	}
	switch {
	case m.FwdChanID != 0:
		h := tg.MessageFwdHeader{FromID: &tg.PeerChannel{ChannelID: m.FwdChanID}, ChannelPost: m.FwdChanMs, Date: m.Date}
		h.SetSavedFromPeer(&tg.PeerChannel{ChannelID: m.FwdChanID})
		h.SetSavedFromMsgID(m.FwdChanMs)
		out.SetFwdFrom(h)
	default:
		if h, ok := m.Fwd.TL(); ok {
			out.SetFwdFrom(h)
		}
	}
	if m.EditDate != 0 {
		out.SetEditDate(m.EditDate)
	}
	if !m.Media.Empty() {
		out.SetMedia(m.Media.TL)
	}
	if m.Grouped != 0 {
		out.SetGroupedID(m.Grouped)
	}
	if r, ok := media.DecodeRich(m.Rich); ok {
		out.SetRichMessage(r)
	}
	if m.ReplyMarkup != nil {
		out.SetReplyMarkup(m.ReplyMarkup)
	}
	return out
}

// replyHeader is Telegram's: a message in a topic says so and points at
// the topic, a reply in one points at the message and names the topic in
// reply_to_top_id. The topic's own first message and General have none.
func (m Msg) replyHeader() (*tg.MessageReplyHeader, bool) {
	switch {
	case m.Topic != 0 && m.Topic == m.ID:
		return nil, false
	case m.Topic != 0 && m.ReplyTo != 0 && m.ReplyTo != m.Topic:
		h := &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: m.ReplyTo}
		h.SetReplyToTopID(m.Topic)
		return h, true
	case m.Topic != 0:
		return &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: m.Topic}, true
	case m.ReplyTo != 0:
		return &tg.MessageReplyHeader{ReplyToMsgID: m.ReplyTo}, true
	}
	return nil, false
}

// SetOut marks messages viewer wrote as outgoing, on a copy.
func SetOut(u tg.UpdateClass, viewer int64) tg.UpdateClass {
	mark := func(msg tg.MessageClass) tg.MessageClass {
		switch m := msg.(type) {
		case *tg.Message:
			if p, ok := m.FromID.(*tg.PeerUser); ok && ids.Server(p.UserID) == viewer {
				c := *m
				c.Out = true
				return &c
			}
		case *tg.MessageService:
			if p, ok := m.FromID.(*tg.PeerUser); ok && ids.Server(p.UserID) == viewer {
				c := *m
				c.Out = true
				return &c
			}
		}
		return msg
	}
	switch v := u.(type) {
	case *tg.UpdateNewChannelMessage:
		c := *v
		c.Message = mark(v.Message)
		return &c
	case *tg.UpdateEditChannelMessage:
		c := *v
		c.Message = mark(v.Message)
		return &c
	}
	return u
}

// MarkOut is SetOut for a bare message.
func MarkOut(msg tg.MessageClass, viewer int64) tg.MessageClass {
	u := SetOut(&tg.UpdateNewChannelMessage{Message: msg}, viewer)
	return u.(*tg.UpdateNewChannelMessage).Message
}
