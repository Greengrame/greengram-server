package botapi

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/messages"
)

// Bot API chat ids: people are their user id, groups and channels
// -100<id>, as on Telegram.
const channelShift = 1_000_000_000_000

type chat struct {
	user    int64 // server id
	channel int64
}

func (c chat) id() int64 {
	if c.channel != 0 {
		return -(channelShift + c.channel)
	}
	return ids.Client(c.user)
}

type apiUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
	Premium   bool   `json:"is_premium,omitempty"`
}

type apiChat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

type apiOrigin struct {
	Type       string   `json:"type"`
	Date       int      `json:"date"`
	SenderUser *apiUser `json:"sender_user,omitempty"`
	SenderName string   `json:"sender_user_name,omitempty"`
	Chat       *apiChat `json:"chat,omitempty"`
	SenderChat *apiChat `json:"sender_chat,omitempty"`
	MessageID  int      `json:"message_id,omitempty"`
}

// origin is forward_origin for o; nil when it isn't a forward.
func (v *view) origin(o messages.Origin) *apiOrigin {
	switch {
	case o.Chan != 0:
		c := v.chat(chat{channel: o.Chan})
		if o.Post == 0 {
			return &apiOrigin{Type: "chat", Date: o.Date, SenderChat: &c}
		}
		return &apiOrigin{Type: "channel", Date: o.Date, Chat: &c, MessageID: o.Post}
	case o.User != 0:
		return &apiOrigin{Type: "user", Date: o.Date, SenderUser: v.user(o.User)}
	case o.Name != "":
		return &apiOrigin{Type: "hidden_user", Date: o.Date, SenderName: o.Name}
	}
	return nil
}

type apiMessage struct {
	MessageID     int                `json:"message_id"`
	From          *apiUser           `json:"from,omitempty"`
	SenderChat    *apiChat           `json:"sender_chat,omitempty"`
	ViaBot        *apiUser           `json:"via_bot,omitempty"`
	Date          int                `json:"date"`
	Chat          apiChat            `json:"chat"`
	ForwardOrigin *apiOrigin         `json:"forward_origin,omitempty"`
	ReplyTo       *apiMessage        `json:"reply_to_message,omitempty"`
	EditDate      int                `json:"edit_date,omitempty"`
	Text          string             `json:"text,omitempty"`
	Entities      []apiMessageEntity `json:"entities,omitempty"`
	ReplyMarkup   *apiMarkup         `json:"reply_markup,omitempty"`
	WebAppData    *apiWebAppData     `json:"web_app_data,omitempty"`
}

type apiWebAppData struct {
	Data       string `json:"data"`
	ButtonText string `json:"button_text"`
}

func splitName(name string) (string, string) {
	first, last, _ := strings.Cut(strings.TrimSpace(name), " ")
	return first, strings.TrimSpace(last)
}

func userJSON(u account.User) *apiUser {
	first, last := splitName(u.Name)
	out := &apiUser{ID: ids.Client(u.ID), IsBot: u.IsBot, FirstName: first, LastName: last}
	if u.UsernamePublic {
		out.Username = u.Username
	}
	return out
}

// view renders for one bot, loading users and chats once per request.
type view struct {
	s     *Server
	ctx   context.Context
	bot   bots.Bot
	users map[int64]account.User
	chans map[int64]channels.Channel
}

func (s *Server) view(ctx context.Context, bot bots.Bot) *view {
	return &view{s: s, ctx: ctx, bot: bot, users: map[int64]account.User{}, chans: map[int64]channels.Channel{}}
}

func (v *view) user(id int64) *apiUser {
	u, ok := v.users[id]
	if !ok {
		got, err := v.s.Accounts.Users(v.ctx, []int64{id})
		if err != nil {
			return &apiUser{ID: ids.Client(id)}
		}
		u, ok = got[id]
		if !ok {
			return &apiUser{ID: ids.Client(id), FirstName: "Deleted Account"}
		}
		v.users[id] = u
	}
	return userJSON(u)
}

