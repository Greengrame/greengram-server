package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/ids"
)

// botCall is one Bot API request, the way bot libraries make them.
func (s *stack) botCall(t *testing.T, token, method string, body any) (map[string]any, int) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(s.http.URL+"/bot"+token+"/"+method, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	d := json.NewDecoder(resp.Body)
	d.UseNumber()
	if err := d.Decode(&out); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return out, resp.StatusCode
}

func (s *stack) bot(t *testing.T, token, method string, body any) any {
	t.Helper()
	out, code := s.botCall(t, token, method, body)
	if code != 200 || out["ok"] != true {
		t.Fatalf("%s: %d %v", method, code, out)
	}
	return out["result"]
}

func num(v any) int64 {
	n, _ := v.(json.Number).Int64()
	return n
}

// waitFrom waits for a new message from user `from` starting with prefix.
func waitFrom(t *testing.T, got <-chan tg.UpdatesClass, from int64, prefix string) *tg.Message {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case u := <-got:
			ups, ok := u.(*tg.Updates)
			if !ok {
				continue
			}
			for _, x := range ups.Updates {
				nm, ok := x.(*tg.UpdateNewMessage)
				if !ok {
					continue
				}
				m, ok := nm.Message.(*tg.Message)
				if !ok {
					continue
				}
				if p, ok := m.FromID.(*tg.PeerUser); ok && p.UserID == from && strings.HasPrefix(m.Message, prefix) {
					return m
				}
			}
		case <-deadline:
			t.Fatalf("no message from %d starting %q", from, prefix)
			return nil
		}
	}
}

func resolve(ctx context.Context, t *testing.T, c *tg.Client, username string) *tg.User {
	t.Helper()
	res, err := c.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: username})
	if err != nil {
		t.Fatalf("resolve @%s: %v", username, err)
	}
	for _, u := range res.Users {
		if v, ok := u.(*tg.User); ok && v.Username == username {
			return v
		}
	}
	t.Fatalf("resolve @%s: no user", username)
	return nil
}

var randomID int64 = time.Now().UnixNano()

func say(ctx context.Context, t *testing.T, c *tg.Client, to *tg.User, text string) {
	t.Helper()
	randomID++
	if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
		Peer: &tg.InputPeerUser{UserID: to.ID, AccessHash: to.AccessHash}, Message: text, RandomID: randomID,
	}); err != nil {
		t.Fatalf("send %q: %v", text, err)
	}
}

