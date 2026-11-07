package wallet

import (
	"context"
	"errors"
	"math/big"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
)

// Earn parks coins in a campaign and hands them back, nothing more. The rate
// is 0: a yield would mint balances out of nothing, and that's the owner's
// call to make, not a default. Moves run under the same per-user lock as every
// other balance change.

type campaign struct {
	ID   int64
	Name string // the app only shows names it knows
	Code string
}

var campaigns = []campaign{{1, "usdt_evaa", "USDT"}, {2, "ton_staking", "TON"}}

func campaignByID(raw string) (campaign, bool) {
	id, _ := strconv.ParseInt(raw, 10, 64)
	for _, c := range campaigns {
		if c.ID == id {
			return c, true
		}
	}
	return campaign{}, false
}

var earnTxPath = regexp.MustCompile(`^/earn/public-api/v1/transactions/(\d+)/?$`)

var earnPath = regexp.MustCompile(`^/earn/public-api/v1/campaigns/(\d+)/(deposit|withdraw|full-withdraw|transactions|terms_and_conditions|mark_tooltip_campaign_seen)/?$`)

func (s *Service) earnBalance(ctx context.Context, q querier, user int64, c campaign) (*big.Int, error) {
	var units string
	err := q.QueryRow(ctx, "SELECT units::text FROM earn_accounts WHERE user_id=$1 AND campaign_id=$2", user, c.ID).Scan(&units)
	if errors.Is(err, pgx.ErrNoRows) {
		return new(big.Int), nil
	}
	if err != nil {
		return nil, err
	}
	return bigOf(units), nil
}

func (s *Service) earn(ctx context.Context, u account.User, method, path string, data map[string]any, query url.Values, m Market) (any, bool, error) {
	if method == "GET" && strings.TrimSuffix(path, "/") == "/earn/public-api/v1/campaigns" {
		out, err := s.earnList(ctx, u, m)
		return out, true, err
	}
	if method == "GET" && strings.HasPrefix(path, "/earn/public-api/v1/reference-data/currencies") {
		return map[string]any{"currencies": []any{}}, true, nil
	}
	if r := earnTxPath.FindStringSubmatch(path); r != nil && method == "GET" {
		out, err := s.earnTransaction(ctx, u, r[1])
		return out, true, err
	}
	r := earnPath.FindStringSubmatch(path)
	if r == nil {
		return nil, false, nil
	}
	c, ok := campaignByID(r[1])
	if !ok {
		return nil, true, fail(404, "Campaign not found.")
	}
	switch {
	case r[2] == "transactions":
		out, err := s.earnHistory(ctx, u, c)
		return out, true, err
	case method != "POST":
		return nil, true, fail(405, "Method not allowed.")
	case r[2] == "terms_and_conditions" || r[2] == "mark_tooltip_campaign_seen":
		return map[string]any{"ok": true}, true, nil
	}
	amount := ""
	if r[2] != "full-withdraw" {
		amount = jsString(data["amount"])
	}
	out, err := s.earnMove(ctx, u, c, r[2], amount, jsString(data["idempotency_key"]))
	return out, true, err
}

func (s *Service) earnList(ctx context.Context, u account.User, m Market) (any, error) {
	list := make([]map[string]any, 0, len(campaigns))
	for _, c := range campaigns {
		bal, err := s.earnBalance(ctx, s.db, u.ID, c)
		if err != nil {
			return nil, err
		}
		fiat, rate := s.fiatFor(ctx, u.ID)
		value := "0"
		if q, fresh := s.fresh(m, c.Code); fresh {
			if v, ok := MultiplyMarketAmount(format(bal, c.Code), q.Price); ok {
				value = convertFiat(v, rate)
			}
		}
		a, _ := Lookup(c.Code)
		var acc any
		if bal.Sign() > 0 {
			acc = map[string]any{"currency": c.Code, "balance": format(bal, c.Code), "balance_fiat": value, "fiat_currency": fiat, "paid_out_amount": "0", "created_at": "2026-01-01T00:00:00Z"}
		}
		list = append(list, map[string]any{"campaign_id": c.ID, "name": c.Name, "status": "active", "title": a.Name, "start_at": "2026-01-01T00:00:00Z", "end_at": nil,
			"deposit_currency": c.Code, "payout_currency": c.Code, "input_currency": c.Code, "deposit_withdraw_currency": c.Code, "payout_withdraw_currency": c.Code, "apy_type": "flexible",
			"interest_rate": map[string]any{"rate": "0", "bonus_rate": nil, "combined_rate": "0", "effective_rate": "0", "interest_type": "apy"}, "current_apy": "0",
			"min_deposit_amount": "0.01", "max_amount": nil, "total_earned": "0", "payout_pool": nil, "access_policy": map[string]any{"access_to_deposit": true, "access_to_withdrawal": true, "access_to_full_withdrawal": true, "deposit_restriction_reason": nil}, "depeg_info": nil,
			"terms_accepted": true, "is_terms_accepted": true, "account": acc, "payout_account": nil, "bonus": nil})
	}
	return map[string]any{"campaigns": list, "required_kyc_level": nil, "tooltip_campaign_id": nil, "total_participants": 0}, nil
}

// earnValues is what each campaign holds, in the display currency.
func (s *Service) earnValues(ctx context.Context, u account.User, m Market) ([]string, error) {
	_, rate := s.fiatFor(ctx, u.ID)
	var out []string
	for _, c := range campaigns {
		bal, err := s.earnBalance(ctx, s.db, u.ID, c)
		if err != nil {
			return nil, err
		}
		if q, fresh := s.fresh(m, c.Code); fresh && bal.Sign() > 0 {
			if v, ok := MultiplyMarketAmount(format(bal, c.Code), q.Price); ok {
				out = append(out, convertFiat(v, rate))
			}
		}
	}
	return out, nil
}

