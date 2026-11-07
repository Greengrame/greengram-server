package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

func buttonData(t *testing.T, m *tg.Message) []byte {
	t.Helper()
	markup, ok := m.ReplyMarkup.(*tg.ReplyInlineMarkup)
	if !ok || len(markup.Rows) != 1 || len(markup.Rows[0].Buttons) != 1 {
		t.Fatalf("no button on %+v", m)
	}
	cb, ok := markup.Rows[0].Buttons[0].Type.(*tg.InlineButtonTypeCallback)
	if !ok {
		t.Fatalf("button %+v", markup.Rows[0].Buttons[0])
	}
	return cb.Data
}

// A frozen account can resolve, open and write @SpamBot and nothing else; an
// appeal reaches the admins and their answer comes back through the bot.
func TestSpamBotAppeal(t *testing.T) {
	s := start(t)
	boss := s.browser(t)
	head := boss.must("/api/register", map[string]string{"username": "boss", "name": "Boss", "password": "correct horse boss"})
	bossID := int64(head["id"].(float64))
	s.mountAdmin()
	ada := s.signUp(t, "ada")
	bob := s.signUp(t, "bob")
	bg := context.Background()

	pushes := make(chan tg.UpdatesClass, 256)
	c := s.live(t, ada, pushes)
	ctx, cancel := context.WithTimeout(bg, 3*time.Minute)
	defer cancel()

	sb := resolve(ctx, t, c, "spambot")
	if !sb.Bot || !sb.Verified {
		t.Fatalf("spambot %+v", sb)
	}
	full, err := c.UsersGetFullUser(ctx, &tg.InputUser{UserID: sb.ID, AccessHash: sb.AccessHash})
	if err != nil {
		t.Fatal(err)
	}
	info, has := full.FullUser.GetBotInfo()
	if !has {
		t.Fatal("spambot has no bot info")
	}
	if _, web := info.MenuButton.(*tg.BotMenuButton); web {
		t.Fatalf("spambot has a mini app button: %+v", info.MenuButton)
	}

	say(ctx, t, c, sb, "/start")
	waitFrom(t, pushes, sb.ID, "Good news, no limits")

	access := fmt.Sprintf("/api/admin/users/%d/access", ada.id)
	if code, out := boss.call("PATCH", access, map[string]any{"state": "frozen", "reason": "spam"}); code != 200 {
		t.Fatalf("freeze: %d %v", code, out)
	}

	res, err := c.HelpGetAppConfig(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var url string
	for _, v := range res.(*tg.HelpAppConfig).Config.(*tg.JSONObject).Value {
		if str, ok := v.Value.(*tg.JSONString); ok && v.Key == "freeze_appeal_url" {
			url = str.Value
		}
	}
	if url != "https://t.me/spambot" {
		t.Fatalf("freeze_appeal_url %q", url)
	}

	// only @SpamBot, and only text
	if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "hi", RandomID: 900011}); !tgerr.Is(err, "FROZEN_METHOD_INVALID") {
		t.Fatalf("frozen send to bob: %v", err)
	}
	if _, err := c.MessagesSetTyping(ctx, &tg.MessagesSetTypingRequest{Peer: userPeer(bob.id), Action: &tg.SendMessageTypingAction{}}); !tgerr.Is(err, "FROZEN_METHOD_INVALID") {
		t.Fatalf("frozen typing to bob: %v", err)
	}
	if _, err := c.MessagesSetTyping(ctx, &tg.MessagesSetTypingRequest{Peer: &tg.InputPeerUser{UserID: sb.ID, AccessHash: sb.AccessHash}, Action: &tg.SendMessageTypingAction{}}); err != nil {
		t.Fatalf("typing to the bot: %v", err)
	}

	if _, err := c.MessagesStartBot(ctx, &tg.MessagesStartBotRequest{Bot: &tg.InputUser{UserID: sb.ID, AccessHash: sb.AccessHash},
		Peer: &tg.InputPeerUser{UserID: sb.ID, AccessHash: sb.AccessHash}, RandomID: 900012, StartParam: ""}); err != nil {
		t.Fatalf("frozen start: %v", err)
	}
	intro := waitFrom(t, pushes, sb.ID, "Your account was frozen")
	press := func(m *tg.Message) {
		t.Helper()
		_, err := c.MessagesGetBotCallbackAnswer(ctx, &tg.MessagesGetBotCallbackAnswerRequest{
			Peer: &tg.InputPeerUser{UserID: sb.ID, AccessHash: sb.AccessHash}, MsgID: m.ID, Data: buttonData(t, m)})
		if err != nil {
			t.Fatalf("frozen press: %v", err)
		}
	}
	press(intro)
	waitFrom(t, pushes, sb.ID, "Tell us why")
	say(ctx, t, c, sb, "I only posted once")
	waitFrom(t, pushes, sb.ID, "Thank you")

	var notices int
	for range 40 {
		s.db.Pool.QueryRow(bg, "SELECT count(*) FROM messages WHERE owner_id=$1 AND text LIKE 'New appeal%I only posted once%'", bossID).Scan(&notices)
		if notices == 1 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if notices != 1 {
		t.Fatalf("%d notices to the admin", notices)
	}

	listed := func() map[string]any {
		t.Helper()
		code, out := boss.call("GET", "/api/admin/appeals", nil)
		list, _ := out["appeals"].([]any)
		if code != 200 || len(list) == 0 {
			t.Fatalf("appeals %d %v", code, out)
		}
		return list[0].(map[string]any)
	}
	first := listed()
	if first["status"] != "open" || first["text"] != "I only posted once" || first["userId"] != float64(ada.id) || first["username"] != "ada" {
		t.Fatalf("appeal %v", first)
	}
	id := int64(first["id"].(float64))

	// one at a time
	say(ctx, t, c, sb, "and again")
	waitFrom(t, pushes, sb.ID, "Your appeal is with the moderators")

	if code, out := boss.call("POST", fmt.Sprintf("/api/admin/appeals/%d/reject", id), map[string]any{"reason": "We saw the logs"}); code != 200 {
		t.Fatalf("reject: %d %v", code, out)
	}
	waitFrom(t, pushes, sb.ID, "Your appeal was rejected: We saw the logs")
	if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "hi", RandomID: 900013}); !tgerr.Is(err, "FROZEN_METHOD_INVALID") {
		t.Fatalf("rejected and sending: %v", err)
	}
	if code, _ := boss.call("POST", fmt.Sprintf("/api/admin/appeals/%d/approve", id), nil); code != 409 {
		t.Fatalf("approved a decided appeal: %d", code)
	}

	// too soon to ask again
	say(ctx, t, c, sb, "/start")
	waitFrom(t, pushes, sb.ID, "Your last appeal was rejected: We saw the logs")

	if _, err := s.db.Pool.Exec(bg, "UPDATE freeze_appeals SET decided_at = now() - interval '2 days'"); err != nil {
		t.Fatal(err)
	}
	say(ctx, t, c, sb, "/start")
	press(waitFrom(t, pushes, sb.ID, "Your account was frozen"))
	waitFrom(t, pushes, sb.ID, "Tell us why")
	say(ctx, t, c, sb, "Please, it won't happen again")
	waitFrom(t, pushes, sb.ID, "Thank you")
	second := listed()
	if second["status"] != "open" || second["id"] == first["id"] {
		t.Fatalf("second appeal %v", second)
	}
	if code, out := boss.call("POST", fmt.Sprintf("/api/admin/appeals/%d/approve", int64(second["id"].(float64))), map[string]any{}); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	waitFrom(t, pushes, sb.ID, "Good news: your appeal was accepted")
	if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "free", RandomID: 900014}); err != nil {
		t.Fatalf("unfrozen send: %v", err)
	}
	if got := listed(); got["status"] != "rejected" && got["status"] != "approved" {
		t.Fatalf("list after the decision %v", got)
	}
	var actions int
	s.db.Pool.QueryRow(bg, "SELECT count(*) FROM audit_log WHERE target_id=$1 AND action IN ('appeal_rejected', 'account_active')", ada.id).Scan(&actions)
	if actions != 2 {
		t.Fatalf("%d audit entries", actions)
	}

	// settling it by hand closes the appeal
	if code, _ := boss.call("PATCH", access, map[string]any{"state": "frozen"}); code != 200 {
		t.Fatal("refreeze")
	}
	if _, err := s.db.Pool.Exec(bg, "INSERT INTO freeze_appeals(user_id, text) VALUES ($1, 'by hand')", ada.id); err != nil {
		t.Fatal(err)
	}
	if code, _ := boss.call("PATCH", access, map[string]any{"state": "active"}); code != 200 {
		t.Fatal("unfreeze")
	}
	var open int
	s.db.Pool.QueryRow(bg, "SELECT count(*) FROM freeze_appeals WHERE user_id=$1 AND status='open'", ada.id).Scan(&open)
	if open != 0 {
		t.Fatalf("%d appeals still open after unfreezing by hand", open)
	}
}

// destroy_auth_key takes the sign-in with it, like a log out. The edge test
// TestDestroyAuthKey covers the message reaching this hook: td's client wraps
// every call, so it can't send the bare top level message from here.
func TestDestroyAuthKeyEndsSession(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bg := context.Background()
	count := func() int {
		var n int
		if err := s.db.Pool.QueryRow(bg, "SELECT count(*) FROM sessions WHERE user_id=$1", alice.id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 1 {
		t.Fatalf("%d sessions before", count())
	}
	var keyID int64
	if err := s.db.Pool.QueryRow(bg, `SELECT k.id FROM auth_keys k JOIN sessions x ON x.token_hash=k.session_hash WHERE x.user_id=$1`, alice.id).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if err := s.srv.OnKeyDestroyed(bg, keyID); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Fatalf("%d sessions left after destroy_auth_key", n)
	}
}
