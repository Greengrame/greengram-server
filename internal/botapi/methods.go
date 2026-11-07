package botapi

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/messages"
)

var (
	errChatNotFound = bad("chat not found")
	errConflict     = &apiError{409, "Conflict: terminated by other getUpdates request; make sure that only one bot instance is running", 0}
)

// chat resolves chat_id: a user id, -100<channel id> or @channelusername.
func (s *Server) chat(ctx context.Context, bot bots.Bot, p params, key string) (chat, error) {
	raw := strings.TrimSpace(p.str(key))
	if raw == "" {
		return chat{}, bad(key + " is empty")
	}
	if strings.HasPrefix(raw, "@") {
		c, ok, err := s.Chans.ByUsername(ctx, raw[1:])
		if err != nil {
			return chat{}, err
		}
		if ok {
			return chat{channel: c.ID}, nil
		}
		// bots reach each other by username, and only when both are in the mode
		if other, ok, err := s.Bots.ByUsername(ctx, raw[1:]); err != nil {
			return chat{}, err
		} else if ok && bool(bot.State.Settings.BotToBot) && bool(other.State.Settings.BotToBot) && other.UserID != bot.UserID {
			return chat{user: other.UserID}, nil
		}
		return chat{}, errChatNotFound
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n == 0 {
		return chat{}, errChatNotFound
	}
	if n < 0 {
		if n > -channelShift {
			// basic groups don't exist here; every group is a supergroup
			return chat{}, errChatNotFound
		}
		return chat{channel: -n - channelShift}, nil
	}
	id := ids.Server(n)
	users, err := s.Accounts.Users(ctx, []int64{id})
	if err != nil {
		return chat{}, err
	}
	if u, ok := users[id]; !ok || id == bot.UserID {
		return chat{}, errChatNotFound
	} else if u.Disabled {
		return chat{}, forbidden("user is deactivated")
	}
	return chat{user: id}, nil
}

// writable checks the bot may post to c: people must have written to the
// bot first; membership in groups is the channels service's business.
func (s *Server) writable(ctx context.Context, bot bots.Bot, c chat) error {
	if c.channel != 0 || bot.BuiltIn() {
		return nil
	}
	other, isBot, err := s.Bots.Get(ctx, c.user)
	if err != nil {
		return err
	}
	if isBot {
		if !bot.State.Settings.BotToBot || !other.State.Settings.BotToBot {
			return forbidden("bots can't send messages to bots")
		}
		return nil
	}
	ok, err := s.Msgs.HasDialog(ctx, bot.UserID, c.user)
	if err != nil {
		return err
	}
	if !ok {
		return forbidden("bot can't initiate conversation with a user")
	}
	return nil
}

func (s *Server) getMe(c *call) (any, error) {
	b := c.bot
	first, last := splitName(b.Name)
	return map[string]any{
		"id": ids.Client(b.UserID), "is_bot": true, "first_name": first, "last_name": last, "username": b.Username,
		"can_join_groups": b.State.AllowGroups(), "can_read_all_group_messages": !b.State.GroupPrivacy(), "supports_inline_queries": bool(b.State.Settings.Inline),
		"can_connect_to_business": false, "has_main_web_app": b.MainAppURL != "",
	}, nil
}

// allowed is the kinds getUpdates hands out, from allowed_updates.
func allowed(p params) (map[string]bool, error) {
	var list []string
	if ok, err := p.json("allowed_updates", &list); err != nil || !ok || len(list) == 0 {
		return nil, err
	}
	out := map[string]bool{}
	for _, k := range list {
		out[k] = true
	}
	return out, nil
}

func (s *Server) getUpdates(c *call) (any, error) {
	offset, err := c.p.int64("offset")
	if err != nil {
		return nil, err
	}
	limit, err := c.p.int("limit")
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	timeout, err := c.p.int("timeout")
	if err != nil {
		return nil, err
	}
	timeout = min(max(timeout, 0), 50)
	want, err := allowed(c.p)
	if err != nil {
		return nil, err
	}
	// one poll per bot, as on Telegram: a newer call ends this one with
	// 409, or a single token could keep any number of them open
	ctx, cancel := context.WithCancel(c.ctx)
	defer cancel()
	me := &poll{stop: cancel}
	s.mu.Lock()
	if s.polls == nil {
		s.polls = map[int64]*poll{}
	}
	if old := s.polls[c.bot.UserID]; old != nil {
		old.superseded = true
		old.stop()
	}
	s.polls[c.bot.UserID] = me
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.polls[c.bot.UserID] == me {
			delete(s.polls, c.bot.UserID)
		}
		s.mu.Unlock()
	}()
	list, err := s.Bots.WaitUpdates(ctx, c.bot.UserID, offset, limit, time.Duration(timeout)*time.Second)
	s.mu.Lock()
	superseded := me.superseded
	s.mu.Unlock()
	if superseded {
		return nil, errConflict
	}
	if err != nil {
		if ctx.Err() != nil {
			return []any{}, nil
		}
		return nil, err
	}
	v := s.view(c.ctx, c.bot)
	out := []any{}
	var drop []int64
	for _, u := range list {
		r, ok := v.update(u)
		if !ok {
			drop = append(drop, u.ID)
			continue
		}
		if want != nil {
			skip := true
			for k := range r {
				if want[k] {
					skip = false
				}
			}
			if skip {
				drop = append(drop, u.ID)
				continue
			}
		}
		out = append(out, r)
	}
	if len(drop) > 0 {
		s.Bots.Drop(c.ctx, c.bot.UserID, drop)
	}
	return out, nil
}

