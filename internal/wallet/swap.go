package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
)

var quoteIDRe = regexp.MustCompile(`^[a-f0-9]{48}$`)

type conversion struct {
	from, to           Asset
	fromUnits, toUnits *big.Int
	fromQuote, toQuote Quote
	market             Market
	maxAmount          bool
	balance            *big.Int
}

// convert is crypto-swaps.js conversion(): data names one side's amount and
// the other side follows from the two USD prices, with no fee.
func (s *Service) convert(ctx context.Context, u account.User, data map[string]any) (conversion, error) {
	var c conversion
	if err := s.requirePerson(ctx, s.db, u.ID); err != nil {
		return c, err
	}
	from, okFrom := Lookup(jsString(data["from_currency"]))
	to, okTo := Lookup(jsString(data["to_currency"]))
	if !okFrom || !okTo || from.Code == to.Code {
		return c, fail(400, "Choose two different supported assets.")
	}
	fa := data["from_amount"]
	receive := fa == nil || fa == ""
	if v, ok := data["max_amount"]; ok {
		if _, isBool := v.(bool); !isBool {
			return c, fail(400, "Choose a valid maximum-amount setting.")
		}
	}
	if v, ok := data["previous_exchange_uid"]; ok && !quoteIDRe.MatchString(jsString(v)) {
		return c, fail(400, "Choose a valid previous exchange quote.")
	}
	if truthy(data["local_currency"]) && data["local_currency"] != "USD" {
		return c, fail(400, "Choose USD for the local exchange value.")
	}
	if list, ok := data["contributing_account_types"].([]any); ok && len(list) > 0 {
		return c, fail(409, "Move funds to your main Wallet before exchanging them.")
	}
	m := s.market.Refresh(ctx)
	fq, ok1 := s.fresh(m, from.Code)
	tq, ok2 := s.fresh(m, to.Code)
	if !ok1 || !ok2 {
		return c, fail(503, "Current prices are unavailable for this pair. Try again shortly.")
	}
	bal, err := s.Balance(ctx, u.ID, from.Code)
	if err != nil {
		return c, err
	}
	maxAmount, _ := data["max_amount"].(bool)
	var amount *big.Int
	if maxAmount && !receive {
		amount = new(big.Int).Set(bal)
	} else {
		side, raw := from.Code, data["from_amount"]
		if receive {
			side, raw = to.Code, data["to_amount"]
		}
		v := jsString(raw)
		if v == "" {
			v = "0"
		}
		if amount, err = parseAmount(v, side); err != nil {
			return c, err
		}
	}
	c = conversion{from: from, to: to, fromQuote: fq, toQuote: tq, market: m, maxAmount: maxAmount, balance: bal}
	if receive {
		c.toUnits = amount
		c.fromUnits, err = SwapUnits(amount, fq.Price, tq.Price, from.Decimals, to.Decimals, true)
	} else {
		c.fromUnits = amount
		c.toUnits, err = SwapUnits(amount, fq.Price, tq.Price, from.Decimals, to.Decimals, false)
	}
	if err != nil {
		return c, fail(503, "Current prices are unavailable for this pair. Try again shortly.")
	}
	if len(c.fromUnits.String()) > 60 || len(c.toUnits.String()) > 60 {
		return c, fail(400, "The quoted exchange amount is too large.")
	}
	return c, nil
}

func (s *Service) conversionView(c conversion) map[string]any {
	fiat, _ := MultiplyMarketAmount(format(c.fromUnits, c.from.Code), c.fromQuote.Price)
	rate, _ := SwapRate(c.fromQuote.Price, c.toQuote.Price)
	return map[string]any{
		"from_currency": c.from.Code, "to_currency": c.to.Code,
		"from_amount": format(c.fromUnits, c.from.Code), "to_amount": format(c.toUnits, c.to.Code),
		"from_fiat_amount": fiat, "from_fiat_currency": "USD", "rate": rate,
		"fee_currency": c.from.Code, "fee_amount": "0", "fee_percent": "0", "fee_fiat_amount": "0", "fee_fiat_currency": "USD",
		"new_rates": false, "price_deviation_percent": "0", "market_source": c.market.Source,
		"market_updated_at": min(c.fromQuote.UpdatedAt, c.toQuote.UpdatedAt), "max_amount": c.maxAmount,
		"ttl": 60, "expires_at": s.unix() + 60,
	}
}

// Convert is the live preview while typing.
func (s *Service) Convert(ctx context.Context, u account.User, data map[string]any) (map[string]any, error) {
	c, err := s.convert(ctx, u, data)
	if err != nil {
		return nil, err
	}
	view := s.conversionView(c)
	view["min_amount"] = format(big.NewInt(1), c.from.Code)
	view["max_amount"] = format(c.balance, c.from.Code)
	return view, nil
}

