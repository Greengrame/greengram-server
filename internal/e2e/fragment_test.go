package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/session"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/fragment"
	"github.com/Greengrame/greengram-server/internal/web"
)

func (b *browser) fragmentCall(method, path string, body any) (int, map[string]any) {
	b.t.Helper()
	var r *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	} else {
		r = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, b.base+path, r)
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func phoneSignUp(t *testing.T, s *stack, phone, username, pw string) (*browser, int64) {
	t.Helper()
	b := s.browser(t)
	b.must("/api/auth/start", map[string]string{"mode": "phone"})
	b.must("/api/auth/intent", map[string]string{"intent": "register"})
	b.must("/api/auth/code", map[string]string{"phone": phone})
	out := b.must("/api/auth/register", map[string]string{"firstName": username, "username": username, "password": pw})
	return b, int64(out["user"].(map[string]any)["id"].(float64))
}

// phoneSignIn gives the account id signed in, or 0 and the HTTP status.
func phoneSignIn(t *testing.T, s *stack, phone, pw string) (int64, int) {
	t.Helper()
	b := s.browser(t)
	b.must("/api/auth/start", map[string]string{"mode": "phone"})
	b.must("/api/auth/code", map[string]string{"phone": phone})
	code, out := b.post("/api/auth/password", map[string]string{"password": pw})
	if code != 200 {
		return 0, code
	}
	return int64(out["user"].(map[string]any)["id"].(float64)), code
}