// Webhooks aren't here (greengram-web had none either): polling only.
func (s *Server) setWebhook(c *call) (any, error) {
	if c.p.str("url") == "" {
		return true, nil
	}
	return nil, bad("webhooks are not supported on this server, use getUpdates")
}

func (s *Server) deleteWebhook(c *call) (any, error) {
	if c.p.bool("drop_pending_updates") {
		if err := s.Bots.DropAll(c.ctx, c.bot.UserID); err != nil {
			return nil, err
		}
	}
	return true, nil
}

func (s *Server) getWebhookInfo(c *call) (any, error) {
	n, err := s.Bots.Pending(c.ctx, c.bot.UserID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"url": "", "has_custom_certificate": false, "pending_update_count": n}, nil
}

// text reads text + parse_mode/entities the Bot API way.
func text(p params, key, entKey string) (string, []tg.MessageEntityClass, error) {
	t, ents, err := botAPIFormattedTextRaw(p.str(key), p.str("parse_mode"), p.str(entKey), messages.MaxLength, true)
	if err != nil {
		if e := asAPIError(err); e != nil {
			return "", nil, err
		}
		return "", nil, bad(err.Error())
	}
	return t, tgEntities(ents), nil
}

func markup(p params) (tg.ReplyMarkupClass, bool, error) {
	var m apiMarkup
	ok, err := p.json("reply_markup", &m)
	if err != nil || !ok {
		return nil, ok, err
	}
	t, err := tgMarkup(m)
	return t, true, err
}

func replyTo(p params) (int, error) {
	var r struct {
		MessageID int `json:"message_id"`
	}
	if ok, err := p.json("reply_parameters", &r); err != nil {
		return 0, err
	} else if ok {
		return r.MessageID, nil
	}
	return p.int("reply_to_message_id")
}

// Out is a message a bot sends.
type Out struct {
	Text     string
	Entities []tg.MessageEntityClass
	Markup   tg.ReplyMarkupClass
	ReplyTo  int
	Media    media.Ref // private chats only; Text is the caption then
}

// send posts as the bot and pushes it to the chat.
func (s *Server) send(ctx context.Context, bot bots.Bot, c chat, o Out) (*apiMessage, error) {
	v := s.view(ctx, bot)
	if c.channel != 0 {
		sent, err := s.Chans.Send(ctx, bot.UserID, c.channel, channels.Draft{Text: o.Text, Entities: o.Entities, ReplyTo: o.ReplyTo, ReplyMarkup: o.Markup})
		if err != nil {
			return nil, err
		}
		s.Push.DeliverChannel(sent.Events)
		return v.channelMsg(sent.Msg, 0), nil
	}
	if _, blocked, err := s.Msgs.Blocks(ctx, bot.UserID, c.user); err != nil {
		return nil, err
	} else if blocked {
		return nil, forbidden("bot was blocked by the user")
	}
	sent, err := s.Msgs.Send(ctx, bot.UserID, c.user, messages.Draft{Text: o.Text, Entities: o.Entities, ReplyTo: o.ReplyTo, ReplyMarkup: o.Markup, Media: o.Media})
	if err != nil {
		return nil, err
	}
	s.Push.Deliver(sent.Events)
	return v.private(sent.Msg, 0), nil
}