func (s *Service) earnHistory(ctx context.Context, u account.User, c campaign) (any, error) {
	rows, err := s.db.Query(ctx, `SELECT id, event_key, units::text, created_at FROM crypto_events
		WHERE user_id=$1 AND asset=$2 AND kind='earn_move' AND event_key LIKE $3 ORDER BY id DESC LIMIT 100`, u.ID, c.Code, "earn:"+strconv.FormatInt(c.ID, 10)+":%")
	if err != nil {
		return nil, err
	}
	list := []map[string]any{}
	var id, at int64
	var key, units string
	if _, err := pgx.ForEachRow(rows, []any{&id, &key, &units, &at}, func() error {
		n := bigOf(units)
		kind := "withdraw"
		if n.Sign() < 0 {
			kind = "deposit"
		}
		// the client's own key: the app matches rows to what it just sent
		list = append(list, earnTx(id, clientKey(key), kind, new(big.Int).Abs(n), c, at))
		return nil
	}); err != nil {
		return nil, err
	}
	return map[string]any{"transactions": list, "next_cursor": nil, "has_next": false}, nil
}

// clientKey is the idempotency key inside an "earn:campaign:user:key" event key.
func clientKey(event string) string {
	parts := strings.SplitN(event, ":", 4)
	return parts[len(parts)-1]
}

// earnTransaction is one row's detail page.
func (s *Service) earnTransaction(ctx context.Context, u account.User, raw string) (any, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, fail(404, "Transaction not found.")
	}
	var key, asset, units string
	var at int64
	err = s.db.QueryRow(ctx, `SELECT event_key, asset, units::text, created_at FROM crypto_events WHERE id=$1 AND user_id=$2 AND kind='earn_move'`, id, u.ID).Scan(&key, &asset, &units, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fail(404, "Transaction not found.")
	}
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(key, ":", 3)
	c, ok := campaignByID(parts[min(1, len(parts)-1)])
	if !ok {
		return nil, fail(404, "Transaction not found.")
	}
	n := bigOf(units)
	kind := "withdraw"
	if n.Sign() < 0 {
		kind = "deposit"
	}
	return earnTx(id, clientKey(key), kind, new(big.Int).Abs(n), c, at), nil
}

func earnTx(id int64, key, kind string, units *big.Int, c campaign, at int64) map[string]any {
	date := isoDate(at)
	direction := "withdrawal" // the app labels a row by these two, not by type
	if kind == "deposit" {
		direction = "deposit"
	}
	return map[string]any{"id": id, "transaction_id": id, "idempotency_key": key, "type": kind, "direction": direction, "transaction_type": "regular", "status": "success",
		"amount": format(units, c.Code), "currency": c.Code, "date": date, "created_at": date, "campaign_id": c.ID, "campaign_name": c.Name}
}

// earnMove is one deposit or withdrawal: main balance, campaign balance and
// the history event all change in a transaction or not at all. The client's
// idempotency key makes a retry a no-op.
func (s *Service) earnMove(ctx context.Context, u account.User, c campaign, kind, amount, idem string) (any, error) {
	if len(idem) == 0 || len(idem) > 80 {
		return nil, fail(400, "Invalid request.")
	}
	var units *big.Int
	if kind != "full-withdraw" {
		var err error
		if units, err = parseAmount(amount, c.Code); err != nil {
			return nil, err
		}
		if units.Sign() < 1 {
			return nil, fail(400, "Enter a positive amount.")
		}
	}
	key := "earn:" + strconv.FormatInt(c.ID, 10) + ":" + strconv.FormatInt(u.ID, 10) + ":" + idem
	var id int64
	direction := kind
	if kind == "full-withdraw" {
		direction = "withdraw"
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := lock(ctx, tx, u.ID); err != nil {
			return err
		}
		var raw string
		err := tx.QueryRow(ctx, "SELECT id, abs(units)::text FROM crypto_events WHERE event_key=$1 AND user_id=$2", key, u.ID).Scan(&id, &raw)
		if err == nil {
			units = bigOf(raw)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		parked, err := s.earnBalance(ctx, tx, u.ID, c)
		if err != nil {
			return err
		}
		if kind == "full-withdraw" {
			units = parked
		}
		if units.Sign() < 1 {
			return fail(400, "Nothing to withdraw.")
		}
		diff := new(big.Int).Set(units) // to the main balance
		if direction == "deposit" {
			diff.Neg(diff)
		} else if parked.Cmp(units) < 0 {
			return fail(400, "That is more than this campaign holds.")
		}
		if _, err := change(ctx, tx, u.ID, c.Code, diff); err != nil {
			return err
		}
		next := new(big.Int).Sub(parked, diff)
		if _, err := tx.Exec(ctx, `INSERT INTO earn_accounts(user_id, campaign_id, units) VALUES ($1, $2, $3::numeric)
			ON CONFLICT (user_id, campaign_id) DO UPDATE SET units = excluded.units`, u.ID, c.ID, next.String()); err != nil {
			return err
		}
		if err := s.event(ctx, tx, key, u.ID, c.Code, diff, "earn_move", 0); err != nil {
			return err
		}
		return tx.QueryRow(ctx, "SELECT id FROM crypto_events WHERE event_key=$1", key).Scan(&id)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"transaction": earnTx(id, idem, direction, units, c, int64(s.unix()))}, nil
}
