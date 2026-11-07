package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/botapi"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/miniapps"
	"github.com/Greengrame/greengram-server/internal/builtin"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/messages"
)

// Bots turns bots on: system accounts, the built-in bots, their MTProto
// methods and the Bot API. siteName names the announcements account.
// The returned server's Wrap serves /bot<token>/... in front of the mux.
func (a *API) Bots(ctx context.Context, b *bots.Service, siteName string) (*botapi.Server, error) {
	sys, err := b.EnsureSystem(ctx, siteName)
	if err != nil {
		return nil, err
	}
	ids.SetAnnouncements(sys[bots.Announcements])
	if err := b.EnsureBuiltins(ctx, a.in.BaseURL); err != nil {
		return nil, err
	}
	a.bots = b
	a.botAPI = &botapi.Server{Bots: b, Accounts: a.accounts, Msgs: a.msgs, Chans: a.chans, Push: a}
	go b.Notifier().Run(ctx)
	a.registerBots()
	go builtin.Run(ctx, builtin.Deps{Bots: b, Msgs: a.msgs, Send: a.botAPI, BaseURL: a.in.BaseURL, Reports: a.reports})
	return a.botAPI, nil
}

// Deliver pushes private-chat events to people's apps (botapi.Pusher).
func (a *API) Deliver(events []messages.Event) { a.deliver(events, edge.Session{}) }

// DeliverChannel pushes group and channel events (botapi.Pusher).
func (a *API) DeliverChannel(events []channels.Event) { a.deliverChannel(events, edge.Session{}) }

var (
	errBotInvalid  = tgerr.New(400, "BOT_INVALID")
	errBotRequired = tgerr.New(400, "USER_BOT_REQUIRED")
)

func (a *API) registerBots() {
	handle(a, tlprofile.SemanticMethodMessagesGetBotCallbackAnswer, a.getBotCallbackAnswer)
	handle(a, tlprofile.SemanticMethodMessagesStartBot, a.startBot)
	handle(a, tlprofile.SemanticMethodMessagesGetInlineBotResults, a.getInlineBotResults)
	handle(a, tlprofile.SemanticMethodMessagesSendInlineBotResult, a.sendInlineBotResult)
	handle(a, tlprofile.SemanticMethodMessagesRequestWebView, a.requestWebView)
	handle(a, tlprofile.SemanticMethodMessagesRequestSimpleWebView, a.requestSimpleWebView)
	handle(a, tlprofile.SemanticMethodMessagesRequestMainWebView, a.requestMainWebView)
	handle(a, tlprofile.SemanticMethodMessagesRequestAppWebView, a.requestAppWebView)
	handle(a, tlprofile.SemanticMethodMessagesGetBotApp, a.getBotApp)
	handle(a, tlprofile.SemanticMethodMessagesProlongWebView, func(context.Context, *tg.MessagesProlongWebViewRequest) (any, error) { return true, nil })
	handle(a, tlprofile.SemanticMethodBotsSetBotCommands, a.setBotCommands)
	handle(a, tlprofile.SemanticMethodBotsGetBotCommands, a.getBotCommands)
	handle(a, tlprofile.SemanticMethodBotsResetBotCommands, a.resetBotCommands)
	handle(a, tlprofile.SemanticMethodBotsSetBotMenuButton, a.setBotMenuButton)
	handle(a, tlprofile.SemanticMethodBotsGetBotMenuButton, a.getBotMenuButton)
	handle(a, tlprofile.SemanticMethodBotsSetBotInfo, a.setBotInfo)
	handle(a, tlprofile.SemanticMethodBotsGetBotInfo, a.getBotInfo)
}

// botFlags marks bot users the way clients expect them; called from
// userView for every bot.
func botFlags(viewer int64, t *tg.User, b bots.Bot) {
	t.Status = nil
	t.SetBotInfoVersion(b.InfoVersion)
	t.BotHasMainApp = b.MainAppURL != ""
	t.BotCanEdit = b.OwnerID == viewer
	t.BotNochats = !b.State.AllowGroups()
	t.BotChatHistory = !b.State.GroupPrivacy()
	if b.State.Settings.Inline {
		t.SetBotInlinePlaceholder(b.State.InlinePlaceholder())
	}
	t.Verified = b.BuiltIn()
	if t.BotHasMainApp {
		t.SetBotActiveUsers(0)
	}
}