// SendPrivate is for the built-in bots, which reply in private chats.
func (s *Server) SendPrivate(ctx context.Context, bot bots.Bot, user int64, o Out) error {
	_, err := s.send(ctx, bot, chat{user: user}, o)
	return err
}

func (s *Server) sendMessage(c *call) (any, error) {
	to, err := s.chat(c.ctx, c.bot, c.p, "chat_id")
	if err != nil {
		return nil, err
	}
	if err := s.writable(c.ctx, c.bot, to); err != nil {
		return nil, err
	}
	t, ents, err := text(c.p, "text", "entities")
	if err != nil {
		return nil, err
	}
	mk, _, err := markup(c.p)
	if err != nil {
		return nil, err
	}
	reply, err := replyTo(c.p)
	if err != nil {
		return nil, err
	}
	return s.send(c.ctx, c.bot, to, Out{Text: t, Entities: ents, Markup: mk, ReplyTo: reply})
}

// edit applies a change to one of the bot's messages and pushes it.
func (s *Server) edit(c *call, ch messages.Change) (any, error) {
	if c.p.has("inline_message_id") {
		return s.editInline(c, ch)
	}
	to, err := s.chat(c.ctx, c.bot, c.p, "chat_id")
	if err != nil {
		return nil, err
	}
	id, err := c.p.int("message_id")
	if err != nil || id <= 0 {
		return nil, bad("message identifier is not specified")
	}
	v := s.view(c.ctx, c.bot)
	if to.channel != 0 {
		m, events, err := s.Chans.EditBot(c.ctx, c.bot.UserID, to.channel, id, ch)
		if err != nil {
			return nil, err
		}
		s.Push.DeliverChannel(events)
		return v.channelMsg(m, 0), nil
	}
	m, events, err := s.Msgs.EditBot(c.ctx, c.bot.UserID, to.user, id, ch)
	if err != nil {
		return nil, err
	}
	s.Push.Deliver(events)
	return v.private(m, 0), nil
}

func (s *Server) editMessageText(c *call) (any, error) {
	t, ents, err := text(c.p, "text", "entities")
	if err != nil {
		return nil, err
	}
	// no reply_markup takes the keyboard off, as on Telegram
	ch := messages.Change{Text: &t, Entities: ents, SetMarkup: true}
	if ch.Markup, _, err = markup(c.p); err != nil {
		return nil, err
	}
	return s.edit(c, ch)
}

func (s *Server) editMessageReplyMarkup(c *call) (any, error) {
	ch := messages.Change{SetMarkup: true}
	var err error
	if ch.Markup, _, err = markup(c.p); err != nil {
		return nil, err
	}
	return s.edit(c, ch)
}

func (s *Server) remove(c *call, list []int) (any, error) {
	to, err := s.chat(c.ctx, c.bot, c.p, "chat_id")
	if err != nil {
		return nil, err
	}
	if to.channel != 0 {
		res, events, err := s.Chans.DeleteMessages(c.ctx, c.bot.UserID, to.channel, list)
		if err != nil {
			return nil, err
		}
		s.Push.DeliverChannel(events)
		if res.PtsCount == 0 {
			return nil, bad("message to delete not found")
		}
		return true, nil
	}
	// in private chats a bot may delete both sides' messages, for everyone
	own, err := s.Msgs.Get(c.ctx, c.bot.UserID, list)
	if err != nil {
		return nil, err
	}
	var mine []int
	for _, m := range own {
		if m.Peer == to.user {
			mine = append(mine, m.ID)
		}
	}
	if len(mine) == 0 {
		return nil, bad("message to delete not found")
	}
	_, events, err := s.Msgs.Delete(c.ctx, c.bot.UserID, mine, true)
	if err != nil {
		return nil, err
	}
	s.Push.Deliver(events)
	return true, nil
}

func (s *Server) deleteMessage(c *call) (any, error) {
	id, err := c.p.int("message_id")
	if err != nil || id <= 0 {
		return nil, bad("message identifier is not specified")
	}
	return s.remove(c, []int{id})
}