// TestBots goes the whole way: BotFather makes a bot, a person writes to
// it, the bot reads over the Bot API and answers with a keyboard, the
// button press reaches the bot and its answer comes back.
func TestBots(t *testing.T) {
	s := start(t)
	ada := s.signUp(t, "ada")
	pushes := make(chan tg.UpdatesClass, 256)
	c := s.live(t, ada, pushes)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	bf := resolve(ctx, t, c, "botfather")
	if !bf.Bot || !bf.Verified {
		t.Fatalf("botfather user %+v", bf)
	}
	say(ctx, t, c, bf, "/newbot")
	waitFrom(t, pushes, bf.ID, "Alright, a new bot.")
	say(ctx, t, c, bf, "Echo")
	waitFrom(t, pushes, bf.ID, "Good. Now let's choose a username")
	say(ctx, t, c, bf, "not_a_good_name")
	waitFrom(t, pushes, bf.ID, "Sorry, the username must end in 'bot'")
	say(ctx, t, c, bf, "echo_test_bot")
	done := waitFrom(t, pushes, bf.ID, "Done! Congratulations")
	var token string
	for _, e := range done.Entities {
		if code, ok := e.(*tg.MessageEntityCode); ok {
			token = string([]rune(done.Message)[code.Offset : code.Offset+code.Length])
		}
	}
	if !strings.Contains(token, ":") {
		t.Fatalf("no token in %q", done.Message)
	}

	if _, code := s.botCall(t, "1:nope", "getMe", nil); code != 401 {
		t.Fatalf("bad token: %d", code)
	}
	me := s.bot(t, token, "getMe", nil).(map[string]any)
	if me["username"] != "echo_test_bot" || me["is_bot"] != true || me["first_name"] != "Echo" {
		t.Fatalf("getMe %v", me)
	}
	s.bot(t, token, "setMyCommands", map[string]any{"commands": []map[string]string{{"command": "start", "description": "Say hi"}}})

	// bots can't start conversations
	if out, code := s.botCall(t, token, "sendMessage", map[string]any{"chat_id": ada.id, "text": "hi"}); code != 403 {
		t.Fatalf("unprompted message: %d %v", code, out)
	}

	echo := resolve(ctx, t, c, "echo_test_bot")
	full, err := c.UsersGetFullUser(ctx, &tg.InputUser{UserID: echo.ID, AccessHash: echo.AccessHash})
	if err != nil {
		t.Fatal(err)
	}
	info, ok := full.FullUser.GetBotInfo()
	if !ok || len(info.Commands) != 1 || info.Commands[0].Command != "start" {
		t.Fatalf("bot info %+v", full.FullUser.BotInfo)
	}
	say(ctx, t, c, echo, "/start")

	ups := s.bot(t, token, "getUpdates", map[string]any{"timeout": 10}).([]any)
	if len(ups) != 1 {
		t.Fatalf("updates %v", ups)
	}
	first := ups[0].(map[string]any)
	msg := first["message"].(map[string]any)
	chatID := num(msg["chat"].(map[string]any)["id"])
	ents, _ := msg["entities"].([]any)
	if msg["text"] != "/start" || chatID != ada.id || len(ents) != 1 || ents[0].(map[string]any)["type"] != "bot_command" {
		t.Fatalf("update %v", first)
	}
	offset := num(first["update_id"]) + 1

	sent := s.bot(t, token, "sendMessage", map[string]any{
		"chat_id": chatID, "text": "<b>Pick</b> one", "parse_mode": "HTML",
		"reply_markup": map[string]any{"inline_keyboard": [][]map[string]string{{{"text": "Yes", "callback_data": "yes"}, {"text": "Site", "url": "https://example.com"}}}},
	}).(map[string]any)
	if sent["text"] != "Pick one" {
		t.Fatalf("sent %v", sent)
	}
	got := waitFrom(t, pushes, echo.ID, "Pick one")
	kb, ok := got.ReplyMarkup.(*tg.ReplyInlineMarkup)
	if !ok || len(kb.Rows) != 1 || len(kb.Rows[0].Buttons) != 2 {
		t.Fatalf("keyboard %+v", got.ReplyMarkup)
	}
	if len(got.Entities) != 1 {
		t.Fatalf("parse_mode entities %+v", got.Entities)
	}

	// a press waits for the bot; the bot answers from getUpdates
	type answer struct {
		a   *tg.MessagesBotCallbackAnswer
		err error
	}
	pressed := make(chan answer, 1)
	go func() {
		a, err := c.MessagesGetBotCallbackAnswer(ctx, &tg.MessagesGetBotCallbackAnswerRequest{
			Peer: &tg.InputPeerUser{UserID: echo.ID, AccessHash: echo.AccessHash}, MsgID: got.ID, Data: []byte("yes"),
		})
		pressed <- answer{a, err}
	}()
	ups = s.bot(t, token, "getUpdates", map[string]any{"offset": offset, "timeout": 10}).([]any)
	if len(ups) != 1 {
		t.Fatalf("callback updates %v", ups)
	}
	cq := ups[0].(map[string]any)["callback_query"].(map[string]any)
	if cq["data"] != "yes" || num(cq["from"].(map[string]any)["id"]) != ada.id || num(cq["message"].(map[string]any)["message_id"]) != num(sent["message_id"]) {
		t.Fatalf("callback %v", cq)
	}
	s.bot(t, token, "answerCallbackQuery", map[string]any{"callback_query_id": cq["id"], "text": "you said yes"})
	res := <-pressed
	if res.err != nil || res.a.Message != "you said yes" {
		t.Fatalf("callback answer %+v %v", res.a, res.err)
	}
	if out, code := s.botCall(t, token, "answerCallbackQuery", map[string]any{"callback_query_id": cq["id"]}); code != 400 {
		t.Fatalf("second answer: %d %v", code, out)
	}
	// data the message has no button for is refused
	if _, err := c.MessagesGetBotCallbackAnswer(ctx, &tg.MessagesGetBotCallbackAnswerRequest{
		Peer: &tg.InputPeerUser{UserID: echo.ID, AccessHash: echo.AccessHash}, MsgID: got.ID, Data: []byte("no"),
	}); !tgerr.Is(err, "DATA_INVALID") {
		t.Fatalf("bad data: %v", err)
	}

	// edits land on the person's side
	s.bot(t, token, "editMessageText", map[string]any{"chat_id": chatID, "message_id": sent["message_id"], "text": "You picked yes"})
	h, err := history(ctx, c, echo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if h[0].Message != "You picked yes" || h[0].ReplyMarkup != nil {
		t.Fatalf("after edit %+v", h[0])
	}
	if out, code := s.botCall(t, token, "editMessageText", map[string]any{"chat_id": chatID, "message_id": sent["message_id"], "text": "You picked yes"}); code != 400 ||
		!strings.Contains(out["description"].(string), "not modified") {
		t.Fatalf("same edit: %d %v", code, out)
	}

	// in a group the bot sees what people write and can answer there. New bots
	// start in privacy mode: TestBotGroupPrivacy has the rest of that.
	if err := bots.New(s.db.Pool).SetSetting(ctx, ids.Server(echo.ID), "priv", false); err != nil {
		t.Fatal(err)
	}
	cr, err := c.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Bot room", Users: []tg.InputUserClass{&tg.InputUser{UserID: echo.ID, AccessHash: echo.AccessHash}}})
	if err != nil {
		t.Fatal(err)
	}
	group := channelOf(t, cr.Updates)
	randomID++
	if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "hello room", RandomID: randomID}); err != nil {
		t.Fatal(err)
	}
	offset = num(ups[0].(map[string]any)["update_id"]) + 1
	ups = s.bot(t, token, "getUpdates", map[string]any{"offset": offset, "timeout": 10, "allowed_updates": []string{"message"}}).([]any)
	if len(ups) != 1 {
		t.Fatalf("group updates %v", ups)
	}
	gm := ups[0].(map[string]any)["message"].(map[string]any)
	gchat := gm["chat"].(map[string]any)
	if gm["text"] != "hello room" || gchat["type"] != "supergroup" || num(gchat["id"]) != -(1_000_000_000_000+group.ID) || gchat["title"] != "Bot room" {
		t.Fatalf("group update %v", gm)
	}
	s.bot(t, token, "sendMessage", map[string]any{"chat_id": gchat["id"], "text": "hi room",
		"reply_parameters": map[string]any{"message_id": gm["message_id"]},
		"reply_markup":     `{"inline_keyboard":[[{"text":"Wave","callback_data":"wave"}]]}`})
	room := waitChannelText(t, pushes, "hi room")
	if room.ReplyMarkup == nil {
		t.Fatalf("group keyboard missing: %+v", room)
	}
	go func() {
		a, err := c.MessagesGetBotCallbackAnswer(ctx, &tg.MessagesGetBotCallbackAnswerRequest{Peer: peerCh(group), MsgID: room.ID, Data: []byte("wave")})
		pressed <- answer{a, err}
	}()
	offset = num(ups[0].(map[string]any)["update_id"]) + 1
	ups = s.bot(t, token, "getUpdates", map[string]any{"offset": offset, "timeout": 10}).([]any)
	if len(ups) != 1 {
		t.Fatalf("group callback updates %v", ups)
	}
	cq = ups[0].(map[string]any)["callback_query"].(map[string]any)
	if cq["data"] != "wave" || num(cq["message"].(map[string]any)["chat"].(map[string]any)["id"]) != num(gchat["id"]) {
		t.Fatalf("group callback %v", cq)
	}
	s.bot(t, token, "answerCallbackQuery", map[string]any{"callback_query_id": cq["id"], "text": "hello!", "show_alert": true})
	if res := <-pressed; res.err != nil || res.a.Message != "hello!" || !res.a.Alert {
		t.Fatalf("group callback answer %+v %v", res.a, res.err)
	}

	// a revoked token stops working
	say(ctx, t, c, bf, "/revoke")
	waitFrom(t, pushes, bf.ID, "Choose a bot.")
	say(ctx, t, c, bf, "@echo_test_bot")
	waitFrom(t, pushes, bf.ID, "Your token was replaced")
	if _, code := s.botCall(t, token, "getMe", nil); code != 401 {
		t.Fatalf("revoked token: %d", code)
	}
}

