package e2e

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/bots"
)

// botTexts drains the bot's queue into the texts of its message updates.
func (s *stack) botTexts(t *testing.T, token string, offset *int64) []string {
	t.Helper()
	ups := s.bot(t, token, "getUpdates", map[string]any{"offset": *offset}).([]any)
	out := []string{}
	for _, u := range ups {
		m := u.(map[string]any)
		*offset = num(m["update_id"]) + 1
		if msg, ok := m["message"].(map[string]any); ok {
			out = append(out, msg["text"].(string))
		}
	}
	return out
}

func wantTexts(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if len(want) == 0 {
		want = []string{}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s: got %q, want %q", what, got, want)
	}
}

// TestBotGroupPrivacy: a new bot starts in privacy mode and gets only what is
// meant for it; off, or as an admin, it gets everything.
func TestBotGroupPrivacy(t *testing.T) {
	s := start(t)
	ada := s.signUp(t, "ada")
	svc := bots.New(s.db.Pool)
	b, token, err := svc.Create(context.Background(), ada.id, "Quiet", "quiet_test_bot")
	if err != nil {
		t.Fatal(err)
	}
	me := s.bot(t, token, "getMe", nil).(map[string]any)
	if me["can_read_all_group_messages"] != false {
		t.Fatalf("new bot reads everything: %v", me)
	}
	run(t, s.client(ada.storage), func(ctx context.Context, c *tg.Client) error {
		u := resolve(ctx, t, c, "quiet_test_bot")
		if u.BotChatHistory {
			t.Errorf("bot_chat_history set in privacy mode")
		}
		in := &tg.InputUser{UserID: u.ID, AccessHash: u.AccessHash}
		cr, err := c.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Room", Users: []tg.InputUserClass{in}})
		if err != nil {
			return err
		}
		group := channelOf(t, cr.Updates)
		chatID := -(1_000_000_000_000 + group.ID)
		send := func(text string, reply int) {
			t.Helper()
			randomID++
			r := &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: text, RandomID: randomID}
			if reply != 0 {
				r.ReplyTo = &tg.InputReplyToMessage{ReplyToMsgID: reply}
			}
			if _, err := c.MessagesSendMessage(ctx, r); err != nil {
				t.Fatalf("send %q: %v", text, err)
			}
		}
		var offset int64
		send("just talking", 0)
		send("hi @someone_else", 0)
		send("not@quiet_test_bot_really", 0)
		send("/help@other_bot", 0)
		wantTexts(t, "not for the bot", s.botTexts(t, token, &offset))

		send("/help", 0)
		send("/help@quiet_test_bot now", 0)
		send("hey @Quiet_Test_Bot look", 0)
		wantTexts(t, "for the bot", s.botTexts(t, token, &offset), "/help", "/help@quiet_test_bot now", "hey @Quiet_Test_Bot look")

		said := s.bot(t, token, "sendMessage", map[string]any{"chat_id": chatID, "text": "I'm here"}).(map[string]any)
		send("a reply to it", int(num(said["message_id"])))
		wantTexts(t, "reply", s.botTexts(t, token, &offset), "a reply to it")

		// the owner's switch applies at once
		if err := svc.SetSetting(ctx, b.UserID, "priv", false); err != nil {
			return err
		}
		send("everything now", 0)
		wantTexts(t, "privacy off", s.botTexts(t, token, &offset), "everything now")
		if me := s.bot(t, token, "getMe", nil).(map[string]any); me["can_read_all_group_messages"] != true {
			t.Errorf("getMe after turning it off: %v", me)
		}
		if u := resolve(ctx, t, c, "quiet_test_bot"); !u.BotChatHistory {
			t.Errorf("bot_chat_history not set with privacy off")
		}

		// an admin bot reads everything whatever the switch says
		if err := svc.SetSetting(ctx, b.UserID, "priv", true); err != nil {
			return err
		}
		send("quiet again", 0)
		wantTexts(t, "privacy back on", s.botTexts(t, token, &offset))
		if _, err := c.ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{Channel: inCh(group), UserID: in, AdminRights: tg.ChatAdminRights{DeleteMessages: true}}); err != nil {
			return err
		}
		send("admins hear this", 0)
		wantTexts(t, "admin", s.botTexts(t, token, &offset), "admins hear this")
		return nil
	})
}