func commandsTL(list []bots.Command) []tg.BotCommand {
	out := make([]tg.BotCommand, 0, len(list))
	for _, c := range list {
		out = append(out, tg.BotCommand{Command: c.Command, Description: c.Description})
	}
	return out
}

func menuTL(m *bots.MenuButton) tg.BotMenuButtonClass {
	if m == nil {
		return &tg.BotMenuButtonCommands{}
	}
	return &tg.BotMenuButton{Text: m.Text, URL: m.URL}
}

// botFull fills bot_info in users.getFullUser.
func (a *API) botFull(ctx context.Context, viewer int64, full *tg.UserFull, u account.User) {
	if a.bots == nil || !u.IsBot {
		return
	}
	b, ok, err := a.bots.Get(ctx, u.ID)
	if err != nil || !ok {
		return
	}
	menu, err := a.bots.ChatMenu(ctx, b, viewer)
	if err != nil {
		slog.Warn("api: bot menu", "err", err)
	}
	info := tg.BotInfo{}
	info.SetUserID(ids.Client(b.UserID))
	info.SetDescription(b.Description)
	info.SetCommands(commandsTL(b.Commands))
	info.SetMenuButton(menuTL(menu))
	if id := b.State.WelcomePhoto; id != 0 {
		photo := a.fullPhoto(ctx, id)
		if _, empty := photo.(*tg.PhotoEmpty); !empty {
			info.SetDescriptionPhoto(photo)
		}
	}
	if link := b.State.Settings.PrivacyURL; link != "" {
		info.SetPrivacyPolicyURL(link)
	}
	if set, ok := b.State.Settings.Splash.AppSettings(); ok {
		info.SetAppSettings(set)
	}
	full.SetBotInfo(info)
	if list := b.State.Settings.GroupRights; len(list) > 0 {
		full.SetBotGroupAdminRights(bots.AdminRights(list))
	}
	if list := b.State.Settings.ChannelRights; len(list) > 0 {
		full.SetBotBroadcastAdminRights(bots.AdminRights(list))
	}
	if b.About != "" {
		full.SetAbout(b.About)
	}
}

// meBot is the calling account when it must be a bot. Bots don't sign in
// over MTProto yet (they use the Bot API), so this is mostly for later.
func (a *API) meBot(ctx context.Context) (bots.Bot, error) {
	me, err := a.me(ctx)
	if err != nil {
		return bots.Bot{}, err
	}
	if !me.IsBot || a.bots == nil {
		return bots.Bot{}, errBotRequired
	}
	b, ok, err := a.bots.Get(ctx, me.ID)
	if err != nil {
		return b, err
	}
	if !ok {
		return b, errBotRequired
	}
	return b, nil
}

// bot resolves an input user that must be a bot.
// errBotRestricted is what someone outside a bot's Restrict bot usage list gets.
// Web A has words for this code; it has none for a new one.
var errBotRestricted = tgerr.New(403, "USER_PRIVACY_RESTRICTED")

func (a *API) bot(ctx context.Context, me account.User, in tg.InputUserClass) (bots.Bot, error) {
	id, err := a.inputUser(ctx, me, in)
	if err != nil {
		return bots.Bot{}, err
	}
	b, ok, err := a.bots.Get(ctx, id)
	if err != nil {
		return b, err
	}
	if !ok || b.Disabled {
		return b, errBotInvalid
	}
	return b, nil
}

func defaultScope(s tg.BotCommandScopeClass, lang string) error {
	if _, ok := s.(*tg.BotCommandScopeDefault); !ok || lang != "" {
		return tgerr.New(400, "BOT_COMMAND_SCOPE_INVALID")
	}
	return nil
}

