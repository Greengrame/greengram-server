package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/admin"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/stars"
)

func (b *browser) call(method, path string, body any) (int, map[string]any) {
	b.t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, b.base+path, bytes.NewReader(raw))
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (s *stack) mountAdmin() {
	adm := admin.New(s.db.Pool, s.accounts, s.settings, channels.New(s.db.Pool))
	adm.StarsAdjust = s.stars.Adjust
	adm.AccessChanged = s.api.AccessChanged
	adm.AnswerAppeal = s.api.AnswerAppeal
	adm.Register(s.mux)
}

func appConfig(ctx context.Context, c *tg.Client) (map[string]float64, error) {
	res, err := c.HelpGetAppConfig(ctx, 0)
	if err != nil {
		return nil, err
	}
	cfg, ok := res.(*tg.HelpAppConfig)
	if !ok {
		return nil, fmt.Errorf("app config %T", res)
	}
	out := map[string]float64{}
	for _, v := range cfg.Config.(*tg.JSONObject).Value {
		if n, ok := v.Value.(*tg.JSONNumber); ok {
			out[v.Key] = n.Value
		}
	}
	return out, nil
}

// TestFrozenAccount: a frozen account reads but can't write, over MTProto,
// the Bot API, BotFather and the HTTP side, until it's unfrozen.
func TestFrozenAccount(t *testing.T) {
	s := start(t)
	boss := s.browser(t)
	boss.must("/api/register", map[string]string{"username": "boss", "name": "Boss", "password": "correct horse boss"})
	s.mountAdmin()
	ada := s.signUp(t, "ada")
	bob := s.signUp(t, "bob")
	botSvc := bots.New(s.db.Pool)
	ctx := context.Background()
	_, token, err := botSvc.Create(ctx, ada.id, "Echo", "frozen_echo_bot")
	if err != nil {
		t.Fatal(err)
	}

	access := fmt.Sprintf("/api/admin/users/%d/access", ada.id)
	if code, out := boss.call("PATCH", access, map[string]any{"state": "frozen", "reason": "spam"}); code != 200 || out["accountState"] != "frozen" {
		t.Fatalf("freeze: %d %v", code, out)
	}

	run(t, s.client(ada.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}}); err != nil {
			return fmt.Errorf("frozen read: %w", err)
		}
		if _, err := c.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20}); err != nil {
			return fmt.Errorf("frozen dialogs: %w", err)
		}
		_, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "hi", RandomID: 1})
		if !tgerr.Is(err, "FROZEN_METHOD_INVALID") {
			return fmt.Errorf("frozen send: %v", err)
		}
		if e, _ := tgerr.As(err); e.Code != 420 {
			return fmt.Errorf("frozen send code %d", e.Code)
		}
		_, err = c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Title: "x", Broadcast: true})
		if !tgerr.Is(err, "FROZEN_METHOD_INVALID") {
			return fmt.Errorf("frozen create channel: %v", err)
		}
		cfg, err := appConfig(ctx, c)
		if err != nil {
			return err
		}
		if cfg["freeze_until_date"] == 0 || cfg["freeze_since_date"] < float64(time.Now().Add(-time.Hour).Unix()) {
			return fmt.Errorf("frozen app config %v", cfg)
		}
		return nil
	})

	if out, code := s.botCall(t, token, "sendMessage", map[string]any{"chat_id": ada.id, "text": "hi"}); code != 403 || !strings.Contains(fmt.Sprint(out["description"]), "frozen") {
		t.Fatalf("frozen owner's bot sends: %d %v", code, out)
	}
	if out, code := s.botCall(t, token, "getMe", nil); code != 200 {
		t.Fatalf("frozen owner's bot getMe: %d %v", code, out)
	}
	if _, _, err := botSvc.Create(ctx, ada.id, "Two", "frozen_two_bot"); !errors.Is(err, bots.ErrFrozen) {
		t.Fatalf("frozen owner made a bot: %v", err)
	}
	if fz, err := botSvc.Frozen(ctx, ada.id); err != nil || !fz {
		t.Fatalf("Frozen(ada) %v %v", fz, err)
	}
	if u, _, err := s.accounts.User(ctx, ada.id); err != nil || !u.Frozen() {
		t.Fatalf("ada state %q %v", u.State, err)
	}

	if code, _ := boss.call("PATCH", access, map[string]any{"state": "active", "reason": "appeal"}); code != 200 {
		t.Fatalf("unfreeze: %d", code)
	}
	run(t, s.client(ada.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "back", RandomID: 2}); err != nil {
			return fmt.Errorf("unfrozen send: %w", err)
		}
		say(ctx, t, c, resolve(ctx, t, c, "frozen_echo_bot"), "/start")
		cfg, err := appConfig(ctx, c)
		if err == nil && cfg["freeze_until_date"] != 0 {
			err = fmt.Errorf("unfrozen app config %v", cfg)
		}
		return err
	})
	if out, code := s.botCall(t, token, "sendMessage", map[string]any{"chat_id": ada.id, "text": "hi"}); code != 200 {
		t.Fatalf("unfrozen owner's bot: %d %v", code, out)
	}
}