// TestBotGroupPrivacyExisting: a bot from before the setting, written down as
// off by migration 0340, keeps getting every message and other bots' too.
func TestBotGroupPrivacyExisting(t *testing.T) {
	s := start(t)
	ada := s.signUp(t, "ada")
	ctx := context.Background()
	svc := bots.New(s.db.Pool)
	b, token, err := svc.Create(ctx, ada.id, "Old", "old_test_bot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Pool.Exec(ctx, `UPDATE bots SET state = state || jsonb_build_object('settings', coalesce(state->'settings', '{}') || '{"priv": false, "loop": true}') WHERE user_id=$1`, b.UserID); err != nil {
		t.Fatal(err)
	}
	got, _, err := svc.Get(ctx, b.UserID)
	if err != nil || got.State.GroupPrivacy() || !bool(got.State.Settings.BotToBot) {
		t.Fatalf("state %+v %v", got.State, err)
	}
	_, otherToken, err := svc.Create(ctx, ada.id, "Other", "other_test_bot")
	if err != nil {
		t.Fatal(err)
	}
	run(t, s.client(ada.storage), func(ctx context.Context, c *tg.Client) error {
		var users []tg.InputUserClass
		for _, name := range []string{"old_test_bot", "other_test_bot"} {
			u := resolve(ctx, t, c, name)
			users = append(users, &tg.InputUser{UserID: u.ID, AccessHash: u.AccessHash})
		}
		cr, err := c.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Room", Users: users})
		if err != nil {
			return err
		}
		group := channelOf(t, cr.Updates)
		randomID++
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "from a person", RandomID: randomID}); err != nil {
			return err
		}
		s.bot(t, otherToken, "sendMessage", map[string]any{"chat_id": -(1_000_000_000_000 + group.ID), "text": "from a bot"})
		var offset int64
		wantTexts(t, "old bot", s.botTexts(t, token, &offset), "from a person", "from a bot")
		return nil
	})
}