func (a *API) setBotCommands(ctx context.Context, r *tg.BotsSetBotCommandsRequest) (any, error) {
	b, err := a.meBot(ctx)
	if err != nil {
		return nil, err
	}
	if err := defaultScope(r.Scope, r.LangCode); err != nil {
		return nil, err
	}
	list := make([]bots.Command, 0, len(r.Commands))
	for _, c := range r.Commands {
		list = append(list, bots.Command{Command: strings.TrimPrefix(c.Command, "/"), Description: c.Description})
	}
	if err := botapi.ParseCommands(list); err != nil {
		return nil, tgerr.New(400, "BOT_COMMAND_INVALID")
	}
	return true, a.bots.SetCommands(ctx, b.UserID, list)
}

func (a *API) getBotCommands(ctx context.Context, r *tg.BotsGetBotCommandsRequest) (any, error) {
	b, err := a.meBot(ctx)
	if err != nil {
		return nil, err
	}
	if err := defaultScope(r.Scope, r.LangCode); err != nil {
		return nil, err
	}
	return commandsTL(b.Commands), nil
}

func (a *API) resetBotCommands(ctx context.Context, r *tg.BotsResetBotCommandsRequest) (any, error) {
	b, err := a.meBot(ctx)
	if err != nil {
		return nil, err
	}
	if err := defaultScope(r.Scope, r.LangCode); err != nil {
		return nil, err
	}
	return true, a.bots.SetCommands(ctx, b.UserID, nil)
}

func menuFrom(m tg.BotMenuButtonClass) (*bots.MenuButton, bool, error) {
	switch v := m.(type) {
	case *tg.BotMenuButtonDefault:
		return nil, false, nil
	case *tg.BotMenuButtonCommands:
		return nil, true, nil
	case *tg.BotMenuButton:
		if v.Text == "" || utf8.RuneCountInString(v.Text) > 64 || !strings.HasPrefix(v.URL, "https://") && !strings.HasPrefix(v.URL, "http://") {
			return nil, false, tgerr.New(400, "BUTTON_URL_INVALID")
		}
		return &bots.MenuButton{Text: v.Text, URL: v.URL}, false, nil
	}
	return nil, false, tgerr.New(400, "BUTTON_TYPE_INVALID")
}

func (a *API) setBotMenuButton(ctx context.Context, r *tg.BotsSetBotMenuButtonRequest) (any, error) {
	b, err := a.meBot(ctx)
	if err != nil {
		return nil, err
	}
	m, commands, err := menuFrom(r.Button)
	if err != nil {
		return nil, err
	}
	if _, ok := r.UserID.(*tg.InputUserEmpty); ok {
		return true, a.bots.SetMenu(ctx, b.UserID, m)
	}
	me, _ := a.me(ctx)
	user, err := a.inputUser(ctx, me, r.UserID)
	if err != nil {
		return nil, err
	}
	return true, a.bots.SetChatMenu(ctx, b.UserID, user, m, commands)
}

func (a *API) getBotMenuButton(ctx context.Context, r *tg.BotsGetBotMenuButtonRequest) (any, error) {
	b, err := a.meBot(ctx)
	if err != nil {
		return nil, err
	}
	if _, ok := r.UserID.(*tg.InputUserEmpty); ok {
		return menuTL(b.Menu), nil
	}
	me, _ := a.me(ctx)
	user, err := a.inputUser(ctx, me, r.UserID)
	if err != nil {
		return nil, err
	}
	m, err := a.bots.ChatMenu(ctx, b, user)
	return menuTL(m), err
}

// ownedBot is bots.setBotInfo's target: the caller's own bot, or itself.
func (a *API) ownedBot(ctx context.Context, in tg.InputUserClass, set bool) (bots.Bot, error) {
	if !set {
		return a.meBot(ctx)
	}
	me, err := a.me(ctx)
	if err != nil {
		return bots.Bot{}, err
	}
	b, err := a.bot(ctx, me, in)
	if err != nil {
		return b, err
	}
	if b.OwnerID != me.ID {
		return b, errBotInvalid
	}
	return b, nil
}

