package e2e

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Greengrame/greengram-server/internal/stars"
	"github.com/Greengrame/greengram-server/internal/wallet"
	"github.com/Greengrame/greengram-server/internal/web"
)

// fixedMarket is a price feed that never moves and never dials out.
type fixedMarket struct{ prices map[string]string }

func (f fixedMarket) Read() wallet.Market {
	now := time.Now().Unix()
	m := wallet.Market{Source: "Test", Currency: "USD", MaxAgeSeconds: 600, FetchedAt: &now, UpdatedAt: &now, Assets: map[string]wallet.Quote{}}
	for code, p := range f.prices {
		m.Assets[code] = wallet.Quote{Price: p, UpdatedAt: now, Source: "Test", TimestampType: "observed"}
	}
	return m
}

func (f fixedMarket) Refresh(context.Context) wallet.Market { return f.Read() }

type noCharts struct{}

func (noCharts) Refresh(context.Context, string, string) *wallet.History { return nil }

func (b *browser) mustGet(path string) map[string]any {
	b.t.Helper()
	code, out := b.get(path)
	if code != 200 {
		b.t.Fatalf("GET %s: %d %v", path, code, out)
	}
	return out
}

func (b *browser) status(path string, body any, want int) map[string]any {
	b.t.Helper()
	code, out := b.post(path, body)
	if code != want {
		b.t.Fatalf("%s: want %d, got %d %v", path, want, code, out)
	}
	return out
}

// The wallet's own passcode screens call its own API, and read codes off errors.
func TestWalletAppPasscode(t *testing.T) {
	s := start(t)
	svc := wallet.New(s.db.Pool, s.settings, fixedMarket{map[string]string{"TON": "5", "USDT": "1"}}, s.api.Notify).WithCharts(noCharts{})
	(&web.Wallet{Auth: &web.Auth{Accounts: s.accounts}, Wallet: svc}).Register(s.mux)
	ada := s.browser(t)
	ada.must("/api/register", map[string]string{"username": "ada", "name": "Ada", "password": "correct horse 9"})
	const set, verify, update, remove, challenge = "/api/walt/public-api/passcode/", "/api/walt/public-api/passcode/verify", "/api/walt/public-api/passcode/update", "/api/walt/public-api/passcode/remove", "/api/walt/public-api/challenges"
	refused := func(b *browser, path string, body map[string]any, status int, code string, left float64) {
		t.Helper()
		out := b.status(path, body, status)
		if out["code"] != code || (status != 423 && out["num_remaining_attempts"] != left) {
			t.Fatalf("%s: %v", path, out)
		}
	}
	swap := map[string]any{"from_currency": "TON", "to_currency": "USDT", "from_amount": "2"}

	ada.must(set, map[string]any{"newPasscode": "4821", "newPasscodeType": "4-digit"})
	if st := ada.mustGet("/api/walt/local/passcode/status"); st["unlocked"] != true || st["passcodeType"] != "4-digit" {
		t.Fatalf("set: %v", st)
	}
	ada.status(set, map[string]any{"newPasscode": "1111"}, 409)
	ada.must("/api/walt/local/passcode/lock", map[string]any{})

	// a challenge unlocks with the passcode, and only with it
	refused(ada, challenge, map[string]any{"operation": map[string]any{"name": "wallet-v2/passcode-settings"}}, 423, "passcode_missing", 0)
	refused(ada, challenge, map[string]any{"passcode": "0000"}, 403, "passcode_wrong", 4)
	ada.status("/api/walt/exchange/create_exchange", swap, 423)
	ada.status("/api/walt/local/defi/confirm", map[string]any{"id": "any"}, 423)
	if out := ada.must(challenge, map[string]any{"passcode": "4821"}); out["operationToken"] == nil {
		t.Fatalf("challenge: %v", out)
	}
	ada.must(challenge, map[string]any{})
	ada.must(verify, map[string]any{"passcode": "4821"})

	// changing needs the current one; an email code doesn't stand in for it
	refused(ada, update, map[string]any{"passcode": "", "emailCode": "123456", "newPasscode": "135790", "newPasscodeType": "6-digit"}, 403, "passcode_wrong", 4)
	refused(ada, update, map[string]any{"passcode": "1111", "newPasscode": "135790", "newPasscodeType": "6-digit"}, 403, "passcode_wrong", 3)
	ada.must(update, map[string]any{"passcode": "4821", "newPasscode": "135790", "newPasscodeType": "6-digit"})
	ada.must("/api/walt/local/passcode/lock", map[string]any{})
	refused(ada, verify, map[string]any{"passcode": "4821"}, 403, "passcode_wrong", 4)
	ada.must(verify, map[string]any{"passcode": "135790"})

	// turning it off needs it too, and spending stays refused until there's a new one
	refused(ada, remove, map[string]any{"passcode": "000000"}, 403, "passcode_wrong", 4)
	ada.must(remove, map[string]any{"passcode": "135790"})
	if st := ada.mustGet("/api/walt/local/passcode/status"); st["configured"] != false || st["unlocked"] != false {
		t.Fatalf("removed: %v", st)
	}
	ada.status("/api/walt/exchange/create_exchange", swap, 423)
	ada.must(set, map[string]any{"newPasscode": "7301", "newPasscodeType": "4-digit"})

	// five wrong tries lock it, the right one included, for every way in
	ada.must("/api/walt/local/passcode/lock", map[string]any{})
	for left := 4.0; left >= 0; left-- {
		refused(ada, verify, map[string]any{"passcode": "0000"}, 403, "passcode_wrong", left)
	}
	for _, path := range []string{verify, challenge, remove, update} {
		refused(ada, path, map[string]any{"passcode": "7301", "newPasscode": "1357"}, 429, "passcode_exceeded_max_attempts", 0)
	}
	if st := ada.mustGet("/api/walt/local/passcode/status"); st["unlocked"] != false || st["retryAfter"].(float64) <= 0 {
		t.Fatalf("locked: %v", st)
	}
}

