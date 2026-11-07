package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
)

type Point struct {
	Timestamp int64   `json:"timestamp"`
	Rate      float64 `json:"rate"`
}

// History is one asset's price history per chart period.
type History struct {
	Charts       map[string][]Point `json:"charts"`
	Source       string             `json:"source"`
	SourceURL    string             `json:"sourceUrl"`
	Currency     string             `json:"currency"`
	UpdatedAt    int64              `json:"updatedAt"`
	FetchedAt    int64              `json:"fetchedAt"`
	Timezone     string             `json:"timezone"`
	HistoryStart int64              `json:"historyStart"`
	Stale        bool               `json:"stale"`
}

// ChartSource is where coin pages get their price history; nil for an
// asset or fiat it has nothing for.
type ChartSource interface {
	Refresh(ctx context.Context, code, fiat string) *History
}

var chartPeriods = []struct {
	name string
	days int64
}{{"day", 1}, {"week", 7}, {"month", 30}, {"year", 365}, {"all", 365}}

type chartEntry struct {
	value   *History
	failed  bool
	expires time.Time
	pending chan struct{}
}

// ChartCache is createCryptoChartCache: Kraken OHLC for most coins,
// CoinGecko market_chart for the four whose Kraken pairs are thin, five
// minutes between fetches.
type ChartCache struct {
	HTTP    Doer
	Now     func() time.Time
	TTL     time.Duration
	Timeout time.Duration

	mu       sync.Mutex
	entries  map[string]*chartEntry
	backoff  time.Time // CoinGecko said 429
	defaults sync.Once
}

func (c *ChartCache) init() {
	c.defaults.Do(func() {
		if c.HTTP == nil {
			c.HTTP = noRedirects
		}
		if c.Now == nil {
			c.Now = time.Now
		}
		if c.TTL == 0 {
			c.TTL = 5 * time.Minute
		}
		if c.Timeout == 0 {
			c.Timeout = 4 * time.Second
		}
		c.entries = map[string]*chartEntry{}
	})
}

func (c *ChartCache) read(e *chartEntry) *History {
	if e == nil || e.value == nil {
		return nil
	}
	now := c.Now()
	h := *e.value
	h.Charts = make(map[string][]Point, len(e.value.Charts))
	for k, v := range e.value.Charts {
		h.Charts[k] = append([]Point{}, v...)
	}
	h.Stale = e.failed || !now.Before(e.expires) || now.Unix()-h.UpdatedAt > int64(c.TTL/time.Second)
	return &h
}