func (a *API) setBotInfo(ctx context.Context, r *tg.BotsSetBotInfoRequest) (any, error) {
	_, has := r.GetBot()
	b, err := a.ownedBot(ctx, r.Bot, has)
	if err != nil {
		return nil, err
	}
	if r.LangCode != "" {
		return nil, tgerr.New(400, "LANG_CODE_NOT_SUPPORTED")
	}
	if name, ok := r.GetName(); ok {
		name = strings.TrimSpace(name)
		if name == "" || utf8.RuneCountInString(name) > 64 {
			return nil, tgerr.New(400, "FIRSTNAME_INVALID")
		}
		if err := a.bots.SetName(ctx, b.UserID, name); err != nil {
			return nil, err
		}
	}
	if about, ok := r.GetAbout(); ok {
		if utf8.RuneCountInString(about) > 120 {
			return nil, tgerr.New(400, "ABOUT_TOO_LONG")
		}
		if err := a.bots.SetAbout(ctx, b.UserID, about); err != nil {
			return nil, err
		}
	}
	if d, ok := r.GetDescription(); ok {
		if utf8.RuneCountInString(d) > 512 {
			return nil, tgerr.New(400, "ABOUT_TOO_LONG")
		}
		if err := a.bots.SetDescription(ctx, b.UserID, d); err != nil {
			return nil, err
		}
	}
	return true, nil
}

func (a *API) getBotInfo(ctx context.Context, r *tg.BotsGetBotInfoRequest) (any, error) {
	_, has := r.GetBot()
	b, err := a.ownedBot(ctx, r.Bot, has)
	if err != nil {
		return nil, err
	}
	return &tg.BotsBotInfo{Name: b.Name, About: b.About, Description: b.Description}, nil
}

// getBotCallbackAnswer is a press on an inline button: the bot gets a
// callback_query and the presser waits for answerCallbackQuery.
func (a *API) getBotCallbackAnswer(ctx context.Context, r *tg.MessagesGetBotCallbackAnswerRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	data, ok := r.GetData()
	if !ok || r.Game {
		return nil, tgerr.New(400, "DATA_INVALID")
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	press := bots.Callback{User: me.ID, Data: data}
	if to.channel != 0 {
		if _, _, err := a.chans.Access(ctx, to.channel, me.ID); err != nil {
			return nil, err
		}
		list, err := a.chans.Messages(ctx, to.channel, []int{r.MsgID})
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			return nil, tgerr.New(400, "MESSAGE_ID_INVALID")
		}
		m := list[0]
		if !botapi.HasCallback(m.ReplyMarkup, data) {
			return nil, tgerr.New(400, "DATA_INVALID")
		}
		press.Bot, press.ChatID, press.Channel, press.MsgID = m.From, to.channel, true, m.ID
		if m.ViaBot != 0 {
			press.Bot, press.InlineID = m.ViaBot, bots.ChannelInlineID(to.channel, m.ID)
		}
	} else {
		list, err := a.msgs.Get(ctx, me.ID, []int{r.MsgID})
		if err != nil {
			return nil, err
		}
		if len(list) == 0 || list[0].Peer != to.user {
			return nil, tgerr.New(400, "MESSAGE_ID_INVALID")
		}
		m := list[0]
		if !botapi.HasCallback(m.ReplyMarkup, data) {
			return nil, tgerr.New(400, "DATA_INVALID")
		}
		if m.ViaBot != 0 {
			// the bot has no copy of a message sent via it
			press.Bot, press.ChatID, press.MsgID, press.InlineID = m.ViaBot, me.ID, m.ID, bots.PrivateInlineID(m.Shared)
		} else {
			// the bot's ids are its own; find its copy of the message
			theirs, ok, err := a.msgs.CopyIn(ctx, m.From, m.Shared)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, tgerr.New(400, "MESSAGE_ID_INVALID")
			}
			press.Bot, press.ChatID, press.MsgID = m.From, me.ID, theirs.ID
		}
	}
	if target, ok, err := a.bots.Get(ctx, press.Bot); err != nil {
		return nil, err
	} else if !ok {
		return nil, errBotInvalid
	} else if !target.Usable(me.ID) {
		out := &tg.MessagesBotCallbackAnswer{Alert: true}
		out.SetMessage("This bot is only available to selected users.")
		return out, nil
	}
	id, err := a.bots.Press(ctx, press)
	if err != nil {
		return nil, err
	}
	cb, answered, err := a.bots.WaitAnswer(ctx, id, bots.CallbackTimeout)
	if err != nil {
		return nil, err
	}
	if !answered {
		return nil, tgerr.New(400, "BOT_RESPONSE_TIMEOUT")
	}
	out := &tg.MessagesBotCallbackAnswer{Alert: cb.Alert, CacheTime: cb.CacheTime}
	if cb.Text != "" {
		out.SetMessage(cb.Text)
	}
	if cb.URL != "" {
		out.SetURL(cb.URL)
	}
	return out, nil
}

