package wallet

import (
	"context"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"
)

func n(s string) *big.Int { return bigOf(s) }

func TestAmounts(t *testing.T) {
	for _, c := range []struct{ in, code, units string }{
		{"1.5", "TON", "1500000000"}, {"0.000000001", "TON", "1"}, {"12", "USDT", "12000000"}, {"1", "ETH", "1000000000000000000"},
	} {
		got, ok := ParseAmount(c.in, c.code)
		if !ok || got.String() != c.units {
			t.Fatalf("ParseAmount(%s %s) = %v %v", c.in, c.code, got, ok)
		}
		if back := FormatAmount(got, c.code); back != c.in {
			t.Fatalf("FormatAmount(%s) = %s", c.units, back)
		}
	}
	for _, bad := range []string{"1.0000000001", "-1", "1e3", ".5", ""} {
		if _, ok := ParseAmount(bad, "TON"); ok {
			t.Fatalf("ParseAmount(%q) accepted", bad)
		}
	}
	if _, ok := ParseAmount("1", "DOGGO"); ok {
		t.Fatal("unknown asset accepted")
	}
	if got, _ := ParseAmount("2", "GRAM"); got.String() != "2000000000" {
		t.Fatal("GRAM is TON")
	}
	if FormatAmount(n("-5"), "TON") != "-0.000000005" {
		t.Fatal(FormatAmount(n("-5"), "TON"))
	}
}

func TestStarsPrice(t *testing.T) {
	// 100 Stars at 100/USD is $1, 0.2 TON at $5
	if u, err := CryptoUnitsForStars(100, 100, "5", 9); err != nil || u.String() != "200000000" {
		t.Fatal(u, err)
	}
	// rounds up: 1/300 TON
	if u, _ := CryptoUnitsForStars(1, 100, "3", 9); u.String() != "3333334" {
		t.Fatal(u)
	}
	if u, _ := CryptoUnitsForStars(1, 100, "1.2e-3", 6); u.String() != "8333334" {
		t.Fatal(u)
	}
	if _, err := CryptoUnitsForStars(0, 100, "5", 9); err == nil {
		t.Fatal("zero stars")
	}
	if _, err := CryptoUnitsForStars(1, 100, "0", 9); err == nil {
		t.Fatal("zero price")
	}
	if m, err := MaxStarsForCrypto(n("200000000"), 100, "5", 9); err != nil || m != 100 {
		t.Fatal(m, err)
	}
	if m, _ := MaxStarsForCrypto(n("199999999"), 100, "5", 9); m != 99 {
		t.Fatal(m)
	}
}

func TestSwap(t *testing.T) {
	// 1 TON at $5 to USDT at $1
	if u, err := SwapUnits(n("1000000000"), "5", "1", 9, 6, false); err != nil || u.String() != "5000000" {
		t.Fatal(u, err)
	}
	if u, _ := SwapUnits(n("5000000"), "5", "1", 9, 6, true); u.String() != "1000000000" {
		t.Fatal(u)
	}
	// floor when sending, ceil in receive mode
	if u, _ := SwapUnits(n("1"), "1", "3", 6, 6, false); u.String() != "0" {
		t.Fatal(u)
	}
	if u, _ := SwapUnits(n("1"), "3", "1", 6, 6, true); u.String() != "1" {
		t.Fatal(u)
	}
	if r, _ := SwapRate("5", "1"); r != "5" {
		t.Fatal(r)
	}
	if r, _ := SwapRate("1", "3"); r != "0.333333333333333333333333" {
		t.Fatal(r)
	}
	if _, err := SwapRate("0", "1"); err == nil {
		t.Fatal("zero price")
	}
}

func TestMarketAmounts(t *testing.T) {
	if v, _ := MultiplyMarketAmount("1.5", "2.25"); v != "3.375" {
		t.Fatal(v)
	}
	if v, _ := MultiplyMarketAmount("10", "1.2e-7"); v != "0.0000012" {
		t.Fatal(v)
	}
	if v, _ := MultiplyMarketAmount("2", "3e2"); v != "600" {
		t.Fatal(v)
	}
	if v, _ := SumMarketAmounts([]string{"1.5", "0.25", "2"}); v != "3.75" {
		t.Fatal(v)
	}
	if v, ok := SumMarketAmounts(nil); !ok || v != "0" {
		t.Fatal(v)
	}
	if _, ok := SumMarketAmounts([]string{"x"}); ok {
		t.Fatal("bad amount summed")
	}
}

type fakeHTTP func(*http.Request) (int, string)

