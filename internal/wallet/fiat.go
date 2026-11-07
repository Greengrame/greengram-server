package wallet

import (
	"context"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
)

// Prices stay in USD everywhere money moves. The default currency is only a
// display: amounts on screen are USD times CoinGecko's exchange rate, the same
// source the coin prices come from. Without a rate the wallet stays in USD.

const fxMaxAge = 30 * time.Minute

// FXCache holds USD exchange rates. It never blocks a page for long: a failed
// fetch keeps the old rates and waits before trying again.
type FXCache struct {
	HTTP Doer

	mu    sync.Mutex
	rates map[string]string
	at    time.Time
	tried time.Time
}

func (c *FXCache) get(ctx context.Context) map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if now.Sub(c.at) < fxMaxAge || now.Sub(c.tried) < time.Minute {
		return c.rates
	}
	c.tried = now
	d := c.HTTP
	if d == nil {
		d = noRedirects
	}
	var body struct {
		Rates map[string]struct {
			Value float64 `json:"value"`
			Type  string  `json:"type"`
		} `json:"rates"`
	}
	if _, _, err := getJSON(context.WithoutCancel(ctx), d, 5*time.Second, "https://api.coingecko.com/api/v3/exchange_rates", &body); err != nil {
		return c.rates
	}
	usd := body.Rates["usd"].Value
	if usd <= 0 {
		return c.rates
	}
	rates := map[string]string{"USD": "1"}
	for code, r := range body.Rates {
		if r.Type == "fiat" && r.Value > 0 && len(code) == 3 {
			rates[strings.ToUpper(code)] = strconv.FormatFloat(r.Value/usd, 'f', 8, 64)
		}
	}
	c.rates, c.at = rates, now
	return rates
}

// fiatFor is the user's display currency and its rate from USD.
func (s *Service) fiatFor(ctx context.Context, user int64) (code, rate string) {
	var saved string
	err := s.db.QueryRow(ctx, "SELECT local_currency FROM wallet_prefs WHERE user_id=$1", user).Scan(&saved)
	if err != nil || saved == "" || saved == "USD" {
		return "USD", "1"
	}
	if r, ok := s.fx.get(ctx)[saved]; ok {
		return saved, r
	}
	return "USD", "1"
}

func (s *Service) fiatCurrencies(ctx context.Context) []string {
	rates := s.fx.get(ctx)
	if len(rates) == 0 {
		return []string{"USD"}
	}
	out := make([]string, 0, len(rates))
	for code := range rates {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

func (s *Service) setFiat(ctx context.Context, u account.User, code string) error {
	code = strings.ToUpper(code)
	if _, ok := s.fx.get(ctx)[code]; !ok && code != "USD" {
		return fail(400, "This currency is unavailable right now.")
	}
	_, err := s.db.Exec(ctx, `INSERT INTO wallet_prefs(user_id, local_currency) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET local_currency = excluded.local_currency`, u.ID, code)
	return err
}

// convertFiat scales a USD decimal to the display currency.
func convertFiat(usd, rate string) string {
	if rate == "1" {
		return usd
	}
	v, ok := MultiplyMarketAmount(usd, rate)
	if !ok {
		return usd
	}
	return v
}

func convertFiatPtr(usd *string, rate string) any {
	if usd == nil || *usd == "" {
		return nil
	}
	neg := strings.HasPrefix(*usd, "-")
	v := convertFiat(strings.TrimPrefix(*usd, "-"), rate)
	if neg {
		return "-" + v
	}
	return v
}

// fiatRate is the rate for a currency named in a request's path.
func (s *Service) fiatRate(ctx context.Context, code string) string {
	if code == "USD" {
		return "1"
	}
	if r, ok := s.fx.get(ctx)[code]; ok {
		return r
	}
	return "1"
}

// scaleHistory copies a USD chart into the display currency.
func scaleHistory(h *History, rate string) *History {
	r, ok := new(big.Float).SetString(rate)
	if h == nil || rate == "1" || !ok {
		return h
	}
	f, _ := r.Float64()
	out := *h
	out.Charts = make(map[string][]Point, len(h.Charts))
	for k, list := range h.Charts {
		scaled := make([]Point, len(list))
		for i, p := range list {
			p.Rate *= f
			scaled[i] = p
		}
		out.Charts[k] = scaled
	}
	return &out
}

// swapVolumeUSD adds up what the user sold in swaps since a time, at today's
// prices: the ledger keeps units, not what they were worth then.
func (s *Service) swapVolumeUSD(ctx context.Context, user int64, since int64, m Market) (*big.Rat, error) {
	rows, err := s.db.Query(ctx, `SELECT asset, sum(-units)::text FROM crypto_events
		WHERE user_id=$1 AND kind='crypto_swap' AND units < 0 AND created_at >= $2 GROUP BY asset`, user, since)
	if err != nil {
		return nil, err
	}
	total := new(big.Rat)
	var asset, units string
	_, err = pgx.ForEachRow(rows, []any{&asset, &units}, func() error {
		q, fresh := s.fresh(m, asset)
		if !fresh {
			return nil
		}
		if v, ok := MultiplyMarketAmount(format(bigOf(units), asset), q.Price); ok {
			if x, ok := new(big.Rat).SetString(v); ok {
				total.Add(total, x)
			}
		}
		return nil
	})
	return total, err
}

func bigOfRat(v string) *big.Rat {
	r, ok := new(big.Rat).SetString(v)
	if !ok {
		return new(big.Rat)
	}
	return r
}