// startBot is the START button and t.me/bot?start=x deep links: the bot
// gets "/start x" from the person.
func (a *API) startBot(ctx context.Context, r *tg.MessagesStartBotRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	b, err := a.bot(ctx, me, r.Bot)
	if err != nil {
		return nil, err
	}
	text := "/start"
	if p := strings.TrimSpace(r.StartParam); p != "" {
		if len(p) > 64 {
			return nil, tgerr.New(400, "START_PARAM_INVALID")
		}
		text += " " + p
	}
	return a.sendTo(ctx, me, peer{user: b.UserID}, []outgoing{{text: text, randomID: r.RandomID}})
}

// webView builds the URL a mini app opens with: Telegram passes signed
// launch data in the fragment, where telegram-web-app.js reads it. No
// query_id: answerWebAppQuery isn't here yet. instance is chat_instance, which
// only direct links have.
func (a *API) webView(ctx context.Context, me account.User, b bots.Bot, raw string, start, platform string, theme tg.DataJSON, chat, instance string) (*tg.WebViewResultURL, error) {
	if !b.Usable(me.ID) {
		return nil, errBotRestricted
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, tgerr.New(400, "URL_INVALID")
	}
	first, last := splitName(me.Name)
	who := map[string]any{"id": tgUserID(me), "first_name": first, "language_code": clientLanguage(ctx), "allows_write_to_pm": true}
	if last != "" {
		who["last_name"] = last
	}
	if me.Username != "" {
		who["username"] = me.Username
	}
	if me.Premium(time.Now()) {
		who["is_premium"] = true
	}
	// images can't send the launch data, so the built-in apps get a signed link
	if me.PhotoID != 0 && b.BuiltIn() {
		who["photo_url"] = a.in.BaseURL + miniapps.PhotoURL(me.PhotoID)
	}
	user, _ := json.Marshal(who)
	fields := url.Values{"user": {string(user)}}
	if start != "" {
		fields.Set("start_param", start)
	}
	if chat != "" {
		fields.Set("chat_type", chat)
	}
	if instance != "" {
		fields.Set("chat_instance", instance)
	}
	// the built-in apps act for this session (wallet unlocks, sign-out of
	// other sessions on a number change), so the launch names it
	if _, hash, err := a.session(ctx); err == nil && hash != "" && b.BuiltIn() {
		fields.Set("gg_session", bots.SessionTag(b.WebAppKey, hash))
	}
	init := bots.InitData(b.WebAppKey, fields, time.Now())
	frag := url.Values{"tgWebAppData": {init}, "tgWebAppVersion": {"9.1"}, "tgWebAppPlatform": {platform}}
	if theme.Data != "" {
		frag.Set("tgWebAppThemeParams", theme.Data)
	}
	if start != "" {
		frag.Set("tgWebAppStartParam", start)
	}
	u.Fragment = ""
	// the client keeps SDK traffic to this origin when the owner asked for that
	return &tg.WebViewResultURL{URL: u.String() + "#" + frag.Encode(), SameOrigin: bool(b.State.Settings.SameOrigin)}, nil
}

func (a *API) requestWebView(ctx context.Context, r *tg.MessagesRequestWebViewRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	b, err := a.bot(ctx, me, r.Bot)
	if err != nil {
		return nil, err
	}
	link, ok := r.GetURL()
	if !ok || r.FromBotMenu {
		menu, err := a.bots.ChatMenu(ctx, b, me.ID)
		if err != nil {
			return nil, err
		}
		if menu == nil {
			return nil, tgerr.New(400, "URL_INVALID")
		}
		link = menu.URL
	}
	start, _ := r.GetStartParam()
	theme, _ := r.GetThemeParams()
	return a.webView(ctx, me, b, link, start, r.Platform, theme, "sender", "")
}