// TestBotToBot: bots don't hear each other unless one of them is in the mode and
// the message is addressed; a bot in the mode that reads all hears all of it. A
// pair that won't stop gets cut off.
func TestBotToBot(t *testing.T) {
	s := start(t)
	ada := s.signUp(t, "ada")
	svc := bots.New(s.db.Pool)
	ctx := context.Background()
	a, aToken, err := svc.Create(ctx, ada.id, "Alpha", "alpha_test_bot")
	if err != nil {
		t.Fatal(err)
	}
	b, bToken, err := svc.Create(ctx, ada.id, "Beta", "beta_test_bot")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSetting(ctx, a.UserID, "priv", false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSetting(ctx, b.UserID, "priv", false); err != nil {
		t.Fatal(err)
	}
	run(t, s.client(ada.storage), func(ctx context.Context, c *tg.Client) error {
		var users []tg.InputUserClass
		for _, name := range []string{"alpha_test_bot", "beta_test_bot"} {
			u := resolve(ctx, t, c, name)
			users = append(users, &tg.InputUser{UserID: u.ID, AccessHash: u.AccessHash})
		}
		cr, err := c.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Bots", Users: users})
		if err != nil {
			return err
		}
		group := channelOf(t, cr.Updates)
		chatID := -(1_000_000_000_000 + group.ID)
		var offset int64
		say := func(token, text string) map[string]any {
			t.Helper()
			return s.bot(t, token, "sendMessage", map[string]any{"chat_id": chatID, "text": text}).(map[string]any)
		}

		// reading all isn't enough with the mode off on both sides
		say(aToken, "one")
		say(aToken, "/two@beta_test_bot")
		wantTexts(t, "mode off", s.botTexts(t, bToken, &offset))

		// the sender's mode is enough for what it addresses, not for the rest
		if err := svc.SetSetting(ctx, a.UserID, "loop", true); err != nil {
			return err
		}
		say(aToken, "three")
		say(aToken, "/four@beta_test_bot")
		wantTexts(t, "sender's mode", s.botTexts(t, bToken, &offset), "/four@beta_test_bot")

		// a reply counts as addressing the bot
		mine := say(bToken, "five")
		s.bot(t, aToken, "sendMessage", map[string]any{"chat_id": chatID, "text": "six", "reply_to_message_id": num(mine["message_id"])})
		wantTexts(t, "reply", s.botTexts(t, bToken, &offset), "six")

		if err := svc.SetSetting(ctx, b.UserID, "loop", true); err != nil {
			return err
		}
		say(aToken, "seven")
		wantTexts(t, "receiver's mode", s.botTexts(t, bToken, &offset), "seven")

		// in private it takes both and starts by username
		if err := svc.SetSetting(ctx, b.UserID, "loop", false); err != nil {
			return err
		}
		if out, code := s.botCall(t, aToken, "sendMessage", map[string]any{"chat_id": "@beta_test_bot", "text": "psst"}); code != 400 {
			t.Errorf("private with one side off: %d %v", code, out)
		}
		if err := svc.SetSetting(ctx, b.UserID, "loop", true); err != nil {
			return err
		}
		s.bot(t, aToken, "sendMessage", map[string]any{"chat_id": "@beta_test_bot", "text": "psst"})
		wantTexts(t, "private", s.botTexts(t, bToken, &offset), "psst")

		// the guard: at most 30 a minute reach other bots in a chat. 65 sends
		// stay under the limit only if something is wrong, even across a minute.
		cr, err = c.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Loop", Users: users})
		if err != nil {
			return err
		}
		chatID = -(1_000_000_000_000 + channelOf(t, cr.Updates).ID)
		for i := range 65 {
			say(aToken, "spam "+strconv.Itoa(i))
		}
		heard := 0
		for {
			list := s.botTexts(t, bToken, &offset)
			if len(list) == 0 {
				break
			}
			heard += len(list)
		}
		if heard < 30 || heard >= 65 {
			t.Errorf("guard let %d through", heard)
		}
		return nil
	})
}

// waitUpdate polls until an update of this kind comes, skipping what's before it.
func (s *stack) waitUpdate(t *testing.T, token string, offset *int64, kind string) map[string]any {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		ups := s.bot(t, token, "getUpdates", map[string]any{"offset": *offset, "timeout": 1}).([]any)
		for _, raw := range ups {
			u := raw.(map[string]any)
			*offset = num(u["update_id"]) + 1
			if v, ok := u[kind].(map[string]any); ok {
				return v
			}
		}
	}
	t.Fatalf("no %s update", kind)
	return nil
}

// viaMessage is the message a sendInlineBotResult answered with.
func viaMessage(t *testing.T, up tg.UpdatesClass) *tg.Message {
	t.Helper()
	ups, ok := up.(*tg.Updates)
	if !ok {
		t.Fatalf("answer %T", up)
	}
	for _, x := range ups.Updates {
		switch v := x.(type) {
		case *tg.UpdateNewMessage:
			return v.Message.(*tg.Message)
		case *tg.UpdateNewChannelMessage:
			return v.Message.(*tg.Message)
		}
	}
	t.Fatalf("no message in %+v", ups.Updates)
	return nil
}