func (v *view) channel(id int64) (channels.Channel, bool) {
	c, ok := v.chans[id]
	if ok {
		return c, true
	}
	c, ok, err := v.s.Chans.Get(v.ctx, id)
	if err != nil || !ok {
		return c, false
	}
	v.chans[id] = c
	return c, true
}

func (v *view) chat(c chat) apiChat {
	if c.channel != 0 {
		out := apiChat{ID: c.id(), Type: "supergroup"}
		if ch, ok := v.channel(c.channel); ok {
			out.Title, out.Username = ch.Title, ch.Username
			if !ch.Megagroup {
				out.Type = "channel"
			}
		}
		return out
	}
	u := v.user(c.user)
	return apiChat{ID: u.ID, Type: "private", FirstName: u.FirstName, LastName: u.LastName, Username: u.Username}
}

// private renders a message from the bot's box.
func (v *view) private(m messages.Msg, depth int) *apiMessage {
	c := chat{user: m.Peer}
	out := &apiMessage{MessageID: m.ID, From: v.user(m.From), Date: m.Date, Chat: v.chat(c), EditDate: m.EditDate}
	if m.ViaBot != 0 {
		out.ViaBot = v.user(m.ViaBot)
	}
	v.body(out, m.Text, m.Entities, m.ReplyMarkup, !m.Out)
	if d, ok := m.Action.(*tg.MessageActionWebViewDataSentMe); ok {
		out.WebAppData = &apiWebAppData{Data: d.Data, ButtonText: d.Text}
	}
	out.ForwardOrigin = v.origin(m.Fwd)
	if m.ReplyTo != 0 && depth == 0 {
		if list, err := v.s.Msgs.Get(v.ctx, v.bot.UserID, []int{m.ReplyTo}); err == nil && len(list) == 1 {
			out.ReplyTo = v.private(list[0], 1)
		}
	}
	return out
}

func (v *view) channelMsg(m channels.Msg, depth int) *apiMessage {
	c := chat{channel: m.Channel}
	out := &apiMessage{MessageID: m.ID, Date: m.Date, Chat: v.chat(c), EditDate: m.EditDate}
	if m.ViaBot != 0 {
		out.ViaBot = v.user(m.ViaBot)
	}
	switch {
	case m.FwdChanID != 0:
		sc := v.chat(chat{channel: m.FwdChanID})
		out.SenderChat = &sc
		out.ForwardOrigin = &apiOrigin{Type: "channel", Date: m.Date, Chat: &sc, MessageID: m.FwdChanMs}
	case m.Post:
		sc := out.Chat
		out.SenderChat = &sc
	default:
		out.From = v.user(m.From)
	}
	v.body(out, m.Text, m.Entities, m.ReplyMarkup, m.From != v.bot.UserID)
	if o := v.origin(m.Fwd); o != nil {
		out.ForwardOrigin = o
	}
	if m.ReplyTo != 0 && depth == 0 {
		if list, err := v.s.Chans.Messages(v.ctx, m.Channel, []int{m.ReplyTo}); err == nil && len(list) == 1 {
			out.ReplyTo = v.channelMsg(list[0], 1)
		}
	}
	return out
}

// body fills text and entities. Telegram's server finds commands in what
// people send; bot libraries route by that entity.
func (v *view) body(out *apiMessage, text string, ents []tg.MessageEntityClass, markup tg.ReplyMarkupClass, incoming bool) {
	out.Text = text
	if incoming {
		ents = withCommand(text, ents)
	}
	out.Entities = apiEntities(ents)
	out.ReplyMarkup = apiInlineMarkup(markup)
}

// message loads and renders one message as the bot sees it.
func (v *view) message(c chat, id int) (*apiMessage, bool) {
	if c.channel != 0 {
		list, err := v.s.Chans.Messages(v.ctx, c.channel, []int{id})
		if err != nil || len(list) == 0 || list[0].Action != nil {
			return nil, false
		}
		return v.channelMsg(list[0], 0), true
	}
	list, err := v.s.Msgs.Get(v.ctx, v.bot.UserID, []int{id})
	if err != nil || len(list) == 0 {
		return nil, false
	}
	return v.private(list[0], 0), true
}