func (a *API) requestSimpleWebView(ctx context.Context, r *tg.MessagesRequestSimpleWebViewRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	b, err := a.bot(ctx, me, r.Bot)
	if err != nil {
		return nil, err
	}
	link, ok := r.GetURL()
	if !ok {
		if b.Menu == nil {
			return nil, tgerr.New(400, "URL_INVALID")
		}
		link = b.Menu.URL
	}
	start, _ := r.GetStartParam()
	theme, _ := r.GetThemeParams()
	return a.webView(ctx, me, b, link, start, r.Platform, theme, "", "")
}

func (a *API) requestMainWebView(ctx context.Context, r *tg.MessagesRequestMainWebViewRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	b, err := a.bot(ctx, me, r.Bot)
	if err != nil {
		return nil, err
	}
	if b.MainAppURL == "" {
		return nil, tgerr.New(400, "BOT_APP_INVALID")
	}
	start, _ := r.GetStartParam()
	theme, _ := r.GetThemeParams()
	res, err := a.webView(ctx, me, b, b.MainAppURL, start, r.Platform, theme, "sender", "")
	if err == nil {
		mode := b.MainMode()
		res.Fullsize = mode != bots.ModeCompact
		res.Fullscreen = mode == bots.ModeFullscreen
	}
	return res, err
}

// botApp finds a direct link by what the client names it with.
func (a *API) botApp(ctx context.Context, me account.User, in tg.InputBotAppClass) (bots.App, bots.Bot, error) {
	invalid := tgerr.New(400, "BOT_APP_INVALID")
	switch v := in.(type) {
	case *tg.InputBotAppShortName:
		b, err := a.bot(ctx, me, v.BotID)
		if err != nil {
			return bots.App{}, b, err
		}
		app, ok, err := a.bots.AppByName(ctx, b.UserID, v.ShortName)
		if err != nil {
			return app, b, err
		}
		if !ok {
			return app, b, invalid
		}
		return app, b, nil
	case *tg.InputBotAppID:
		app, ok, err := a.bots.AppByID(ctx, v.ID)
		if err != nil {
			return app, bots.Bot{}, err
		}
		if !ok || app.AccessHash != v.AccessHash {
			return app, bots.Bot{}, invalid
		}
		b, ok, err := a.bots.Get(ctx, app.Bot)
		if err != nil {
			return app, b, err
		}
		if !ok || b.Disabled {
			return app, b, invalid
		}
		return app, b, nil
	}
	return bots.App{}, bots.Bot{}, invalid
}

func (a *API) getBotApp(ctx context.Context, r *tg.MessagesGetBotAppRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	app, _, err := a.botApp(ctx, me, r.App)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesBotApp{App: &tg.BotApp{ID: app.ID, AccessHash: app.AccessHash, ShortName: app.ShortName, Title: app.Title,
		Description: app.Description, Photo: a.fullPhoto(ctx, app.Photo), Hash: int64(app.Version)}}, nil
}

// requestAppWebView opens a direct link: start_param from ?startapp=, and the
// chat it was opened from in the launch data.
func (a *API) requestAppWebView(ctx context.Context, r *tg.MessagesRequestAppWebViewRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	app, b, err := a.botApp(ctx, me, r.App)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	chatType, err := a.chatTypeOf(ctx, me, b.UserID, to)
	if err != nil {
		return nil, err
	}
	start, _ := r.GetStartParam()
	if len(start) > 512 {
		return nil, tgerr.New(400, "START_PARAM_INVALID")
	}
	theme, _ := r.GetThemeParams()
	res, err := a.webView(ctx, me, b, app.URL, start, r.Platform, theme, chatType, botapi.ChatInstance(b.UserID, to.channel, me.ID, to.user))
	if err == nil {
		res.Fullsize = !r.Compact
		res.Fullscreen = r.Fullscreen
	}
	return res, err
}