func TestFragment(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	svc := fragment.New(s.db.Pool, s.secret, func() (string, bool) { return "5.5", true })
	(&web.Fragment{Auth: &web.Auth{Accounts: s.accounts}, Fragment: svc}).Register(s.mux)
	s.api.RegisterFragment(svc)

	boss, _ := phoneSignUp(t, s, "+1 555 0101", "boss", "correct horse 1")
	if out := boss.must("/api/admin/fragment/listing", map[string]any{"kind": "username", "value": "@Collect", "amount": "2.5"}); out["itemId"] != "username:collect" {
		t.Fatalf("reserve username: %v", out)
	}
	if out := boss.must("/api/admin/fragment/listing", map[string]any{"kind": "number", "value": "+888 1234 5678", "amount": 10, "countryCode": "888"}); out["itemId"] != "number:88812345678" {
		t.Fatalf("reserve number: %v", out)
	}
	boss.must("/api/admin/fragment/listing", map[string]any{"kind": "username", "value": "spare", "amount": "1"})

	// reserved names and numbers stay out of sign-up and channels
	if code, out := s.browser(t).post("/api/register", map[string]string{"username": "collect", "name": "x", "password": "correct horse 2"}); code != 409 || out["error"] != "That username is reserved on Fragment." {
		t.Fatalf("reserved username sign-up: %d %v", code, out)
	}
	reg := s.browser(t)
	reg.must("/api/auth/start", map[string]string{"mode": "phone"})
	reg.must("/api/auth/intent", map[string]string{"intent": "register"})
	reg.must("/api/auth/code", map[string]string{"phone": "+88812345678"})
	if code, out := reg.post("/api/auth/register", map[string]string{"firstName": "X", "username": "xnum", "password": "correct horse 2"}); code != 409 || out["error"] != "This number is reserved on Fragment." {
		t.Fatalf("reserved number sign-up: %d %v", code, out)
	}
	if free, err := channels.New(s.db.Pool).UsernameFree(ctx, "Collect", 0); err != nil || free {
		t.Fatalf("channel username on Fragment free=%v err=%v", free, err)
	}

	alice, aid := phoneSignUp(t, s, "+1 555 0102", "alice", "correct horse 3")
	if code, _ := alice.post("/api/admin/fragment/listing", map[string]any{"kind": "username", "value": "nope", "amount": "1"}); code != 403 {
		t.Fatalf("member reserving: %d", code)
	}
	// nobody holds @nobodys, so it isn't alice's to sell either
	if code, out := alice.post("/api/fragment/listing", map[string]any{"kind": "username", "value": "nobodys", "amount": "1"}); code != 403 {
		t.Fatalf("member listing an unowned username: %d %v", code, out)
	}
	var stray int
	s.db.Pool.QueryRow(ctx, "SELECT count(*) FROM fragment_items WHERE id='username:nobodys'").Scan(&stray)
	if stray != 0 {
		t.Fatal("unowned username got an owner")
	}
	if code, out := alice.post("/api/fragment/quote", map[string]any{"itemId": "username:collect"}); code != 400 || out["error"] != "You do not have enough GRAM in Wallet." {
		t.Fatalf("quote without GRAM: %d %v", code, out)
	}
	if _, err := s.db.Pool.Exec(ctx, "INSERT INTO crypto_accounts(user_id, asset, units) VALUES ($1, 'TON', 20000000000)", aid); err != nil {
		t.Fatal(err)
	}
	_, cat := alice.fragmentCall("GET", "/api/fragment/catalog", nil)
	if cat["balance"] != "20" || len(cat["items"].([]any)) != 3 {
		t.Fatalf("catalog: %v", cat)
	}

	q := alice.must("/api/fragment/quote", map[string]any{"itemId": "username:collect"})
	if q["amount"] != "2.5" || q["asset"] != "GRAM" {
		t.Fatalf("quote: %v", q)
	}
	paid := alice.must("/api/fragment/purchase", map[string]any{"quoteId": q["id"]})
	if paid["balance"] != "17.5" {
		t.Fatalf("purchase: %v", paid)
	}
	if again := alice.must("/api/fragment/purchase", map[string]any{"quoteId": q["id"]}); again["balance"] != "17.5" {
		t.Fatalf("replayed purchase charged again: %v", again)
	}
	q = alice.must("/api/fragment/quote", map[string]any{"itemId": "number:88812345678"})
	if paid := alice.must("/api/fragment/purchase", map[string]any{"quoteId": q["id"]}); paid["balance"] != "7.5" {
		t.Fatalf("number purchase: %v", paid)
	}
	var units string
	s.db.Pool.QueryRow(ctx, "SELECT units::text FROM crypto_accounts WHERE user_id=$1 AND asset='TON'", aid).Scan(&units)
	if units != "7500000000" {
		t.Fatalf("TON left %s", units)
	}

	// the bought number becomes the primary one; the old one still signs in
	if code, out := alice.post("/api/fragment/assign", map[string]any{"itemId": "number:88812345678", "password": "wrong password!"}); code != 400 {
		t.Fatalf("assign with a wrong password: %d %v", code, out)
	}
	if out := alice.must("/api/fragment/assign", map[string]any{"itemId": "number:88812345678", "password": "correct horse 3"}); out["assigned"] != true {
		t.Fatalf("assign: %v", out)
	}
	if id, code := phoneSignIn(t, s, "+88812345678", "correct horse 3"); id != aid {
		t.Fatalf("+888 sign-in: %d %d", id, code)
	}
	if id, code := phoneSignIn(t, s, "+15550102", "correct horse 3"); id != aid {
		t.Fatalf("old number sign-in: %d %d", id, code)
	}

	// collectible info over MTProto, from alice's own session
	storage := &session.StorageMemory{}
	run(t, s.client(storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.AuthImportAuthorization(ctx, &tg.AuthImportAuthorizationRequest{ID: aid, Bytes: []byte(alice.session())}); err != nil {
			return err
		}
		info, err := c.FragmentGetCollectibleInfo(ctx, &tg.InputCollectibleUsername{Username: "collect"})
		if err != nil {
			return err
		}
		if info.CryptoCurrency != "TON" || info.CryptoAmount != 2_500_000_000 || info.Currency != "USD" || info.Amount != 1375 || info.PurchaseDate == 0 {
			return fmt.Errorf("collectible info %+v", info)
		}
		// "Learn more" opens the item's own page
		if !strings.HasSuffix(info.URL, "/fragment/username/collect") {
			return fmt.Errorf("username url %q", info.URL)
		}
		if num, err := c.FragmentGetCollectibleInfo(ctx, &tg.InputCollectiblePhone{Phone: "+888 1234 5678"}); err != nil || !strings.HasSuffix(num.URL, "/fragment/number/88812345678") {
			return fmt.Errorf("number url: %v %+v", err, num)
		}
		if _, err := c.FragmentGetCollectibleInfo(ctx, &tg.InputCollectiblePhone{Phone: "+888 0000"}); !tgerr.Is(err, "COLLECTIBLE_NOT_FOUND") {
			return fmt.Errorf("unknown number: %v", err)
		}
		return nil
	})

	// selling the primary number needs a replacement; listed, it signs nobody in
	if code, out := alice.post("/api/fragment/listing", map[string]any{"kind": "number", "value": "+88812345678", "amount": "50", "password": "correct horse 3"}); code != 400 {
		t.Fatalf("primary sale without replacement: %d %v", code, out)
	}
	alice.must("/api/fragment/listing", map[string]any{"kind": "number", "value": "+88812345678", "amount": "50", "password": "correct horse 3", "alternativeId": "retained:number:15550102"})
	if id, code := phoneSignIn(t, s, "+88812345678", "correct horse 3"); code != 401 {
		t.Fatalf("listed number still signs in: %d", id)
	}
	if id, _ := phoneSignIn(t, s, "+15550102", "correct horse 3"); id != aid {
		t.Fatalf("replacement number sign-in: %d", id)
	}
	if code, _ := alice.fragmentCall("DELETE", "/api/fragment/listing", map[string]any{"itemId": "number:88812345678"}); code != 200 {
		t.Fatalf("unlist: %d", code)
	}
	if id, _ := phoneSignIn(t, s, "+88812345678", "correct horse 3"); id != aid {
		t.Fatalf("unlisted number sign-in: %d", id)
	}

	// an unsold reservation can be dropped, and the name is free again
	if code, out := boss.fragmentCall("DELETE", "/api/admin/fragment/listing", map[string]any{"itemId": "username:spare", "remove": true}); code != 200 {
		t.Fatalf("drop reservation: %d %v", code, out)
	}
	s.browser(t).must("/api/register", map[string]string{"username": "spare", "name": "Spare", "password": "correct horse 4"})
}