// chatInstance stays the same for a bot and chat, so games and shared
// state can key on it.
func chatInstance(bot int64, c chat) string {
	var b [16]byte
	binary.LittleEndian.PutUint64(b[:8], uint64(bot))
	binary.LittleEndian.PutUint64(b[8:], uint64(c.id()))
	sum := sha256.Sum256(append([]byte("greengram-chat-instance"), b[:]...))
	return strconv.FormatInt(int64(binary.LittleEndian.Uint64(sum[:8])>>1), 10)
}

// ChatInstance is chat_instance for a chat a direct link was opened from. A
// private chat is the same chat from either side, so its two people go in sorted.
func ChatInstance(bot, channel, me, peer int64) string {
	if channel != 0 {
		return chatInstance(bot, chat{channel: channel})
	}
	var b [24]byte
	binary.LittleEndian.PutUint64(b[:8], uint64(bot))
	binary.LittleEndian.PutUint64(b[8:16], uint64(min(me, peer)))
	binary.LittleEndian.PutUint64(b[16:], uint64(max(me, peer)))
	sum := sha256.Sum256(append([]byte("greengram-chat-instance"), b[:]...))
	return strconv.FormatInt(int64(binary.LittleEndian.Uint64(sum[:8])>>1), 10)
}

type apiCallback struct {
	ID           string      `json:"id"`
	From         *apiUser    `json:"from"`
	Message      *apiMessage `json:"message,omitempty"`
	InlineID     string      `json:"inline_message_id,omitempty"`
	ChatInstance string      `json:"chat_instance"`
	Data         string      `json:"data,omitempty"`
}

// update renders a queued update; ok is false when what it points at is gone.
func (v *view) update(u bots.Update) (map[string]any, bool) {
	c := chat{user: u.ChatID}
	if u.Channel {
		c = chat{channel: u.ChatID}
	}
	switch u.Kind {
	case "callback_query":
		cb, ok, err := v.s.Bots.Callback(v.ctx, u.QueryID)
		if err != nil || !ok {
			return nil, false
		}
		out := apiCallback{ID: strconv.FormatInt(cb.ID, 10), From: v.user(cb.User), ChatInstance: chatInstance(v.bot.UserID, c), Data: string(cb.Data)}
		// a message sent via the bot comes by id alone: the bot may not be in that chat
		if cb.InlineID != "" {
			out.InlineID = cb.InlineID
		} else {
			out.Message, _ = v.message(c, cb.MsgID)
		}
		return map[string]any{"update_id": u.ID, "callback_query": out}, true
	case "inline_query":
		q, ok, err := v.s.Bots.InlineQueryByID(v.ctx, u.QueryID)
		if err != nil || !ok {
			return nil, false
		}
		return map[string]any{"update_id": u.ID, "inline_query": map[string]any{"id": strconv.FormatInt(q.ID, 10), "from": v.user(q.User),
			"query": q.Query, "offset": q.Offset, "chat_type": q.ChatType}}, true
	case "chosen_inline_result":
		var ch bots.Chosen
		if json.Unmarshal(u.Payload, &ch) != nil {
			return nil, false
		}
		out := map[string]any{"result_id": ch.ResultID, "from": v.user(ch.User), "query": ch.Query}
		if ch.InlineID != "" {
			out["inline_message_id"] = ch.InlineID
		}
		return map[string]any{"update_id": u.ID, "chosen_inline_result": out}, true
	}
	m, ok := v.message(c, u.MsgID)
	if !ok {
		return nil, false
	}
	kind := u.Kind
	if m.Chat.Type == "channel" {
		kind = strings.Replace(kind, "message", "channel_post", 1)
	}
	return map[string]any{"update_id": u.ID, kind: m}, true
}