// TestBotInline: "@bot query" reaches the bot only with the mode on, the result
// goes out marked via the bot, and the bot edits it by inline_message_id.
func TestBotInline(t *testing.T) {
	s := start(t)
	ada := s.signUp(t, "ada")
	bob := s.signUp(t, "bob")
	svc := bots.New(s.db.Pool)
	b, token, err := svc.Create(context.Background(), ada.id, "Finder", "finder_test_bot")
	if err != nil {
		t.Fatal(err)
	}
	_, otherToken, err := svc.Create(context.Background(), ada.id, "Other", "other_test_bot")
	if err != nil {
		t.Fatal(err)
	}
	if me := s.bot(t, token, "getMe", nil).(map[string]any); me["supports_inline_queries"] != false {
		t.Fatalf("new bot in inline mode: %v", me)
	}
	results := []map[string]any{
		{"type": "article", "id": "one", "title": "One", "input_message_content": map[string]any{"message_text": "first"},
			"reply_markup": map[string]any{"inline_keyboard": [][]map[string]any{{{"text": "Go", "callback_data": "go"}}}}},
		{"type": "article", "id": "two", "title": "Two", "input_message_content": map[string]any{"message_text": "second"}},
	}
	type asked struct {
		res *tg.MessagesBotResults
		err error
	}
	var offset int64
	var inlineID string
	run(t, s.client(ada.storage), func(ctx context.Context, c *tg.Client) error {
		u := resolve(ctx, t, c, "finder_test_bot")
		in := &tg.InputUser{UserID: u.ID, AccessHash: u.AccessHash}
		if _, err := c.MessagesGetInlineBotResults(ctx, &tg.MessagesGetInlineBotResultsRequest{Bot: in, Peer: userPeer(bob.id), Query: "cats"}); !tgerr.Is(err, "BOT_INLINE_DISABLED") {
			t.Fatalf("mode off: %v", err)
		}
		for key, value := range map[string]any{"inline": true, "inph": "Find...", "infdb": "1"} {
			if err := svc.SetSetting(ctx, b.UserID, key, value); err != nil {
				return err
			}
		}
		if me := s.bot(t, token, "getMe", nil).(map[string]any); me["supports_inline_queries"] != true {
			t.Errorf("getMe with the mode on: %v", me)
		}
		if u = resolve(ctx, t, c, "finder_test_bot"); u.BotInlinePlaceholder != "Find..." {
			t.Errorf("placeholder %q", u.BotInlinePlaceholder)
		}
		// the person waits while the bot answers
		ask := func(peer tg.InputPeerClass, query string) *tg.MessagesBotResults {
			t.Helper()
			got := make(chan asked, 1)
			go func() {
				res, err := c.MessagesGetInlineBotResults(ctx, &tg.MessagesGetInlineBotResultsRequest{Bot: in, Peer: peer, Query: query})
				got <- asked{res, err}
			}()
			q := s.waitUpdate(t, token, &offset, "inline_query")
			if q["query"] != query {
				t.Fatalf("query %v", q)
			}
			s.bot(t, token, "answerInlineQuery", map[string]any{"inline_query_id": q["id"], "results": results})
			a := <-got
			if a.err != nil {
				t.Fatalf("results: %v", a.err)
			}
			return a.res
		}
		res := ask(userPeer(bob.id), "cats")
		if len(res.Results) != 2 || res.Results[0].(*tg.BotInlineResult).Title != "One" {
			t.Fatalf("results %+v", res.Results)
		}
		// within cache_time the bot isn't asked again
		again, err := c.MessagesGetInlineBotResults(ctx, &tg.MessagesGetInlineBotResultsRequest{Bot: in, Peer: userPeer(bob.id), Query: "cats"})
		if err != nil {
			return err
		}
		if again.QueryID != res.QueryID {
			t.Errorf("cached answer has query %d, want %d", again.QueryID, res.QueryID)
		}

		randomID++
		up, err := c.MessagesSendInlineBotResult(ctx, &tg.MessagesSendInlineBotResultRequest{Peer: userPeer(bob.id), RandomID: randomID, QueryID: res.QueryID, ID: "one"})
		if err != nil {
			return err
		}
		if m := viaMessage(t, up); m.ViaBotID != u.ID || m.Message != "first" {
			t.Errorf("sent %+v", m)
		}
		randomID++
		if _, err := c.MessagesSendInlineBotResult(ctx, &tg.MessagesSendInlineBotResultRequest{Peer: userPeer(bob.id), RandomID: randomID, QueryID: res.QueryID, ID: "nope"}); !tgerr.Is(err, "RESULT_ID_INVALID") {
			t.Errorf("unknown result: %v", err)
		}
		chosen := s.waitUpdate(t, token, &offset, "chosen_inline_result")
		if chosen["result_id"] != "one" || chosen["query"] != "cats" {
			t.Errorf("chosen %v", chosen)
		}
		inlineID, _ = chosen["inline_message_id"].(string)
		if inlineID == "" {
			t.Fatalf("a result with a keyboard needs inline_message_id: %v", chosen)
		}

		// in a group the bot hears a message sent via it, even in privacy mode
		cr, err := c.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Room", Users: []tg.InputUserClass{in}})
		if err != nil {
			return err
		}
		group := channelOf(t, cr.Updates)
		res = ask(peerCh(group), "dogs")
		randomID++
		if _, err := c.MessagesSendInlineBotResult(ctx, &tg.MessagesSendInlineBotResultRequest{Peer: peerCh(group), RandomID: randomID, QueryID: res.QueryID, ID: "two"}); err != nil {
			return err
		}
		heard := s.waitUpdate(t, token, &offset, "message")
		if via, _ := heard["via_bot"].(map[string]any); heard["text"] != "second" || via["username"] != "finder_test_bot" {
			t.Errorf("heard %v", heard)
		}
		return nil
	})

	// only article results can be sent here, and the bot is told so
	if out, code := s.botCall(t, token, "answerInlineQuery", map[string]any{"inline_query_id": "1", "results": []map[string]any{{"type": "photo", "id": "p"}}}); code != 400 {
		t.Errorf("photo result: %d %v", code, out)
	}

	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		list, err := history(ctx, c, ada.id)
		if err != nil {
			return err
		}
		if len(list) == 0 || list[0].Message != "first" || list[0].ViaBotID == 0 {
			t.Fatalf("bob's copy %+v", list)
		}
		// the press goes to the bot it came through; it has no copy of the message
		pressed := make(chan error, 1)
		go func() {
			_, err := c.MessagesGetBotCallbackAnswer(ctx, &tg.MessagesGetBotCallbackAnswerRequest{Peer: userPeer(ada.id), MsgID: list[0].ID, Data: []byte("go")})
			pressed <- err
		}()
		cb := s.waitUpdate(t, token, &offset, "callback_query")
		if cb["inline_message_id"] != inlineID || cb["message"] != nil {
			t.Errorf("callback %v", cb)
		}
		s.bot(t, token, "answerCallbackQuery", map[string]any{"callback_query_id": cb["id"], "text": "ok"})
		if err := <-pressed; err != nil {
			return err
		}
		// another bot can't touch it, the one it came through can
		if out, code := s.botCall(t, otherToken, "editMessageText", map[string]any{"inline_message_id": inlineID, "text": "stolen"}); code != 400 {
			t.Errorf("other bot's edit: %d %v", code, out)
		}
		if got := s.bot(t, token, "editMessageText", map[string]any{"inline_message_id": inlineID, "text": "changed"}); got != true {
			t.Errorf("edit answered %v", got)
		}
		if list, err = history(ctx, c, ada.id); err != nil {
			return err
		}
		if list[0].Message != "changed" {
			t.Errorf("after the edit: %q", list[0].Message)
		}
		return nil
	})
}