func (s *Server) deleteMessages(c *call) (any, error) {
	var list []int
	if ok, err := c.p.json("message_ids", &list); err != nil {
		return nil, err
	} else if !ok || len(list) == 0 || len(list) > 100 {
		return nil, bad("message_ids must have 1-100 identifiers")
	}
	return s.remove(c, list)
}

func (s *Server) answerCallbackQuery(c *call) (any, error) {
	id, err := strconv.ParseInt(c.p.str("callback_query_id"), 10, 64)
	if err != nil {
		return nil, bad(bots.ErrQueryInvalid.Error())
	}
	t := c.p.str("text")
	if utf8.RuneCountInString(t) > 200 {
		return nil, bad("MESSAGE_TOO_LONG")
	}
	cache, err := c.p.int("cache_time")
	if err != nil {
		return nil, err
	}
	if err := s.Bots.Answer(c.ctx, c.bot.UserID, id, t, c.p.bool("show_alert"), c.p.str("url"), cache); err != nil {
		return nil, err
	}
	return true, nil
}

var chatActions = map[string]tg.SendMessageActionClass{
	"typing": &tg.SendMessageTypingAction{}, "choose_sticker": &tg.SendMessageChooseStickerAction{},
	"find_location": &tg.SendMessageGeoLocationAction{}, "record_voice": &tg.SendMessageRecordAudioAction{},
	"record_video": &tg.SendMessageRecordVideoAction{}, "record_video_note": &tg.SendMessageRecordRoundAction{},
	"upload_photo": &tg.SendMessageUploadPhotoAction{}, "upload_video": &tg.SendMessageUploadVideoAction{},
	"upload_voice": &tg.SendMessageUploadAudioAction{}, "upload_document": &tg.SendMessageUploadDocumentAction{},
	"upload_video_note": &tg.SendMessageUploadRoundAction{},
}

func (s *Server) sendChatAction(c *call) (any, error) {
	to, err := s.chat(c.ctx, c.bot, c.p, "chat_id")
	if err != nil {
		return nil, err
	}
	act, ok := chatActions[c.p.str("action")]
	if !ok {
		return nil, bad("wrong parameter action in request")
	}
	from := ids.Client(c.bot.UserID)
	if to.channel != 0 {
		_, m, err := s.Chans.Access(c.ctx, to.channel, c.bot.UserID)
		if err != nil {
			return nil, err
		}
		if !channels.InChat(m.Status) {
			return nil, forbidden("bot is not a member of the supergroup chat")
		}
		members, err := s.Chans.MemberIDs(c.ctx, to.channel)
		if err != nil {
			return nil, err
		}
		s.Push.DeliverChannel([]channels.Event{{Users: members, Update: &tg.UpdateChannelUserTyping{ChannelID: to.channel, FromID: &tg.PeerUser{UserID: from}, Action: act}}})
		return true, nil
	}
	if err := s.writable(c.ctx, c.bot, to); err != nil {
		return nil, err
	}
	s.Push.Deliver([]messages.Event{{UserID: to.user, Update: &tg.UpdateUserTyping{UserID: from, Action: act}}})
	return true, nil
}

func (s *Server) getChat(c *call) (any, error) {
	to, err := s.chat(c.ctx, c.bot, c.p, "chat_id")
	if err != nil {
		return nil, err
	}
	v := s.view(c.ctx, c.bot)
	out := map[string]any{}
	ac := v.chat(to)
	out["id"], out["type"] = ac.ID, ac.Type
	if to.channel != 0 {
		ch, _, err := s.Chans.Access(c.ctx, to.channel, c.bot.UserID)
		if err != nil {
			return nil, err
		}
		out["title"] = ch.Title
		if ch.Username != "" {
			out["username"] = ch.Username
		}
		if ch.About != "" {
			out["description"] = ch.About
		}
		return out, nil
	}
	out["first_name"] = ac.FirstName
	if ac.LastName != "" {
		out["last_name"] = ac.LastName
	}
	if ac.Username != "" {
		out["username"] = ac.Username
	}
	if u, ok := v.users[to.user]; ok && u.Bio != "" {
		out["bio"] = u.Bio
	}
	return out, nil
}

func memberStatus(m channels.Membership) string {
	switch m.Status {
	case channels.Creator:
		return "creator"
	case channels.Admin:
		return "administrator"
	case channels.Restricted:
		return "restricted"
	case channels.Kicked:
		return "kicked"
	case channels.Member:
		return "member"
	}
	return "left"
}

