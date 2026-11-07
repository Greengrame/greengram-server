package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Quote is one asset's USD price and its 24h figures; the nullable ones
// stay null when a feed doesn't have them.
type Quote struct {
	Price                     string  `json:"price"`
	PriceChange24h            *string `json:"priceChange24h"`
	PriceChange24hPercent     *string `json:"priceChange24hPercent"`
	MarketCap                 *string `json:"marketCap"`
	MarketCapChange24hPercent *string `json:"marketCapChange24hPercent"`
	Volume24h                 *string `json:"volume24h"`
	CirculatingSupply         *string `json:"circulatingSupply"`
	TotalSupply               *string `json:"totalSupply"`
	MaxSupply                 *string `json:"maxSupply"`
	UpdatedAt                 int64   `json:"updatedAt"`
	TimestampType             string  `json:"timestampType"`
	Source                    string  `json:"source"`
	SourceURL                 string  `json:"sourceUrl"`
	Stale                     bool    `json:"stale"`
}

// Market is a snapshot of the price cache, the shape the wallet UI reads.
type Market struct {
	Source        string           `json:"source"`
	SourceURL     string           `json:"sourceUrl"`
	Currency      string           `json:"currency"`
	FetchedAt     *int64           `json:"fetchedAt"`
	UpdatedAt     *int64           `json:"updatedAt"`
	MaxAgeSeconds int64            `json:"maxAgeSeconds"`
	RefreshFailed bool             `json:"refreshFailed"`
	Stale         bool             `json:"stale"`
	Assets        map[string]Quote `json:"assets"`
}

// Fresh is the price a quote may be built on: present, positive and at
// most maxAge (capped at ten minutes) old.
func (m Market) Fresh(code string, now time.Time) (Quote, bool) {
	q, ok := m.Assets[code]
	age := int64(600)
	if m.MaxAgeSeconds > 0 {
		age = min(m.MaxAgeSeconds, 600)
	}
	cur := now.Unix()
	p, err := strconv.ParseFloat(q.Price, 64)
	if !ok || q.Stale || err != nil || math.IsInf(p, 0) || p <= 0 || q.UpdatedAt <= 0 || q.UpdatedAt < cur-age || q.UpdatedAt > cur+300 {
		return Quote{}, false
	}
	return q, true
}

// MarketSource is where the wallet gets prices. Read never blocks; Refresh
// fetches when the cache is due and returns the result.
type MarketSource interface {
	Read() Market
	Refresh(ctx context.Context) Market
}

// Provider is one price feed. stale says the cache has nothing usable, so a
// feed may retry once rather than fail the caller.
type Provider interface {
	Fetch(ctx context.Context, codes []string, stale bool) (map[string]Quote, error)
}

// Doer is the HTTP client; tests swap in a fake.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// the old fetches ran with redirect: 'error'; a 3xx comes back as not ok
var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

const (
	coinGeckoURL = "https://www.coingecko.com/"
	krakenURL    = "https://www.kraken.com/"
)

type MarketOptions struct {
	Kraken, CoinGecko Provider // nil = the real feeds over HTTP
	HTTP              Doer
	PrimaryCoinGecko  bool
	NoFallback        bool
	TTL, MaxAge       time.Duration
	Timeout           time.Duration
	Now               func() time.Time
}

// MarketCache is createCryptoMarketCache: Kraken first, CoinGecko for the
// rest, a minute between fetches, half a minute after a failure.
type MarketCache struct {
	o       MarketOptions
	primary string

	mu        sync.Mutex
	assets    map[string]Quote
	fetchedAt *int64
	next      time.Time
	failed    bool
	pending   chan struct{}
}