// TestAnnouncements: an admin broadcast reaches people from 777000.
func TestAnnouncements(t *testing.T) {
	s := start(t)
	admin := s.browser(t)
	u := admin.must("/api/register", map[string]string{"username": "boss", "name": "Boss", "password": "correct horse boss"})
	if u["role"] != "admin" {
		t.Fatalf("first account %v", u)
	}
	bob := s.signUp(t, "bob")
	pushes := make(chan tg.UpdatesClass, 64)
	c := s.live(t, bob, pushes)

	if code, _ := s.browser(t).post("/api/admin/broadcasts", map[string]string{"text": "x", "audience": "all"}); code != 401 {
		t.Fatalf("broadcast without a session: %d", code)
	}
	out := admin.must("/api/admin/broadcasts", map[string]string{"text": "Maintenance tonight", "audience": "all"})
	if out["recipients"].(float64) != 2 {
		t.Fatalf("broadcast %v", out)
	}
	// what admin/src/views/broadcast.jsx reads
	get := func(path string) map[string]any {
		t.Helper()
		resp, err := admin.c.Get(admin.base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out := map[string]any{}
		json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d %v", path, resp.StatusCode, out)
		}
		return out
	}
	hist := get("/api/admin/broadcasts")["broadcasts"].([]any)
	if e := hist[0].(map[string]any); len(hist) != 1 || e["actor"] != "Boss" || e["audience"] != "all" || e["recipients"] != 2.0 || e["text"] != "Maintenance tonight" || e["createdAt"].(float64) < 1 {
		t.Fatalf("history %v", hist)
	}
	var audited int
	s.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_log WHERE action='broadcast'").Scan(&audited)
	if audited != 1 {
		t.Fatalf("broadcast audit entries: %d", audited)
	}
	if r := get("/api/admin/broadcasts?audience=premium"); r["audience"] != "premium" || r["recipients"] != 0.0 {
		t.Fatalf("premium audience %v", r)
	}
	s.db.Pool.Exec(context.Background(), "UPDATE users SET premium_until=extract(epoch FROM now())::bigint + 3600 WHERE username='bob'")
	if r := get("/api/admin/broadcasts?audience=premium"); r["recipients"] != 1.0 {
		t.Fatalf("premium audience with bob %v", r)
	}
	if r := get("/api/admin/broadcasts?audience=admins"); r["recipients"] != 1.0 {
		t.Fatalf("admins audience %v", r)
	}
	s.db.Pool.Exec(context.Background(), "UPDATE users SET account_state='banned' WHERE username='bob'")
	if r := get("/api/admin/broadcasts?audience=all"); r["recipients"] != 1.0 {
		t.Fatalf("banned bob still reached %v", r)
	}
	s.db.Pool.Exec(context.Background(), "UPDATE users SET account_state='active' WHERE username='bob'")

	m := waitNew(t, pushes, "Maintenance tonight")
	if p, ok := m.PeerID.(*tg.PeerUser); !ok || p.UserID != 777000 {
		t.Fatalf("announcement peer %+v", m.PeerID)
	}
	if p, ok := m.FromID.(*tg.PeerUser); !ok || p.UserID != 777000 {
		t.Fatalf("announcement from %+v", m.FromID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dl, err := c.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, x := range dl.(*tg.MessagesDialogs).Users {
		if v, ok := x.(*tg.User); ok && v.ID == 777000 && v.FirstName == "Greengram" {
			found = true
		}
	}
	if !found {
		t.Fatalf("777000 missing from dialogs users: %+v", dl)
	}
}

// TestBotUsernameOnFragment: BotFather can't hand out a name that is up
// for sale or owned on Fragment.
func TestBotUsernameOnFragment(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	owner := s.signUp(t, "owner")
	if _, err := s.db.Pool.Exec(ctx, "INSERT INTO fragment_items(id, kind, title, units) VALUES ('username:luxurybot', 'username', '@luxurybot', 5000000000)"); err != nil {
		t.Fatal(err)
	}
	svc := bots.New(s.db.Pool)
	if err := svc.CheckUsername(ctx, "LuxuryBot"); err == nil {
		t.Fatal("a Fragment name passed the check")
	}
	if _, _, err := svc.Create(ctx, owner.id, "Lux", "luxurybot"); err == nil {
		t.Fatal("made a bot with a Fragment name")
	}
	if _, _, err := svc.Create(ctx, owner.id, "Plain", "plainbot"); err != nil {
		t.Fatal(err)
	}
}

// TestOneLongPoll: like Telegram, a second getUpdates ends the first with
// 409, so one token can't hold any number of polls open.
func TestOneLongPoll(t *testing.T) {
	s := start(t)
	owner := s.signUp(t, "owner")
	_, token, err := bots.New(s.db.Pool).Create(context.Background(), owner.id, "Poller", "pollerbot")
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan int, 1)
	go func() {
		_, code := s.botCall(t, token, "getUpdates", map[string]any{"timeout": 30})
		first <- code
	}()
	time.Sleep(500 * time.Millisecond)
	second := make(chan int, 1)
	go func() {
		_, code := s.botCall(t, token, "getUpdates", map[string]any{"timeout": 1})
		second <- code
	}()
	select {
	case code := <-first:
		if code != 409 {
			t.Fatalf("first poll: %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the first poll is still open")
	}
	if code := <-second; code != 200 {
		t.Fatalf("second poll: %d", code)
	}
}

// TestBotAllowGroups: BotFather's Allow Groups switch keeps a bot out of groups,
// not channels, and clients are told through bot_nochats.
func TestBotAllowGroups(t *testing.T) {
	s := start(t)
	ada := s.signUp(t, "ada")
	botSvc := bots.New(s.db.Pool)
	ctx := context.Background()
	b, _, err := botSvc.Create(ctx, ada.id, "Lonely", "lonely_test_bot")
	if err != nil {
		t.Fatal(err)
	}
	if err := botSvc.SetSetting(ctx, b.UserID, "grps", false); err != nil {
		t.Fatal(err)
	}
	run(t, s.client(ada.storage), func(ctx context.Context, c *tg.Client) error {
		u := resolve(ctx, t, c, "lonely_test_bot")
		if !u.BotNochats {
			t.Errorf("bot_nochats not set: %+v", u)
		}
		invite := func(broadcast bool) (*tg.MessagesInvitedUsers, error) {
			res, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: broadcast, Megagroup: !broadcast, Title: "Room"})
			if err != nil {
				return nil, err
			}
			return c.ChannelsInviteToChannel(ctx, &tg.ChannelsInviteToChannelRequest{Channel: inCh(channelOf(t, res)), Users: []tg.InputUserClass{&tg.InputUser{UserID: u.ID, AccessHash: u.AccessHash}}})
		}
		out, err := invite(false)
		if err != nil {
			return err
		}
		if len(out.MissingInvitees) != 1 {
			t.Errorf("group took the bot: %+v", out)
		}
		out, err = invite(true)
		if err != nil {
			return err
		}
		if len(out.MissingInvitees) != 0 {
			t.Errorf("channel refused the bot: %+v", out)
		}
		return nil
	})
}