func (s *Server) getChatMember(c *call) (any, error) {
	to, err := s.chat(c.ctx, c.bot, c.p, "chat_id")
	if err != nil {
		return nil, err
	}
	uid, err := c.p.int64("user_id")
	if err != nil || uid <= 0 {
		return nil, bad("invalid user_id specified")
	}
	id := ids.Server(uid)
	v := s.view(c.ctx, c.bot)
	if to.channel == 0 {
		if id != to.user && id != c.bot.UserID {
			return nil, bad("user not found")
		}
		return map[string]any{"status": "member", "user": v.user(id)}, nil
	}
	if _, _, err := s.Chans.Access(c.ctx, to.channel, c.bot.UserID); err != nil {
		return nil, err
	}
	m, err := s.Chans.Member(c.ctx, to.channel, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": memberStatus(m), "user": v.user(id)}, nil
}

func (s *Server) getChatMemberCount(c *call) (any, error) {
	to, err := s.chat(c.ctx, c.bot, c.p, "chat_id")
	if err != nil {
		return nil, err
	}
	if to.channel == 0 {
		return 2, nil
	}
	ch, _, err := s.Chans.Access(c.ctx, to.channel, c.bot.UserID)
	if err != nil {
		return nil, err
	}
	return ch.Count, nil
}

func (s *Server) getChatAdministrators(c *call) (any, error) {
	to, err := s.chat(c.ctx, c.bot, c.p, "chat_id")
	if err != nil {
		return nil, err
	}
	if to.channel == 0 {
		return nil, bad("there are no administrators in the private chat")
	}
	if _, _, err := s.Chans.Access(c.ctx, to.channel, c.bot.UserID); err != nil {
		return nil, err
	}
	list, _, err := s.Chans.Members(c.ctx, to.channel, "admins", "", 0, 200)
	if err != nil {
		return nil, err
	}
	v := s.view(c.ctx, c.bot)
	out := []any{}
	for _, m := range list {
		out = append(out, map[string]any{"status": memberStatus(m), "user": v.user(m.UserID), "is_anonymous": false})
	}
	return out, nil
}

func (s *Server) leaveChat(c *call) (any, error) {
	to, err := s.chat(c.ctx, c.bot, c.p, "chat_id")
	if err != nil {
		return nil, err
	}
	if to.channel == 0 {
		return nil, bad("chat member status can't be changed in private chats")
	}
	events, err := s.Chans.Leave(c.ctx, c.bot.UserID, to.channel)
	if err != nil {
		return nil, err
	}
	s.Push.DeliverChannel(events)
	return true, nil
}

// Only the default scope and language exist, like greengram-web.
func scope(p params) error {
	var sc struct {
		Type string `json:"type"`
	}
	if ok, err := p.json("scope", &sc); err != nil {
		return err
	} else if ok && sc.Type != "default" {
		return bad("only the default command scope is supported")
	}
	if p.str("language_code") != "" {
		return bad("localized commands are not supported")
	}
	return nil
}

var commandRe2 = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// ParseCommands checks a command list the way BotFather does.
func ParseCommands(list []bots.Command) error {
	if len(list) > 100 {
		return bad("too many commands")
	}
	seen := map[string]bool{}
	for _, c := range list {
		if !commandRe2.MatchString(c.Command) {
			return bad("BOT_COMMAND_INVALID")
		}
		if n := utf8.RuneCountInString(c.Description); n < 1 || n > 256 {
			return bad("BOT_COMMAND_DESCRIPTION_INVALID")
		}
		if seen[c.Command] {
			return bad("BOT_COMMAND_INVALID")
		}
		seen[c.Command] = true
	}
	return nil
}

func (s *Server) setMyCommands(c *call) (any, error) {
	if err := scope(c.p); err != nil {
		return nil, err
	}
	var list []bots.Command
	if _, err := c.p.json("commands", &list); err != nil {
		return nil, err
	}
	for i := range list {
		list[i].Command = strings.TrimPrefix(list[i].Command, "/")
	}
	if err := ParseCommands(list); err != nil {
		return nil, err
	}
	return true, s.Bots.SetCommands(c.ctx, c.bot.UserID, list)
}

func (s *Server) getMyCommands(c *call) (any, error) {
	if err := scope(c.p); err != nil {
		return nil, err
	}
	if c.bot.Commands == nil {
		return []bots.Command{}, nil
	}
	return c.bot.Commands, nil
}

func (s *Server) deleteMyCommands(c *call) (any, error) {
	if err := scope(c.p); err != nil {
		return nil, err
	}
	return true, s.Bots.SetCommands(c.ctx, c.bot.UserID, nil)
}

type apiMenuButton struct {
	Type   string     `json:"type"`
	Text   string     `json:"text,omitempty"`
	WebApp *apiWebApp `json:"web_app,omitempty"`
}

func menuJSON(m *bots.MenuButton) apiMenuButton {
	if m == nil {
		return apiMenuButton{Type: "commands"}
	}
	return apiMenuButton{Type: "web_app", Text: m.Text, WebApp: &apiWebApp{URL: m.URL}}
}

func (s *Server) setChatMenuButton(c *call) (any, error) {
	var b apiMenuButton
	if _, err := c.p.json("menu_button", &b); err != nil {
		return nil, err
	}
	var menu *bots.MenuButton
	switch b.Type {
	case "", "default", "commands":
	case "web_app":
		if b.WebApp == nil || b.Text == "" || utf8.RuneCountInString(b.Text) > 64 || !validURL(b.WebApp.URL, "https", "http") {
			return nil, bad("provide a menu label and a valid Web App URL")
		}
		menu = &bots.MenuButton{Text: b.Text, URL: b.WebApp.URL}
	default:
		return nil, bad("unsupported menu button type")
	}
	if !c.p.has("chat_id") {
		return true, s.Bots.SetMenu(c.ctx, c.bot.UserID, menu)
	}
	to, err := s.chat(c.ctx, c.bot, c.p, "chat_id")
	if err != nil {
		return nil, err
	}
	if to.channel != 0 {
		return nil, bad("menu buttons can be set only in private chats")
	}
	return true, s.Bots.SetChatMenu(c.ctx, c.bot.UserID, to.user, menu, b.Type == "commands")
}

func (s *Server) getChatMenuButton(c *call) (any, error) {
	if !c.p.has("chat_id") {
		return menuJSON(c.bot.Menu), nil
	}
	to, err := s.chat(c.ctx, c.bot, c.p, "chat_id")
	if err != nil {
		return nil, err
	}
	if to.channel != 0 {
		return nil, bad("menu buttons can be set only in private chats")
	}
	m, err := s.Bots.ChatMenu(c.ctx, c.bot, to.user)
	if err != nil {
		return nil, err
	}
	return menuJSON(m), nil
}

func noLanguage(p params) error {
	if p.str("language_code") != "" {
		return bad("localized bot profiles are not supported")
	}
	return nil
}

func (s *Server) setMyName(c *call) (any, error) {
	if err := noLanguage(c.p); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(c.p.str("name"))
	if name == "" || utf8.RuneCountInString(name) > 64 {
		return nil, bad("bot name must be 1-64 characters")
	}
	return true, s.Bots.SetName(c.ctx, c.bot.UserID, name)
}

func (s *Server) getMyName(c *call) (any, error) {
	return map[string]string{"name": c.bot.Name}, noLanguage(c.p)
}

func (s *Server) setMyDescription(c *call) (any, error) {
	if err := noLanguage(c.p); err != nil {
		return nil, err
	}
	d := c.p.str("description")
	if utf8.RuneCountInString(d) > 512 {
		return nil, bad("bot description is too long")
	}
	return true, s.Bots.SetDescription(c.ctx, c.bot.UserID, d)
}

func (s *Server) getMyDescription(c *call) (any, error) {
	return map[string]string{"description": c.bot.Description}, noLanguage(c.p)
}

func (s *Server) setMyShortDescription(c *call) (any, error) {
	if err := noLanguage(c.p); err != nil {
		return nil, err
	}
	d := c.p.str("short_description")
	if utf8.RuneCountInString(d) > 120 {
		return nil, bad("bot short description is too long")
	}
	return true, s.Bots.SetAbout(c.ctx, c.bot.UserID, d)
}

func (s *Server) getMyShortDescription(c *call) (any, error) {
	return map[string]string{"short_description": c.bot.About}, noLanguage(c.p)
}