func (c *ChartCache) Refresh(ctx context.Context, code, fiat string) *History {
	c.init()
	a, ok := Lookup(code)
	if !ok || a.Code != code || fiat != "USD" {
		return nil
	}
	key := code + ":" + fiat
	c.mu.Lock()
	e := c.entries[key]
	if e != nil && e.pending != nil {
		p := e.pending
		c.mu.Unlock()
		select {
		case <-p:
		case <-ctx.Done():
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.read(e)
	}
	if e != nil && c.Now().Before(e.expires) || os.Getenv("CRYPTO_MARKET_FETCH") == "false" {
		defer c.mu.Unlock()
		return c.read(e)
	}
	if e == nil {
		e = &chartEntry{}
		c.entries[key] = e
	}
	p := make(chan struct{})
	e.pending = p
	var previous *History
	if e.value != nil {
		previous = e.value
	}
	c.mu.Unlock()

	value, failed := c.fetch(context.WithoutCancel(ctx), a, previous)

	c.mu.Lock()
	defer c.mu.Unlock()
	short := min(c.TTL, time.Minute)
	switch {
	case value == nil:
		e.failed = true
		e.expires = c.Now().Add(short)
	default:
		e.value, e.failed = value, failed
		e.expires = c.Now().Add(c.TTL)
		if failed {
			e.expires = c.Now().Add(short)
		}
	}
	e.pending = nil
	close(p)
	return c.read(e)
}

// fetch builds the new history; nil when nothing came back at all.
func (c *ChartCache) fetch(ctx context.Context, a Asset, previous *History) (*History, bool) {
	var day, history []Point
	source, failed := "CoinGecko", false
	pair, hasPair := KrakenPairs[a.Code]
	kraken := func() (d, h []Point, ok int, failed bool) {
		var wg sync.WaitGroup
		var errD, errH error
		wg.Add(2)
		go func() { defer wg.Done(); d, errD = c.kraken(ctx, pair, 60) }()
		go func() { defer wg.Done(); h, errH = c.kraken(ctx, pair, 1440) }()
		wg.Wait()
		if errD == nil {
			ok++
		}
		if errH == nil {
			ok++
		}
		return d, h, ok, errD != nil || errH != nil
	}
	// Kraken's TON, USDT, BTC and XAUT pairs are thin; CoinGecko first there
	if hasPair && a.Code != "TON" && a.Code != "USDT" && a.Code != "BTC" && a.Code != "XAUT" {
		if d, h, ok, f := kraken(); ok > 0 {
			source, day, history, failed = "Kraken", d, h, f
		}
	}
	if source != "Kraken" {
		var err error
		if day, err = c.gecko(ctx, a.MarketID, 1); err != nil {
			failed = true
		}
		if history, err = c.gecko(ctx, a.MarketID, 365); err != nil {
			failed = true
		}
	}
	if source != "Kraken" && hasPair && (len(day) == 0 || len(history) == 0) {
		have := 0
		if len(day) > 0 {
			have++
		}
		if len(history) > 0 {
			have++
		}
		if d, h, ok, f := kraken(); ok > have {
			source, day, history, failed = "Kraken", d, h, f
		}
	}
	var prev map[string][]Point
	if previous != nil && previous.Source == source {
		prev = previous.Charts
	}
	now := c.Now().Unix()
	charts := map[string][]Point{}
	var latest, start int64
	for _, period := range chartPeriods {
		points := history
		if period.name == "day" {
			points = day
		}
		if len(points) == 0 && prev != nil {
			points = prev[period.name]
		}
		if len(points) == 0 {
			if period.name == "day" {
				points = history
			} else {
				points = day
			}
		}
		kept := []Point{}
		for _, p := range points {
			if p.Timestamp >= now-period.days*86400 {
				kept = append(kept, p)
				latest = max(latest, p.Timestamp)
				if start == 0 || p.Timestamp < start {
					start = p.Timestamp
				}
			}
		}
		charts[period.name] = kept
	}
	if latest == 0 {
		return nil, true
	}
	url := coinGeckoURL
	if source == "Kraken" {
		url = krakenURL
	}
	return &History{Charts: charts, Source: source, SourceURL: url, Currency: "USD", UpdatedAt: latest, FetchedAt: now,
		Timezone: "UTC", HistoryStart: start}, failed
}

var errHistory = errors.New("wallet: price history unavailable")

func (c *ChartCache) gecko(ctx context.Context, id string, days int) ([]Point, error) {
	c.mu.Lock()
	limited := c.Now().Before(c.backoff)
	c.mu.Unlock()
	if limited {
		return nil, errors.New("wallet: price history rate limited")
	}
	q := url.Values{"vs_currency": {"usd"}, "days": {strconv.Itoa(days)}, "precision": {"full"}}
	var body struct {
		Prices [][]json.Number `json:"prices"`
	}
	status, header, err := getJSON(ctx, c.HTTP, c.Timeout, "https://api.coingecko.com/api/v3/coins/"+id+"/market_chart?"+q.Encode(), &body)
	if status == 429 {
		wait := time.Minute
		if v := header.Get("Retry-After"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				wait = time.Duration(n) * time.Second
			} else if t, err := time.Parse(time.RFC1123, v); err == nil {
				wait = t.Sub(c.Now())
			}
		}
		wait = min(time.Hour, max(time.Minute, wait))
		c.mu.Lock()
		if until := c.Now().Add(wait); until.After(c.backoff) {
			c.backoff = until
		}
		c.mu.Unlock()
	}
	if err != nil {
		return nil, errHistory
	}
	limit := c.Now().UnixMilli() + 300000
	var points []Point
	for _, row := range body.Prices {
		if len(row) != 2 {
			continue
		}
		ms, err := row[0].Int64()
		rate, err2 := row[1].Float64()
		if err != nil || err2 != nil || ms <= 0 || ms > limit || ms > 1<<53-1 || math.IsInf(rate, 0) || rate <= 0 {
			continue
		}
		points = append(points, Point{ms / 1000, rate})
	}
	points = dedupe(points)
	if len(points) == 0 {
		return nil, errHistory
	}
	return points, nil
}

func (c *ChartCache) kraken(ctx context.Context, pair string, interval int64) ([]Point, error) {
	q := url.Values{"pair": {pair}, "interval": {strconv.FormatInt(interval, 10)}}
	var body struct {
		Error  []any                      `json:"error"`
		Result map[string]json.RawMessage `json:"result"`
	}
	if _, _, err := getJSON(ctx, c.HTTP, c.Timeout, "https://api.kraken.com/0/public/OHLC?"+q.Encode(), &body); err != nil {
		return nil, errHistory
	}
	if body.Error == nil || len(body.Error) > 0 {
		return nil, errHistory
	}
	var rows []any
	for key, raw := range body.Result {
		if key != "last" && json.Unmarshal(raw, &rows) == nil {
			break
		}
	}
	now := c.Now().UnixMilli()
	var points []Point
	for _, r := range rows {
		row, _ := r.([]any)
		if len(row) < 5 {
			continue
		}
		t, ok := row[0].(float64)
		closing, _ := row[4].(string)
		rate := jsNumber(closing)
		if !ok || t != math.Trunc(t) || t <= 0 || t > 1<<53-1 || (int64(t)+interval*60)*1000 > now || math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 {
			continue
		}
		points = append(points, Point{int64(t) + interval*60, rate})
	}
	points = dedupe(points)
	if len(points) == 0 {
		return nil, errHistory
	}
	return points, nil
}

// dedupe keeps the last point per second, oldest first.
func dedupe(in []Point) []Point {
	seen := map[int64]int{}
	var out []Point
	for _, p := range in {
		if i, ok := seen[p.Timestamp]; ok {
			out[i] = p
			continue
		}
		seen[p.Timestamp] = len(out)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp < out[j].Timestamp })
	return out
}

// NativeCharts is nativeCryptoCharts(): the periods flattened next to the
// source fields, nil when there is nothing to draw.
func NativeCharts(h *History) map[string]any {
	if h == nil {
		return nil
	}
	has := false
	for _, v := range h.Charts {
		has = has || len(v) > 0
	}
	if !has {
		return nil
	}
	out := map[string]any{"market_source": h.Source, "market_source_url": h.SourceURL, "market_updated_at": h.UpdatedAt,
		"market_fetched_at": h.FetchedAt, "market_stale": h.Stale, "timezone": h.Timezone, "history_start": h.HistoryStart}
	for k, v := range h.Charts {
		out[k] = v
	}
	return out
}