func NewMarket(o MarketOptions) *MarketCache {
	if o.HTTP == nil {
		o.HTTP = noRedirects
	}
	if o.TTL == 0 {
		o.TTL = time.Minute
	}
	if o.MaxAge == 0 || o.MaxAge > 10*time.Minute {
		o.MaxAge = 10 * time.Minute
	}
	if o.Timeout == 0 {
		o.Timeout = 4 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Kraken == nil {
		o.Kraken = &Kraken{HTTP: o.HTTP, Timeout: o.Timeout, Now: o.Now}
	}
	if o.CoinGecko == nil {
		o.CoinGecko = &CoinGecko{HTTP: o.HTTP, Timeout: o.Timeout, Now: o.Now}
	}
	primary := "Kraken"
	if o.PrimaryCoinGecko {
		primary = "CoinGecko"
	}
	return &MarketCache{o: o, primary: primary, assets: map[string]Quote{}}
}

func (c *MarketCache) Read() Market {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.read()
}

func (c *MarketCache) read() Market {
	now := c.o.Now()
	out := Market{Currency: "USD", FetchedAt: c.fetchedAt, MaxAgeSeconds: int64(c.o.MaxAge / time.Second), RefreshFailed: c.failed,
		Stale: true, Assets: make(map[string]Quote, len(c.assets))}
	var sources []string
	for _, a := range Assets {
		q, ok := c.assets[a.Code]
		if !ok {
			continue
		}
		q.Stale = now.Unix()-q.UpdatedAt > out.MaxAgeSeconds
		out.Stale = out.Stale && q.Stale
		out.Assets[a.Code] = q
		if out.UpdatedAt == nil || q.UpdatedAt < *out.UpdatedAt {
			at := q.UpdatedAt
			out.UpdatedAt = &at
		}
		found := false
		for _, s := range sources {
			found = found || s == q.Source
		}
		if !found {
			sources = append(sources, q.Source)
		}
	}
	out.Source = strings.Join(sources, " + ")
	if out.Source == "" {
		out.Source = c.primary
	}
	out.SourceURL = coinGeckoURL
	if out.Source == "Kraken" {
		out.SourceURL = krakenURL
	}
	return out
}

func (c *MarketCache) Refresh(ctx context.Context) Market {
	c.mu.Lock()
	if p := c.pending; p != nil {
		c.mu.Unlock()
		select {
		case <-p:
		case <-ctx.Done():
		}
		return c.Read()
	}
	if c.o.Now().Before(c.next) || os.Getenv("CRYPTO_MARKET_FETCH") == "false" {
		defer c.mu.Unlock()
		return c.read()
	}
	p := make(chan struct{})
	c.pending = p
	stale := c.read().Stale
	c.mu.Unlock()

	assets, err := c.retrieve(context.WithoutCancel(ctx), stale)

	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.o.Now()
	if err != nil {
		c.failed = true
		c.next = now.Add(min(c.o.TTL, 30*time.Second))
	} else {
		for code, q := range assets {
			if old, ok := c.assets[code]; ok && old.UpdatedAt > q.UpdatedAt {
				continue
			}
			c.assets[code] = q
		}
		at := now.Unix()
		c.fetchedAt = &at
		c.failed = false
		c.next = now.Add(c.o.TTL)
	}
	c.pending = nil
	close(p)
	return c.read()
}

func allCodes() []string {
	out := make([]string, len(Assets))
	for i, a := range Assets {
		out[i] = a.Code
	}
	return out
}

func (c *MarketCache) retrieve(ctx context.Context, stale bool) (map[string]Quote, error) {
	first, second := c.o.Kraken, c.o.CoinGecko
	if c.primary == "CoinGecko" {
		first, second = second, first
	}
	assets, err := first.Fetch(ctx, allCodes(), stale)
	if err != nil {
		if c.o.NoFallback {
			return nil, err
		}
		return second.Fetch(ctx, allCodes(), stale)
	}
	var missing []string
	for _, code := range allCodes() {
		if _, ok := assets[code]; !ok {
			missing = append(missing, code)
		}
	}
	if c.primary == "Kraken" && !c.o.NoFallback && len(missing) > 0 {
		if more, err := c.o.CoinGecko.Fetch(ctx, missing, stale); err == nil {
			for k, v := range more {
				assets[k] = v
			}
		}
	}
	return assets, nil
}

func getJSON(ctx context.Context, d Doer, timeout time.Duration, u string, out any) (int, http.Header, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := d.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, resp.Header, fmt.Errorf("wallet: %s answered %d", req.URL.Host, resp.StatusCode)
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	return resp.StatusCode, resp.Header, dec.Decode(out)
}

// KrakenPairs are crypto-kraken.js's USD pairs.
var KrakenPairs = map[string]string{"TON": "TONUSD", "USDT": "USDTZUSD", "BTC": "XXBTZUSD", "XAUT": "XAUTUSD", "ETH": "XETHZUSD",
	"XRP": "XXRPZUSD", "TRX": "TRXUSD", "BNB": "BNBUSD", "SOL": "SOLUSD", "LTC": "XLTCZUSD", "SUI": "SUIUSD", "DOGE": "XDGUSD",
	"DASH": "DASHUSD", "PI": "PIUSD", "AAVE": "AAVEUSD", "HYPE": "HYPEUSD", "LINK": "LINKUSD", "UNI": "UNIUSD"}

type Kraken struct {
	HTTP    Doer
	Timeout time.Duration
	Now     func() time.Time
}

var plainPriceRe = regexp.MustCompile(`^\d+(?:\.\d+)?$`)

// Fetch takes every pair in one call; Kraken has no market caps, so only
// the last trade price comes back.
func (k *Kraken) Fetch(ctx context.Context, _ []string, _ bool) (map[string]Quote, error) {
	pairs := make([]string, 0, len(Assets))
	for _, a := range Assets {
		// most assets aren't on Kraken and an unknown pair fails the whole call
		if pair, ok := KrakenPairs[a.Code]; ok {
			pairs = append(pairs, pair)
		}
	}
	var body struct {
		Error  []any                      `json:"error"`
		Result map[string]json.RawMessage `json:"result"`
	}
	if _, _, err := getJSON(ctx, k.HTTP, k.Timeout, "https://api.kraken.com/0/public/Ticker?pair="+url.QueryEscape(strings.Join(pairs, ",")), &body); err != nil {
		return nil, errors.New("wallet: fallback market data unavailable")
	}
	if body.Error == nil || len(body.Error) > 0 || body.Result == nil {
		return nil, errors.New("wallet: invalid fallback market data")
	}
	now := k.Now().Unix()
	out := map[string]Quote{}
	for _, a := range Assets {
		var row struct {
			C []any `json:"c"`
			V []any `json:"v"`
		}
		raw, ok := body.Result[KrakenPairs[a.Code]]
		if !ok || json.Unmarshal(raw, &row) != nil || len(row.C) == 0 || len(row.V) < 2 {
			continue
		}
		price, _ := row.C[0].(string)
		p, err := strconv.ParseFloat(price, 64)
		if len(price) > 80 || !plainPriceRe.MatchString(price) || err != nil || math.IsInf(p, 0) || p <= 0 || jsNumber(row.V[1]) <= 0 {
			continue
		}
		out[a.Code] = Quote{Price: price, UpdatedAt: now, TimestampType: "observed", Source: "Kraken", SourceURL: krakenURL}
	}
	if len(out) == 0 {
		return nil, errors.New("wallet: no fallback market data available")
	}
	return out, nil
}

// jsNumber is Number(v) for what JSON hands us; NaN compares false.
func jsNumber(v any) float64 {
	switch x := v.(type) {
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return math.NaN()
		}
		return f
	case float64:
		return x
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return math.NaN()
		}
		return f
	}
	return math.NaN()
}