func TestWalletViewFlags(t *testing.T) {
	s := start(t)
	svc := wallet.New(s.db.Pool, s.settings, fixedMarket{map[string]string{"TON": "5", "USDT": "1"}}, s.api.Notify).WithCharts(noCharts{})
	(&web.Wallet{Auth: &web.Auth{Accounts: s.accounts}, Wallet: svc}).Register(s.mux)
	ada := s.browser(t)
	ada.must("/api/register", map[string]string{"username": "ada", "name": "Ada", "password": "correct horse 9"})
	const get, set = "/api/walt/user-profile/public-api/v1/webview-settings/get", "/api/walt/user-profile/public-api/v1/webview-settings/set"
	flags := func() map[string]any {
		t.Helper()
		data, ok := ada.mustGet(get)["data"].(map[string]any)
		if !ok {
			t.Fatal("no view settings")
		}
		out, ok := data["userFlags"].(map[string]any)
		if !ok {
			t.Fatalf("view settings: %v", data)
		}
		return out
	}
	if f := flags(); f["hide_balances"] != false {
		t.Fatalf("starts hidden: %v", f)
	}
	ada.must(set, map[string]any{"changedUserFlags": map[string]any{"hide_balances": true}})
	if f := flags(); f["hide_balances"] != true {
		t.Fatalf("not kept: %v", f)
	}
	// what this server offers isn't the app's to change
	ada.must(set, map[string]any{"changedUserFlags": map[string]any{"is_custody_available": false, "hide_balances": false}})
	if f := flags(); f["hide_balances"] != false || f["is_custody_available"] != true {
		t.Fatalf("flags: %v", f)
	}
}