// QuoteSwap fixes a conversion for a minute so submit pays what was shown.
func (s *Service) QuoteSwap(ctx context.Context, u account.User, data map[string]any) (map[string]any, error) {
	c, err := s.convert(ctx, u, data)
	if err != nil {
		return nil, err
	}
	view := s.conversionView(c)
	if c.fromUnits.Sign() < 1 || c.toUnits.Sign() < 1 {
		return nil, fail(400, "The amount is too small to exchange.")
	}
	bal, err := s.Balance(ctx, u.ID, c.from.Code)
	if err != nil {
		return nil, err
	}
	if c.fromUnits.Cmp(bal) > 0 {
		return nil, errInsufficient
	}
	id := randomHex(24)
	err = s.tx(ctx, func(tx pgx.Tx) error {
		// one quote count per user at a time
		if err := lock(ctx, tx, u.ID); err != nil {
			return err
		}
		if prev, ok := data["previous_exchange_uid"]; ok {
			var done bool
			err := tx.QueryRow(ctx, "SELECT result IS NOT NULL FROM crypto_swap_quotes WHERE id=$1 AND user_id=$2", jsString(prev), u.ID).Scan(&done)
			if errors.Is(err, pgx.ErrNoRows) {
				return fail(404, "Exchange quote not found.")
			}
			if err != nil {
				return err
			}
			if done {
				return fail(409, "This exchange was already completed.")
			}
		}
		var active int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM crypto_swap_quotes WHERE user_id=$1 AND result IS NULL AND expires_at > $2", u.ID, s.unix()).Scan(&active); err != nil {
			return err
		}
		if active >= 100 {
			return fail(429, "Too many active exchange quotes. Wait a minute before reviewing another.")
		}
		view["uid"] = id
		if _, err := tx.Exec(ctx, "DELETE FROM crypto_swap_quotes WHERE result IS NULL AND expires_at < $1", s.unix()-86400); err != nil {
			return err
		}
		raw, err := json.Marshal(view)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO crypto_swap_quotes(id, user_id, from_asset, to_asset, from_units, to_units, expires_at, quote)
			VALUES ($1, $2, $3, $4, $5::numeric, $6::numeric, $7, $8)`, id, u.ID, c.from.Code, c.to.Code, c.fromUnits.String(), c.toUnits.String(), view["expires_at"], raw)
		return err
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// SubmitSwap pays a quote once; a repeat returns the first result.
func (s *Service) SubmitSwap(ctx context.Context, u account.User, id string) (map[string]any, error) {
	if err := s.requirePerson(ctx, s.db, u.ID); err != nil {
		return nil, err
	}
	var out map[string]any
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := s.requirePerson(ctx, tx, u.ID); err != nil {
			return err
		}
		if err := lock(ctx, tx, u.ID); err != nil {
			return err
		}
		var from, to, fromRaw, toRaw string
		var expires int
		var quote, result []byte
		err := tx.QueryRow(ctx, "SELECT from_asset, to_asset, from_units::text, to_units::text, expires_at, quote, result FROM crypto_swap_quotes WHERE id=$1 AND user_id=$2 FOR UPDATE",
			id, u.ID).Scan(&from, &to, &fromRaw, &toRaw, &expires, &quote, &result)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(404, "Exchange quote not found.")
		}
		if err != nil {
			return err
		}
		if result != nil {
			return json.Unmarshal(result, &out)
		}
		if expires <= s.unix() {
			return fail(410, "Exchange quote expired. Review a new quote.")
		}
		fromUnits, toUnits := bigOf(fromRaw), bigOf(toRaw)
		fromBal, err := change(ctx, tx, u.ID, from, new(big.Int).Neg(fromUnits))
		if err != nil {
			return err
		}
		toBal, err := change(ctx, tx, u.ID, to, toUnits)
		if err != nil {
			return err
		}
		if err := s.event(ctx, tx, "swap:"+id+":from", u.ID, from, new(big.Int).Neg(fromUnits), "crypto_swap", 0); err != nil {
			return err
		}
		if err := s.event(ctx, tx, "swap:"+id+":to", u.ID, to, toUnits, "crypto_swap", 0); err != nil {
			return err
		}
		if err := json.Unmarshal(quote, &out); err != nil {
			return err
		}
		out["status"] = "success"
		out["from_balance"] = format(fromBal, from)
		out["to_balance"] = format(toBal, to)
		raw, err := json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE crypto_swap_quotes SET result=$2 WHERE id=$1", id, raw)
		return err
	})
	return out, err
}