// TestStarsAdjustRace: retries of one request race to a single entry,
// distinct requests all land, and no balance passes 2^53-1.
func TestStarsAdjustRace(t *testing.T) {
	s := start(t)
	boss := s.signUp(t, "boss")
	bob := s.signUp(t, "bob")
	ctx := context.Background()

	var wg sync.WaitGroup
	ids := make([]string, 16)
	errs := make([]error, 16)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids[i], _, errs[i] = s.stars.Adjust(ctx, boss.id, bob.id, 7, "", "same-request-id")
		}()
	}
	wg.Wait()
	for i := range ids {
		if errs[i] != nil || ids[i] != ids[0] {
			t.Fatalf("retry %d: %q %v (first %q)", i, ids[i], errs[i], ids[0])
		}
	}
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = s.stars.Adjust(ctx, boss.id, bob.id, 1, "", fmt.Sprintf("distinct-request-%d", i))
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if bal, _ := s.stars.Balance(ctx, bob.id); bal != 7+16 {
		t.Fatalf("balance %d", bal)
	}

	if _, err := s.db.Pool.Exec(ctx, `INSERT INTO star_ledger(user_id, amount, reason, reference, created_by, created_at, transaction_id)
		VALUES ($1, $2, 'test', 'near-cap', $1, 0, 'near-cap')`, bob.id, int64(stars.MaxBalance)-23-2); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.stars.Adjust(ctx, boss.id, bob.id, 5, "", "past-the-cap")
	var e *account.Error
	if !errors.As(err, &e) || e.Message != "The resulting Stars balance is invalid." {
		t.Fatalf("past 2^53: %v", err)
	}
	if _, bal, err := s.stars.Adjust(ctx, boss.id, bob.id, 2, "", "up-to-the-cap"); err != nil || bal != stars.MaxBalance {
		t.Fatalf("to 2^53-1: %d %v", bal, err)
	}
	if _, err := s.stars.AddGift(ctx, stars.Gift{Title: "Moon", Emoji: "🌕", Stars: 1 << 53}); err == nil {
		t.Fatal("gift priced past 2^53")
	}
}

func TestBotUpdatesPrune(t *testing.T) {
	s := start(t)
	ada := s.signUp(t, "ada")
	botSvc := bots.New(s.db.Pool)
	ctx := context.Background()
	b, _, err := botSvc.Create(ctx, ada.id, "Quiet", "quiet_prune_bot")
	if err != nil {
		t.Fatal(err)
	}
	run(t, s.client(ada.storage), func(ctx context.Context, c *tg.Client) error {
		to := resolve(ctx, t, c, "quiet_prune_bot")
		say(ctx, t, c, to, "hello")
		say(ctx, t, c, to, "again")
		return nil
	})
	count := func() int {
		var n int
		s.db.Pool.QueryRow(ctx, "SELECT count(*) FROM bot_updates WHERE bot_id=$1", b.UserID).Scan(&n)
		return n
	}
	if n := count(); n != 2 {
		t.Fatalf("queued %d", n)
	}
	if _, err := botSvc.Prune(ctx); err != nil || count() != 2 {
		t.Fatalf("prune dropped fresh updates: %v %d", err, count())
	}
	s.db.Pool.Exec(ctx, "UPDATE bot_updates SET created_at = now() - interval '2 days' WHERE bot_id=$1 AND id = (SELECT min(id) FROM bot_updates WHERE bot_id=$1)", b.UserID)
	if n, err := botSvc.Prune(ctx); err != nil || n < 1 || count() != 1 {
		t.Fatalf("prune: %d %v, left %d", n, err, count())
	}
}