type CoinGecko struct {
	HTTP    Doer
	Timeout time.Duration
	Now     func() time.Time
	Sleep   func(time.Duration)
}

// Fetch retries once, 250ms later, but only when the cache has nothing
// fresh to fall back on.
func (g *CoinGecko) Fetch(ctx context.Context, codes []string, stale bool) (map[string]Quote, error) {
	ids := make([]string, 0, len(codes))
	for _, c := range codes {
		if a, ok := Lookup(c); ok {
			ids = append(ids, a.MarketID)
		}
	}
	q := url.Values{"vs_currency": {"usd"}, "ids": {strings.Join(ids, ",")}, "sparkline": {"false"}, "precision": {"full"}}
	u := "https://api.coingecko.com/api/v3/coins/markets?" + q.Encode()
	sleep := g.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	var rows []map[string]any
	for attempt := 0; attempt < 2; attempt++ {
		rows = nil
		status, _, err := getJSON(ctx, g.HTTP, g.Timeout, u, &rows)
		if status == 0 && err != nil {
			if attempt > 0 || !stale {
				return nil, err
			}
			sleep(250 * time.Millisecond)
			continue
		}
		if status >= 500 && attempt == 0 && stale {
			sleep(250 * time.Millisecond)
			continue
		}
		if err != nil {
			return nil, errors.New("wallet: market data unavailable")
		}
		break
	}
	if rows == nil {
		return nil, errors.New("wallet: invalid market data")
	}
	now := g.Now()
	out := map[string]Quote{}
	for _, c := range codes {
		a, ok := Lookup(c)
		if !ok {
			continue
		}
		for _, row := range rows {
			if row != nil && row["id"] == a.MarketID {
				if quote, ok := geckoQuote(row, now); ok {
					out[a.Code] = quote
				}
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("wallet: no market data available")
	}
	return out, nil
}

// geckoNumber is marketNumber(): a finite JSON number as a string, else null.
func geckoNumber(v any, positive bool) *string {
	n, ok := v.(json.Number)
	if !ok {
		return nil
	}
	f, err := n.Float64()
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) || positive && f <= 0 {
		return nil
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	return &s
}

func geckoQuote(row map[string]any, now time.Time) (Quote, bool) {
	last, _ := row["last_updated"].(string)
	t, err := time.Parse(time.RFC3339, last)
	price := geckoNumber(row["current_price"], true)
	if err != nil || price == nil || t.Unix() < 1 || t.Unix() > now.Unix()+300 {
		return Quote{}, false
	}
	return Quote{Price: *price, PriceChange24h: geckoNumber(row["price_change_24h"], false),
		PriceChange24hPercent: geckoNumber(row["price_change_percentage_24h"], false), MarketCap: geckoNumber(row["market_cap"], false),
		MarketCapChange24hPercent: geckoNumber(row["market_cap_change_percentage_24h"], false), Volume24h: geckoNumber(row["total_volume"], false),
		CirculatingSupply: geckoNumber(row["circulating_supply"], false), TotalSupply: geckoNumber(row["total_supply"], false),
		MaxSupply: geckoNumber(row["max_supply"], false), UpdatedAt: t.Unix(), Source: "CoinGecko", SourceURL: coinGeckoURL, TimestampType: "provider"}, true
}