// The start page: only money on the DeFi account sends a plain open there. A
// new, empty wallet starts on main.
func TestWalletDefiLanding(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	ada := s.browser(t)
	id := int64(ada.must("/api/register", map[string]string{"username": "ada", "name": "Ada", "password": "correct horse 9"})["id"].(float64))
	svc := wallet.New(s.db.Pool, s.settings, fixedMarket{map[string]string{"TON": "5", "USDT": "1"}}, s.api.Notify).WithCharts(noCharts{})
	(&web.Wallet{Auth: &web.Auth{Accounts: s.accounts}, Wallet: svc}).Register(s.mux)
	check := func(name string, want bool) {
		t.Helper()
		if got := ada.mustGet("/api/walt/local/defi")["opens_here"]; got != want {
			t.Fatalf("%s: opens_here %v", name, got)
		}
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	check("new wallet", false)
	ada.must("/api/walt/local/defi/create", map[string]any{})
	check("empty DeFi account", false)
	exec("INSERT INTO crypto_accounts(user_id, asset, units) VALUES ($1, 'TON', 5000000000)", id)
	check("money on main", false)
	exec("INSERT INTO defi_balances(user_id, asset, units) VALUES ($1, 'TON', 1)", id)
	check("money on both", true)
	exec("UPDATE crypto_accounts SET units=0 WHERE user_id=$1", id)
	check("money on DeFi", true)
	exec("UPDATE defi_balances SET units=0 WHERE user_id=$1", id)
	check("all moved off", false)
}

func TestWalletRoundTrip(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	ada := s.browser(t)
	adaID := int64(ada.must("/api/register", map[string]string{"username": "ada", "name": "Ada", "password": "correct horse 9"})["id"].(float64))
	bob := s.browser(t)
	bobID := int64(bob.must("/api/register", map[string]string{"username": "bob", "name": "Bob", "password": "correct horse 9"})["id"].(float64))

	svc := wallet.New(s.db.Pool, s.settings, fixedMarket{map[string]string{"TON": "5", "USDT": "1"}}, s.api.Notify).WithCharts(noCharts{})
	starsChanged := make(chan int64, 4)
	(&web.Wallet{Auth: &web.Auth{Accounts: s.accounts}, Wallet: svc, StarsChanged: func(u int64) { starsChanged <- u }}).Register(s.mux)

	balance := func(user int64, code string) string {
		t.Helper()
		n, err := svc.Balance(ctx, user, code)
		if err != nil {
			t.Fatal(err)
		}
		return wallet.FormatAmount(n, code)
	}

	// deposits are admin credits; a request id replays, a changed payload doesn't
	credit := map[string]any{"recipientId": adaID, "asset": "TON", "amount": "10", "direction": "credit", "requestId": "credit-request-0001"}
	first := ada.must("/api/admin/crypto-transactions", credit)
	if first["balance"] != "10" {
		t.Fatalf("credit: %v", first)
	}
	if again := ada.must("/api/admin/crypto-transactions", credit); again["transactionId"] != first["transactionId"] || again["balance"] != "10" {
		t.Fatalf("replay: %v", again)
	}
	credit["amount"] = "11"
	ada.status("/api/admin/crypto-transactions", credit, 409)
	bob.status("/api/admin/crypto-transactions", map[string]any{"recipientId": bobID, "asset": "TON", "amount": "1", "direction": "credit", "requestId": "credit-request-0002"}, 403)

	// spending needs the passcode
	swap := map[string]any{"from_currency": "TON", "to_currency": "USDT", "from_amount": "2"}
	ada.status("/api/walt/exchange/create_exchange", swap, 423)
	if st := ada.must("/api/walt/local/passcode/create", map[string]any{"passcode": "123456", "confirmation": "123456"}); st["unlocked"] != true || st["passcodeType"] != "6-digit" {
		t.Fatalf("create passcode: %v", st)
	}
	ada.must("/api/walt/local/passcode/lock", map[string]any{})
	ada.status("/api/walt/exchange/create_exchange", swap, 423)
	ada.status("/api/walt/local/passcode/unlock", map[string]any{"passcode": "000000"}, 403)
	if st := ada.must("/api/walt/local/passcode/unlock", map[string]any{"passcode": "123456"}); st["unlocked"] != true {
		t.Fatalf("unlock: %v", st)
	}

	// 2 TON at $5 is 10 USDT
	q := ada.must("/api/walt/exchange/create_exchange", swap)
	if q["to_amount"] != "10" || q["rate"] != "5" || q["from_fiat_amount"] != "10" {
		t.Fatalf("swap quote: %v", q)
	}
	done := ada.must("/api/walt/exchange/submit_exchange/"+q["uid"].(string), map[string]any{})
	if done["status"] != "success" || done["from_balance"] != "8" || done["to_balance"] != "10" {
		t.Fatalf("swap: %v", done)
	}
	ada.must("/api/walt/exchange/submit_exchange/"+q["uid"].(string), map[string]any{})
	if balance(adaID, "TON") != "8" || balance(adaID, "USDT") != "10" {
		t.Fatalf("after swap %s TON %s USDT", balance(adaID, "TON"), balance(adaID, "USDT"))
	}

	// 4 USDT to Bob, once even if sent twice; 100 is more than Ada has
	tr := ada.must("/api/walt/transfers/create_transfer_request/", map[string]any{"asset": "USDT", "amount": "4", "recipient_id": bobID})
	ada.must("/api/walt/transfers/process_transfer/"+tr["id"].(string)+"/", map[string]any{})
	ada.must("/api/walt/transfers/process_transfer/"+tr["id"].(string)+"/", map[string]any{})
	if balance(adaID, "USDT") != "6" || balance(bobID, "USDT") != "4" {
		t.Fatalf("after transfer ada %s bob %s", balance(adaID, "USDT"), balance(bobID, "USDT"))
	}
	ada.status("/api/walt/transfers/create_transfer_request/", map[string]any{"asset": "USDT", "amount": "100", "recipient_id": bobID}, 400)
	if code, out := ada.get("/api/walt/withdrawals/validate_amount/?currency=USDT&amount=1&address=UQabc"); code != 409 {
		t.Fatalf("external address: %d %v", code, out)
	}
	if out := ada.mustGet("/api/walt/withdrawals/validate_amount/?currency=USDT&amount=7&mention=bob"); out["status"] != "INSUFFICIENT_FUNDS" || out["need_to_buy"] != "1" {
		t.Fatalf("validate: %v", out)
	}
	var text string
	var service bool
	if err := s.db.Pool.QueryRow(ctx, "SELECT text, action IS NOT NULL FROM messages WHERE owner_id=$1 ORDER BY id DESC LIMIT 1", bobID).Scan(&text, &service); err != nil || !service || text != "Ada sent 4 USDT via Wallet.\nTransfer reference: "+tr["id"].(string) {
		t.Fatalf("transfer message %q service=%v %v", text, service, err)
	}

	// two quotes for the whole balance paid at once: one of them has to fail
	a := ada.must("/api/walt/transfers/create_transfer_request/", map[string]any{"asset": "USDT", "amount": "6", "recipient_id": bobID})
	b := ada.must("/api/walt/transfers/create_transfer_request/", map[string]any{"asset": "USDT", "amount": "6", "recipient_id": bobID})
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i, id := range []string{a["id"].(string), b["id"].(string)} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = ada.post("/api/walt/transfers/process_transfer/"+id+"/", map[string]any{})
		}()
	}
	wg.Wait()
	if codes[0]+codes[1] != 600 || balance(adaID, "USDT") != "0" || balance(bobID, "USDT") != "10" {
		t.Fatalf("double spend: %v ada %s bob %s", codes, balance(adaID, "USDT"), balance(bobID, "USDT"))
	}

	// 500 Stars at 100/USD is $5, one TON
	sq := ada.must("/api/stars/crypto/quote", map[string]any{"asset": "TON", "stars": 500})
	if sq["amount"] != "1" {
		t.Fatalf("stars quote: %v", sq)
	}
	bought := ada.must("/api/stars/crypto/purchase", map[string]any{"quoteId": sq["quoteId"]})
	if bought["stars"] != float64(500) || bought["cryptoBalance"] != "7" || bought["starsBalance"] != float64(500) {
		t.Fatalf("stars purchase: %v", bought)
	}
	if u := <-starsChanged; u != adaID {
		t.Fatalf("balance pushed to %d", u)
	}
	if again := ada.must("/api/stars/crypto/purchase", map[string]any{"quoteId": sq["quoteId"]}); again["transactionId"] != bought["transactionId"] {
		t.Fatalf("purchase replay: %v", again)
	}
	if n, err := stars.Balance(ctx, s.db.Pool, adaID); err != nil || n != 500 || balance(adaID, "TON") != "7" {
		t.Fatalf("stars %d %v, TON %s", n, err, balance(adaID, "TON"))
	}
	ada.status("/api/stars/crypto/quote", map[string]any{"asset": "TON", "stars": 1000000}, 400)

	// what the wallet app's home screen adds up: 7 TON at $5 + nothing else
	acc := ada.mustGet("/api/walt/accounts/")
	if acc["overall_available_balance_fiat_amount"] != "35" {
		t.Fatalf("accounts total %v", acc["overall_available_balance_fiat_amount"])
	}
	hist := ada.mustGet("/api/walt/transactions/?limit=2")
	if hist["count"] != float64(6) || hist["has_next"] != true || len(hist["transactions"].([]any)) != 2 {
		t.Fatalf("history %v", hist)
	}
	if code, _ := ada.get("/api/walt/no/such/thing"); code != http.StatusNotImplemented {
		t.Fatalf("unknown walt path: %d", code)
	}
}