func (f fakeHTTP) Do(r *http.Request) (*http.Response, error) {
	code, body := f(r)
	if code == 0 {
		return nil, errors.New("network down")
	}
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

var clock = time.Unix(1800000000, 0)

func TestKraken(t *testing.T) {
	k := &Kraken{Timeout: time.Second, Now: func() time.Time { return clock }, HTTP: fakeHTTP(func(r *http.Request) (int, string) {
		if !strings.Contains(r.URL.RawQuery, "TONUSD") {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		return 200, `{"error":[],"result":{"TONUSD":{"c":["5.1","1"],"v":["1","2"]},"XXBTZUSD":{"c":["abc","1"],"v":["1","2"]},"SOLUSD":{"c":["100","1"],"v":["1","0"]}}}`
	})}
	got, err := k.Fetch(context.Background(), nil, false)
	if err != nil || len(got) != 1 || got["TON"].Price != "5.1" || got["TON"].Source != "Kraken" || got["TON"].UpdatedAt != clock.Unix() {
		t.Fatal(got, err)
	}
	k.HTTP = fakeHTTP(func(*http.Request) (int, string) { return 200, `{"error":["EGeneral:Too many requests"],"result":{}}` })
	if _, err := k.Fetch(context.Background(), nil, false); err == nil {
		t.Fatal("kraken error accepted")
	}
}

func TestCoinGecko(t *testing.T) {
	calls := 0
	g := &CoinGecko{Timeout: time.Second, Now: func() time.Time { return clock }, Sleep: func(time.Duration) {}, HTTP: fakeHTTP(func(r *http.Request) (int, string) {
		calls++
		if calls == 1 {
			return 502, ""
		}
		return 200, `[{"id":"the-open-network","current_price":5.25,"price_change_percentage_24h":-1.5,"market_cap":null,"last_updated":"2027-01-15T08:00:00.000Z"},
			{"id":"bitcoin","current_price":0,"last_updated":"2027-01-15T08:00:00.000Z"}]`
	})}
	got, err := g.Fetch(context.Background(), []string{"TON", "BTC"}, true)
	if err != nil || calls != 2 || len(got) != 1 || got["TON"].Price != "5.25" || *got["TON"].PriceChange24hPercent != "-1.5" || got["TON"].MarketCap != nil {
		t.Fatal(got, err, calls)
	}
	// with fresh prices cached a 5xx is not retried
	calls = 0
	if _, err := g.Fetch(context.Background(), []string{"TON"}, false); err == nil || calls != 1 {
		t.Fatal(err, calls)
	}
}

type fakeProvider struct {
	out   map[string]Quote
	err   error
	calls int
	codes []string
}

func (f *fakeProvider) Fetch(_ context.Context, codes []string, _ bool) (map[string]Quote, error) {
	f.calls++
	f.codes = codes
	return f.out, f.err
}

func TestMarketCache(t *testing.T) {
	now := clock
	kraken := &fakeProvider{out: map[string]Quote{"TON": {Price: "5", UpdatedAt: now.Unix(), Source: "Kraken"}}}
	gecko := &fakeProvider{out: map[string]Quote{"BTC": {Price: "60000", UpdatedAt: now.Unix(), Source: "CoinGecko"}}}
	c := NewMarket(MarketOptions{Kraken: kraken, CoinGecko: gecko, Now: func() time.Time { return now }})
	m := c.Refresh(context.Background())
	if _, ok := m.Fresh("TON", now); !ok || m.Source != "Kraken + CoinGecko" || len(gecko.codes) != len(Assets)-1 {
		t.Fatalf("%+v %v", m, gecko.codes)
	}
	// within the minute the cache answers
	c.Refresh(context.Background())
	if kraken.calls != 1 {
		t.Fatal("refetched inside ttl")
	}
	// Kraken down: CoinGecko alone
	now = now.Add(2 * time.Minute)
	kraken.err = errors.New("down")
	gecko.out = map[string]Quote{"TON": {Price: "6", UpdatedAt: now.Unix(), Source: "CoinGecko"}}
	m = c.Refresh(context.Background())
	if q, ok := m.Fresh("TON", now); !ok || q.Price != "6" || m.RefreshFailed {
		t.Fatalf("%+v", m)
	}
	// everything down: old prices age out after ten minutes
	gecko.err = errors.New("down")
	now = now.Add(11 * time.Minute)
	m = c.Refresh(context.Background())
	if _, ok := m.Fresh("TON", now); ok || !m.RefreshFailed || !m.Stale {
		t.Fatalf("%+v", m)
	}
}
