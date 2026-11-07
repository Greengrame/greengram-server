package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/stars"
)

// Buying Stars with crypto is crypto-stars.js: the instance's starsPerUSD
// rate at the current USD price, a three-minute quote, then one
// transaction that takes the crypto and posts the Stars.

func (s *Service) StarsOptions(ctx context.Context, u account.User) (map[string]any, error) {
	if err := s.requirePerson(ctx, s.db, u.ID); err != nil {
		return nil, err
	}
	rate := int64(s.settings.Get(ctx).StarsPerUSD)
	m := s.market.Refresh(ctx)
	list := make([]map[string]any, 0, len(Assets))
	for _, a := range Assets {
		bal, err := s.Balance(ctx, u.ID, a.Code)
		if err != nil {
			return nil, err
		}
		q, fresh := s.fresh(m, a.Code)
		var price any
		if p, ok := m.Assets[a.Code]; ok && p.Price != "" {
			price = p.Price
		}
		item := map[string]any{"asset": a.Code, "name": a.Name, "balance": format(bal, a.Code), "marketPriceUSD": price,
			"marketAvailable": fresh, "starsPerCoin": nil, "maxStars": nil, "amountFor100Stars": nil}
		if fresh {
			item["starsPerCoin"], _ = MultiplyMarketAmount(q.Price, big.NewInt(rate).String())
			if n, err := MaxStarsForCrypto(bal, rate, q.Price, a.Decimals); err == nil {
				item["maxStars"] = n
			}
			if units, err := CryptoUnitsForStars(100, rate, q.Price, a.Decimals); err == nil {
				item["amountFor100Stars"] = format(units, a.Code)
			}
		}
		list = append(list, item)
	}
	return map[string]any{"monetaryValue": false, "starsPerUSD": rate, "marketSource": m.Source, "marketUpdatedAt": m.UpdatedAt, "assets": list}, nil
}

type starsQuote struct {
	id, asset, price, source string
	units                    *big.Int
	stars, rate, updated     int64
	expires                  int
}

func (q starsQuote) view() map[string]any {
	return map[string]any{"quoteId": q.id, "asset": q.asset, "amount": format(q.units, q.asset), "stars": q.stars, "starsPerUSD": q.rate,
		"marketPriceUSD": q.price, "marketSource": q.source, "marketUpdatedAt": q.updated, "expiresAt": q.expires, "monetaryValue": false}
}

func (s *Service) QuoteStars(ctx context.Context, u account.User, data map[string]any) (map[string]any, error) {
	if err := s.requirePerson(ctx, s.db, u.ID); err != nil {
		return nil, err
	}
	a, found := Lookup(jsString(data["asset"]))
	n, isInt := safeInt(data["stars"])
	if !found || !isInt || n < 1 || n > 1000000000 {
		return nil, fail(400, "Choose a supported asset and 1–1,000,000,000 Stars.")
	}
	rate := int64(s.settings.Get(ctx).StarsPerUSD)
	m := s.market.Refresh(ctx)
	p, fresh := s.fresh(m, a.Code)
	if !fresh {
		return nil, fail(503, "A fresh market price is unavailable. Try again when market data returns.")
	}
	units, err := CryptoUnitsForStars(n, rate, p.Price, a.Decimals)
	if err != nil {
		return nil, fail(503, "The current market price cannot produce a valid quote.")
	}
	bal, err := s.Balance(ctx, u.ID, a.Code)
	if err != nil {
		return nil, err
	}
	if bal.Cmp(units) < 0 {
		return nil, errInsufficient
	}
	q := starsQuote{id: randomHex(24), asset: a.Code, units: units, stars: n, rate: rate, price: p.Price, source: m.Source, updated: p.UpdatedAt, expires: s.unix() + 180}
	_, err = s.db.Exec(ctx, `INSERT INTO crypto_stars_quotes(id, user_id, asset, units, stars, stars_per_usd, market_price, market_source, market_updated_at, expires_at)
		VALUES ($1, $2, $3, $4::numeric, $5, $6, $7, $8, $9, $10)`, q.id, u.ID, q.asset, units.String(), q.stars, q.rate, q.price, q.source, q.updated, q.expires)
	if err != nil {
		return nil, err
	}
	return q.view(), nil
}

// a Stars balance has to stay a safe integer for the web clients
const maxSafe = 1<<53 - 1

func (s *Service) BuyStars(ctx context.Context, u account.User, data map[string]any) (map[string]any, error) {
	if err := s.requirePerson(ctx, s.db, u.ID); err != nil {
		return nil, err
	}
	id, _ := data["quoteId"].(string)
	if !quoteIDRe.MatchString(id) {
		return nil, fail(400, "Choose a valid Stars purchase quote.")
	}
	var out map[string]any
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := lock(ctx, tx, u.ID); err != nil {
			return err
		}
		var q starsQuote
		var raw string
		var result []byte
		// a top-up page's quote pays its form, through the app (PayTopup)
		err := tx.QueryRow(ctx, "SELECT asset, units::text, stars, expires_at, result FROM crypto_stars_quotes WHERE id=$1 AND user_id=$2 AND form_id IS NULL FOR UPDATE",
			id, u.ID).Scan(&q.asset, &raw, &q.stars, &q.expires, &result)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(404, "Stars purchase quote not found.")
		}
		if err != nil {
			return err
		}
		if result != nil {
			return json.Unmarshal(result, &out)
		}
		if q.expires <= s.unix() {
			return fail(410, "The Stars purchase quote expired. Request a new quote.")
		}
		q.id, q.units = id, bigOf(raw)
		out, err = s.settle(ctx, tx, u.ID, q)
		return err
	})
	return out, err
}

// settle takes a quote's asset out of the wallet and posts its Stars. The
// caller holds the user's lock and the quote's row, and checked the quote.
func (s *Service) settle(ctx context.Context, tx pgx.Tx, user int64, q starsQuote) (map[string]any, error) {
	bal, err := Balance(ctx, tx, user, q.asset)
	if err != nil {
		return nil, err
	}
	remaining := bal.Sub(bal, q.units)
	if remaining.Sign() < 0 {
		return nil, errInsufficient
	}
	have, err := stars.Balance(ctx, tx, user)
	if err != nil {
		return nil, err
	}
	if have+q.stars > maxSafe {
		return nil, fail(400, "The resulting Stars balance is too large.")
	}
	issuer, err := stars.Account(ctx, tx, stars.WalletAccount)
	if err != nil {
		return nil, err
	}
	if err := setBalance(ctx, tx, user, q.asset, remaining); err != nil {
		return nil, err
	}
	if err := s.event(ctx, tx, "stars:"+q.id, user, q.asset, new(big.Int).Neg(q.units), "stars_purchase", issuer); err != nil {
		return nil, err
	}
	amount := format(q.units, q.asset)
	txID, err := stars.Post(ctx, tx, stars.Entry{User: user, Amount: q.stars, Reason: "crypto_purchase", Reference: "crypto-stars:" + q.id,
		CreatedBy: user, Peer: issuer, Title: "Wallet", Note: "Stars purchase · " + amount + " " + q.asset}, s.unix())
	if err != nil {
		return nil, err
	}
	out := map[string]any{"ok": true, "transactionId": txID, "monetaryValue": false, "stars": q.stars, "starsBalance": have + q.stars,
		"asset": q.asset, "amount": amount, "cryptoBalance": format(remaining, q.asset)}
	enc, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "UPDATE crypto_stars_quotes SET result=$2 WHERE id=$1", q.id, enc); err != nil {
		return nil, err
	}
	return out, nil
}