func TestBroadcastLimits(t *testing.T) {
	s := start(t)
	boss := s.signUp(t, "boss")
	ctx := context.Background()
	_, _, err := s.api.Broadcast(ctx, boss.id, "all", strings.Repeat("a", 4097))
	var e *account.Error
	if !errors.As(err, &e) || e.Status != 400 {
		t.Fatalf("4097 characters: %v", err)
	}
	if _, _, err := s.api.Broadcast(ctx, boss.id, "all", strings.Repeat("я", 4096)); err != nil {
		t.Fatalf("4096 characters: %v", err)
	}
}

// TestAdminBots is the panel's Bots view (admin/src/views/bots.jsx).
func TestAdminBots(t *testing.T) {
	s := start(t)
	boss := s.browser(t)
	boss.must("/api/register", map[string]string{"username": "boss", "name": "Boss", "password": "correct horse boss"})
	s.mountAdmin()
	ada := s.signUp(t, "ada")
	ctx := context.Background()
	b, token, err := bots.New(s.db.Pool).Create(ctx, ada.id, "Helper", "helper_admin_bot")
	if err != nil {
		t.Fatal(err)
	}

	code, overview := boss.call("GET", "/api/admin", nil)
	if code != 200 {
		t.Fatalf("overview %d %v", code, overview)
	}
	var mine, builtin map[string]any
	for _, x := range overview["bots"].([]any) {
		m := x.(map[string]any)
		switch {
		case m["id"] == float64(b.UserID):
			mine = m
		case m["kind"] == "botfather":
			builtin = m
		}
	}
	if mine == nil || mine["ownerId"] != float64(ada.id) || mine["kind"] != "custom" || mine["username"] != "helper_admin_bot" {
		t.Fatalf("listed %v", overview["bots"])
	}
	if builtin == nil || builtin["ownerId"] != nil {
		t.Fatalf("botfather %v", builtin)
	}
	for _, u := range overview["users"].([]any) {
		if u.(map[string]any)["id"] == float64(b.UserID) {
			t.Fatal("bot listed as a member")
		}
	}

	path := fmt.Sprintf("/api/admin/bots/%d", b.UserID)
	if code, d := boss.call("GET", path, nil); code != 200 || d["canEdit"] != true || d["name"] != "Helper" {
		t.Fatalf("detail %d %v", code, d)
	}
	if code, _ := boss.call("PATCH", path, map[string]any{"username": "not_a_bot_name"}); code != 400 {
		t.Fatalf("username without bot: %d", code)
	}
	if code, _ := boss.call("PATCH", path, map[string]any{"username": "botfather"}); code != 409 {
		t.Fatalf("reserved username: %d", code)
	}
	if code, _ := boss.call("PATCH", path, map[string]any{"token": "x"}); code != 400 {
		t.Fatalf("unknown field: %d", code)
	}
	code, d := boss.call("PATCH", path, map[string]any{"name": "Helper Two", "username": "helper_two_bot", "bio": "short", "description": "long", "disabled": true})
	if code != 200 || d["name"] != "Helper Two" || d["username"] != "helper_two_bot" || d["bio"] != "short" || d["description"] != "long" || d["disabled"] != true {
		t.Fatalf("edit %d %v", code, d)
	}
	if _, code := s.botCall(t, token, "getMe", nil); code != 401 {
		t.Fatalf("disabled bot getMe: %d", code)
	}
	if code, d := boss.call("PATCH", path, map[string]any{"disabled": false}); code != 200 || d["disabled"] != false {
		t.Fatalf("enable %d %v", code, d)
	}
	if _, code := s.botCall(t, token, "getMe", nil); code != 200 {
		t.Fatalf("enabled bot getMe: %d", code)
	}
	if code, _ := boss.call("PATCH", fmt.Sprintf("/api/admin/bots/%v", builtin["id"]), map[string]any{"name": "Mine"}); code != 409 {
		t.Fatalf("edit botfather: %d", code)
	}
	if code, _ := boss.call("GET", fmt.Sprintf("/api/admin/bots/%d", ada.id), nil); code != 404 {
		t.Fatalf("person as a bot: %d", code)
	}
	var audited int
	s.db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action='admin_bot_updated' AND target_id=$1", b.UserID).Scan(&audited)
	if audited != 2 {
		t.Fatalf("bot audit entries %d", audited)
	}
}
