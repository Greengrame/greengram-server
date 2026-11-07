package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/iamxvbaba/td/session"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/site"
	"github.com/Greengrame/greengram-server/internal/wallet"
	"github.com/Greengrame/greengram-server/internal/web"
)

// An app buys Stars the way Telegram's apps do: top-up options, a payment
// form whose url is the wallet's page, the page holds a price in GRAM, and
// payments.sendPaymentForm pays it. One transaction, nothing twice.
func TestStarsTopupFromApp(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	svc := wallet.New(s.db.Pool, s.settings, fixedMarket{map[string]string{"TON": "5", "USDT": "1"}}, s.api.Notify).WithCharts(noCharts{})
	(&web.Wallet{Auth: &web.Auth{Accounts: s.accounts}, Wallet: svc}).Register(s.mux)
	s.api.RegisterTopup(svc)

	// alice also has a browser session, for the web's own purchase endpoint
	ab := s.browser(t)
	alice := person{id: int64(ab.must("/api/register", map[string]string{"username": "alice", "name": "Alice", "password": "correct horse alice"})["id"].(float64)),
		storage: &session.StorageMemory{}}
	token := ab.session()
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.AuthImportAuthorization(ctx, &tg.AuthImportAuthorizationRequest{ID: alice.id, Bytes: []byte(token)})
		return err
	})
	bob := s.signUp(t, "bob")
	page := s.browser(t) // the app's payment browser: no session at all

	num := func(q string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := s.db.Pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// 10 GRAM; 500 Stars at 100 per USD is $5, one GRAM at $5
	exec("INSERT INTO crypto_accounts(user_id, asset, units) VALUES ($1, 'TON', 10000000000)", alice.id)
	starsTotal := func() int64 { return num("SELECT coalesce(sum(amount), 0) FROM star_ledger") }
	gramTotal := func() int64 {
		return num("SELECT coalesce(sum(units), 0)::bigint FROM crypto_accounts WHERE asset='TON'")
	}
	gram := func() int64 {
		return num("SELECT units::bigint FROM crypto_accounts WHERE user_id=$1 AND asset='TON'", alice.id)
	}
	paid := func(form int64) bool {
		t.Helper()
		var p bool
		if err := s.db.Pool.QueryRow(ctx, "SELECT paid FROM payment_forms WHERE id=$1", form).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	topup := func(n, cents int64) *tg.InputInvoiceStars {
		return &tg.InputInvoiceStars{Purpose: &tg.InputStorePaymentStarsTopup{Stars: n, Currency: "USD", Amount: cents}}
	}
	creds := func(quote string) *tg.InputPaymentCredentials {
		raw, _ := json.Marshal(map[string]string{"quote": quote})
		return &tg.InputPaymentCredentials{Data: tg.DataJSON{Data: string(raw)}}
	}
	// getPaymentForm for n Stars, and the page token out of its url
	form := func(ctx context.Context, c *tg.Client, n int64) (*tg.PaymentsPaymentForm, string, error) {
		res, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: topup(n, n)})
		if err != nil {
			return nil, "", err
		}
		f, ok := res.(*tg.PaymentsPaymentForm)
		if !ok {
			return nil, "", fmt.Errorf("form %T", res)
		}
		const prefix = "http://greengram.test/pay/stars/"
		if !strings.HasPrefix(f.URL, prefix) || f.NativeProvider != "" || f.Invoice.Currency != "USD" || len(f.Invoice.Prices) != 1 || f.Invoice.Prices[0].Amount != n {
			return nil, "", fmt.Errorf("form %+v", f)
		}
		return f, strings.TrimPrefix(f.URL, prefix), nil
	}
	quote := func(token, asset string) string {
		t.Helper()
		q := page.must("/api/pay/stars/"+token+"/quote", map[string]any{"asset": asset})
		return q["quoteId"].(string)
	}
	send := func(ctx context.Context, c *tg.Client, f *tg.PaymentsPaymentForm, n int64, quote string) (*tg.PaymentsPaymentResult, error) {
		res, err := c.PaymentsSendPaymentForm(ctx, &tg.PaymentsSendPaymentFormRequest{FormID: f.FormID, Invoice: topup(n, n), Credentials: creds(quote)})
		if err != nil {
			return nil, err
		}
		r, ok := res.(*tg.PaymentsPaymentResult)
		if !ok {
			return nil, fmt.Errorf("result %T", res)
		}
		return r, nil
	}
	refused := func(err error, kind string) error {
		if !tgerr.Is(err, kind) {
			return fmt.Errorf("want %s, got %v", kind, err)
		}
		return nil
	}

	var first *tg.PaymentsPaymentForm
	var firstToken, firstQuote string
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		opts, err := c.PaymentsGetStarsTopupOptions(ctx)
		if err != nil {
			return err
		}
		var found bool
		for _, o := range opts {
			if o.Stars == 500 && o.Currency == "USD" && o.Amount == 500 && !o.Extended {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("options %+v", opts)
		}
		// a price the server doesn't sell at is refused
		if _, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: topup(500, 499)}); refused(err, "INVOICE_INVALID") != nil {
			return refused(err, "INVOICE_INVALID")
		}
		if first, firstToken, err = form(ctx, c, 500); err != nil {
			return err
		}
		// nothing to pay with yet
		if _, err := send(ctx, c, first, 500, strings.Repeat("ab", 24)); refused(err, "PAYMENT_CREDENTIALS_INVALID") != nil {
			return refused(err, "PAYMENT_CREDENTIALS_INVALID")
		}
		return nil
	})

	// the page, with no session: the price in GRAM next to the balance
	got := page.mustGet("/api/pay/stars/" + firstToken)
	var ton map[string]any
	for _, a := range got["assets"].([]any) {
		if a.(map[string]any)["asset"] == "TON" {
			ton = a.(map[string]any)
		}
	}
	if got["stars"] != float64(500) || ton["amount"] != "1" || ton["enough"] != true || ton["balance"] != "10" {
		t.Fatalf("page: %v", got)
	}
	if code, _ := page.get("/api/pay/stars/" + strings.Repeat("0", 48)); code != http.StatusNotFound {
		t.Fatalf("unknown token: %d", code)
	}
	firstQuote = quote(firstToken, "TON")
	// the web's purchase endpoint doesn't take a form's quote
	ab.status("/api/stars/crypto/purchase", map[string]any{"quoteId": firstQuote}, 404)
	// nor does anyone else's app
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := send(ctx, c, first, 500, firstQuote)
		return refused(err, "FORM_ID_EMPTY")
	})

	stars0, gram0 := starsTotal(), gramTotal()
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		// the invoice has to be the form's
		if _, err := send(ctx, c, first, 501, firstQuote); refused(err, "PAYMENT_CREDENTIALS_INVALID") != nil {
			return refused(err, "PAYMENT_CREDENTIALS_INVALID")
		}
		res, err := send(ctx, c, first, 500, firstQuote)
		if err != nil {
			return err
		}
		up, ok := res.Updates.(*tg.Updates)
		if !ok || len(up.Updates) != 1 {
			return fmt.Errorf("updates %+v", res.Updates)
		}
		if b, ok := up.Updates[0].(*tg.UpdateStarsBalance); !ok || b.Balance.(*tg.StarsAmount).Amount != 500 {
			return fmt.Errorf("balance update %+v", up.Updates[0])
		}
		if bal, err := starsBalance(ctx, c); err != nil || bal != 500 {
			return fmt.Errorf("balance %d %v", bal, err)
		}
		// replays: the same form, the same quote
		if _, err := send(ctx, c, first, 500, firstQuote); refused(err, "FORM_SUBMIT_DUPLICATE") != nil {
			return refused(err, "FORM_SUBMIT_DUPLICATE")
		}
		return nil
	})
	// exactly the Stars bought came in and exactly one GRAM went out
	if starsTotal() != stars0+500 || gramTotal() != gram0-1000000000 || gram() != 9000000000 {
		t.Fatalf("totals: stars %d -> %d, gram %d -> %d", stars0, starsTotal(), gram0, gramTotal())
	}
	if n := num("SELECT count(*) FROM star_ledger WHERE user_id=$1 AND reason='crypto_purchase'", alice.id); n != 1 {
		t.Fatalf("%d ledger rows", n)
	}
	ab.status("/api/stars/crypto/purchase", map[string]any{"quoteId": firstQuote}, 404)
	page.status("/api/pay/stars/"+firstToken+"/quote", map[string]any{"asset": "TON"}, 409)
	if got := page.mustGet("/api/pay/stars/" + firstToken); got["paid"] != true {
		t.Fatalf("paid page: %v", got)
	}

	// failures roll the whole payment back: the form stays payable
	stars1, gram1 := starsTotal(), gramTotal()
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		second, tok, err := form(ctx, c, 250)
		if err != nil {
			return err
		}
		third, tok3, err := form(ctx, c, 250)
		if err != nil {
			return err
		}
		// another form's quote doesn't pay this one, even at the same price
		q3 := quote(tok3, "TON")
		if _, err := send(ctx, c, second, 250, q3); refused(err, "PAYMENT_CREDENTIALS_INVALID") != nil {
			return refused(err, "PAYMENT_CREDENTIALS_INVALID")
		}
		q := quote(tok, "TON")
		exec("UPDATE crypto_stars_quotes SET expires_at=0 WHERE id=$1", q)
		if _, err := send(ctx, c, second, 250, q); refused(err, "PAYMENT_CREDENTIALS_EXPIRED") != nil {
			return refused(err, "PAYMENT_CREDENTIALS_EXPIRED")
		}
		q = quote(tok, "TON")
		exec("UPDATE crypto_accounts SET units=1 WHERE user_id=$1 AND asset='TON'", alice.id)
		if _, err := send(ctx, c, second, 250, q); refused(err, "BALANCE_TOO_LOW") != nil {
			return refused(err, "BALANCE_TOO_LOW")
		}
		if paid(second.FormID) {
			return fmt.Errorf("a failed payment left the form paid")
		}
		exec("UPDATE crypto_accounts SET units=9000000000 WHERE user_id=$1 AND asset='TON'", alice.id)

		// two taps at once: one pays
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = send(ctx, c, third, 250, q3)
			}()
		}
		wg.Wait()
		if (errs[0] == nil) == (errs[1] == nil) {
			return fmt.Errorf("parallel sends: %v, %v", errs[0], errs[1])
		}
		for _, err := range errs {
			if err != nil && !tgerr.Is(err, "FORM_SUBMIT_DUPLICATE") {
				return fmt.Errorf("parallel loser: %v", err)
			}
		}
		return nil
	})
	if starsTotal() != stars1+250 || gramTotal() != gram1-500000000 {
		t.Fatalf("after failures: stars %d -> %d, gram %d -> %d", stars1, starsTotal(), gram1, gramTotal())
	}

	// the page itself: own look, no referrer, framed only by this origin
	m := http.NewServeMux()
	(&site.Site{}).Register(m)
	hs := httptest.NewServer(m)
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/pay/stars/" + firstToken)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "/__greengram/pay/pay.js") || resp.Header.Get("Referrer-Policy") != "no-referrer" ||
		!strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors 'self'") {
		t.Fatalf("page: %d %v", resp.StatusCode, resp.Header)
	}
	for _, f := range []string{"pay.js", "pay.css"} {
		if r, err := http.Get(hs.URL + "/__greengram/pay/" + f); err != nil || r.StatusCode != 200 {
			t.Fatalf("%s: %v", f, err)
		} else {
			r.Body.Close()
		}
	}
}