// TestBotDirectLink: /<bot>/<app> opens with start_param and the chat it was
// opened from in signed launch data; Same-Origin reaches the client.
func TestBotDirectLink(t *testing.T) {
	s := start(t)
	ada := s.signUp(t, "ada")
	svc := bots.New(s.db.Pool)
	bg := context.Background()
	b, _, err := svc.Create(bg, ada.id, "Shop", "shop_test_bot")
	if err != nil {
		t.Fatal(err)
	}
	draft := bots.App{Bot: b.UserID, ShortName: "store", Title: "Store", Description: "A store", URL: "https://app.test/store", Photo: 1}
	app, err := svc.CreateApp(bg, draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateApp(bg, draft); err == nil {
		t.Error("the same link twice")
	}
	for _, bad := range []bots.App{
		{Bot: b.UserID, ShortName: "1x", Title: "T", Description: "D", URL: "https://app.test/", Photo: 1},
		{Bot: b.UserID, ShortName: "plain", Title: "T", Description: "D", URL: "http://app.test/", Photo: 1},
		{Bot: b.UserID, ShortName: "nopic", Title: "T", Description: "D", URL: "https://app.test/"},
	} {
		if _, err := svc.CreateApp(bg, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	run(t, s.client(ada.storage), func(ctx context.Context, c *tg.Client) error {
		u := resolve(ctx, t, c, "shop_test_bot")
		in := &tg.InputUser{UserID: u.ID, AccessHash: u.AccessHash}
		name := &tg.InputBotAppShortName{BotID: in, ShortName: "store"}
		got, err := c.MessagesGetBotApp(ctx, &tg.MessagesGetBotAppRequest{App: name})
		if err != nil {
			return err
		}
		if a, ok := got.App.(*tg.BotApp); !ok || a.Title != "Store" || a.ShortName != "store" || a.ID != app.ID {
			t.Errorf("app %+v", got.App)
		}
		open := func(peer tg.InputPeerClass) (*tg.WebViewResultURL, url.Values) {
			t.Helper()
			res, err := c.MessagesRequestAppWebView(ctx, &tg.MessagesRequestAppWebViewRequest{Peer: peer, App: name, StartParam: "promo", Platform: "web"})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			link, err := url.Parse(res.URL)
			if err != nil {
				t.Fatal(err)
			}
			frag, err := url.ParseQuery(link.EscapedFragment())
			if err != nil {
				t.Fatal(err)
			}
			if link.Host != "app.test" || frag.Get("tgWebAppStartParam") != "promo" {
				t.Errorf("url %s", res.URL)
			}
			launch, err := url.ParseQuery(frag.Get("tgWebAppData"))
			if err != nil {
				t.Fatal(err)
			}
			return res, launch
		}
		res, launch := open(&tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash})
		if launch.Get("start_param") != "promo" || launch.Get("chat_type") != "sender" || launch.Get("chat_instance") == "" {
			t.Errorf("launch data %v", launch)
		}
		if res.SameOrigin {
			t.Error("same origin without the switch")
		}
		cr, err := c.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Room", Users: []tg.InputUserClass{in}})
		if err != nil {
			return err
		}
		if _, launch = open(peerCh(channelOf(t, cr.Updates))); launch.Get("chat_type") != "supergroup" {
			t.Errorf("from a group: %v", launch)
		}
		if err := svc.SetSetting(ctx, b.UserID, "sor", true); err != nil {
			return err
		}
		if res, _ = open(&tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash}); !res.SameOrigin {
			t.Error("same origin switch didn't reach the client")
		}
		if err := svc.DeleteApp(ctx, b.UserID, app.ID); err != nil {
			return err
		}
		if _, err := c.MessagesGetBotApp(ctx, &tg.MessagesGetBotAppRequest{App: name}); !tgerr.Is(err, "BOT_APP_INVALID") {
			t.Errorf("deleted link: %v", err)
		}
		return nil
	})
}

// TestBotRestricted: with Restrict bot usage on, only the owner and the list get
// through, in private, in groups and on every other way in.
func TestBotRestricted(t *testing.T) {
	s := start(t)
	ada := s.signUp(t, "ada")
	bob := s.signUp(t, "bob")
	eve := s.signUp(t, "eve")
	svc := bots.New(s.db.Pool)
	bg := context.Background()
	b, token, err := svc.Create(bg, ada.id, "Gate", "gate_test_bot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateApp(bg, bots.App{Bot: b.UserID, ShortName: "door", Title: "Door", Description: "A door", URL: "https://app.test/door", Photo: 1}); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{"priv": false, "inline": true} {
		if err := svc.SetSetting(bg, b.UserID, key, value); err != nil {
			t.Fatal(err)
		}
	}
	var offset int64
	var kept *tg.Message
	// before the switch: eve has a chat with it, and a button
	run(t, s.client(eve.storage), func(ctx context.Context, c *tg.Client) error {
		u := resolve(ctx, t, c, "gate_test_bot")
		say(ctx, t, c, u, "before")
		wantTexts(t, "before", s.botTexts(t, token, &offset), "before")
		s.bot(t, token, "sendMessage", map[string]any{"chat_id": eve.id, "text": "press",
			"reply_markup": map[string]any{"inline_keyboard": [][]map[string]any{{{"text": "Go", "callback_data": "go"}}}}})
		list, err := history(ctx, c, u.ID)
		if err != nil {
			return err
		}
		for _, m := range list {
			if m.Message == "press" {
				kept = m
			}
		}
		if kept == nil {
			t.Fatalf("no button in %+v", list)
		}
		return nil
	})
	if err := svc.SetSetting(bg, b.UserID, "access", true); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSetting(bg, b.UserID, "access_users", []int64{bob.id}); err != nil {
		t.Fatal(err)
	}
	var group *tg.Channel
	run(t, s.client(ada.storage), func(ctx context.Context, c *tg.Client) error {
		u := resolve(ctx, t, c, "gate_test_bot")
		say(ctx, t, c, u, "from the owner")
		cr, err := c.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Room", Users: []tg.InputUserClass{
			&tg.InputUser{UserID: u.ID, AccessHash: u.AccessHash}, &tg.InputUser{UserID: eve.id}}})
		if err != nil {
			return err
		}
		group = channelOf(t, cr.Updates)
		return nil
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		say(ctx, t, c, resolve(ctx, t, c, "gate_test_bot"), "from the list")
		return nil
	})
	run(t, s.client(eve.storage), func(ctx context.Context, c *tg.Client) error {
		u := resolve(ctx, t, c, "gate_test_bot")
		in := &tg.InputUser{UserID: u.ID, AccessHash: u.AccessHash}
		peer := &tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash}
		randomID++
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peer, Message: "let me in", RandomID: randomID}); !tgerr.Is(err, "USER_PRIVACY_RESTRICTED") {
			t.Errorf("message: %v", err)
		}
		randomID++
		if _, err := c.MessagesStartBot(ctx, &tg.MessagesStartBotRequest{Bot: in, Peer: peer, RandomID: randomID}); !tgerr.Is(err, "USER_PRIVACY_RESTRICTED") {
			t.Errorf("start: %v", err)
		}
		if _, err := c.MessagesRequestAppWebView(ctx, &tg.MessagesRequestAppWebViewRequest{Peer: peer, App: &tg.InputBotAppShortName{BotID: in, ShortName: "door"}, Platform: "web"}); !tgerr.Is(err, "USER_PRIVACY_RESTRICTED") {
			t.Errorf("direct link: %v", err)
		}
		if _, err := c.MessagesGetInlineBotResults(ctx, &tg.MessagesGetInlineBotResultsRequest{Bot: in, Peer: peer, Query: "x"}); !tgerr.Is(err, "BOT_INLINE_DISABLED") {
			t.Errorf("inline: %v", err)
		}
		a, err := c.MessagesGetBotCallbackAnswer(ctx, &tg.MessagesGetBotCallbackAnswerRequest{Peer: peer, MsgID: kept.ID, Data: []byte("go")})
		if err != nil {
			return err
		}
		if !a.Alert || a.Message == "" {
			t.Errorf("button: %+v", a)
		}
		// her words in a group stay in the group
		randomID++
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "/help@gate_test_bot", RandomID: randomID}); err != nil {
			return err
		}
		return nil
	})
	wantTexts(t, "restricted", s.botTexts(t, token, &offset), "from the owner", "from the list")
}
