package wallet

import (
	"context"
	"errors"
	"regexp"

	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/stars"
)

// An app's "Buy Stars" is a stars_topup payment form whose url is
// /pay/stars/<token>. That page prices the form's Stars in each Wallet
// asset (TopupPage) and holds one price as a quote bound to the form
// (TopupQuote); the app hands the quote id back in payments.sendPaymentForm
// and PayTopup settles it the way the web's purchase does.

var payTokenRe = regexp.MustCompile(`^[a-f0-9]{48}$`)

type topupForm struct {
	id, user, stars int64
	expires         int
	paid            bool
}

var (
	errTopupLink    = fail(404, "This payment link isn't valid.")
	errTopupPaid    = fail(409, "These Stars are paid for already.")
	errTopupExpired = fail(410, "This payment expired. Start again in the app.")
)

func (s *Service) topupForm(ctx context.Context, token string) (topupForm, error) {
	var f topupForm
	if !payTokenRe.MatchString(token) {
		return f, errTopupLink
	}
	err := s.db.QueryRow(ctx, "SELECT id, user_id, stars, expires_at, paid FROM payment_forms WHERE pay_token=$1 AND kind=$2",
		token, stars.KindTopup).Scan(&f.id, &f.user, &f.stars, &f.expires, &f.paid)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, errTopupLink
	}
	return f, err
}

// open is a form that can still be quoted.
func (s *Service) openTopup(ctx context.Context, token string) (topupForm, error) {
	f, err := s.topupForm(ctx, token)
	switch {
	case err != nil:
		return f, err
	case f.paid:
		return f, errTopupPaid
	case f.expires <= s.unix():
		return f, errTopupExpired
	}
	return f, s.requirePerson(ctx, s.db, f.user)
}

// TopupPage is what the page shows: the Stars, and what they cost in each
// asset next to the balance.
func (s *Service) TopupPage(ctx context.Context, token string) (map[string]any, error) {
	f, err := s.openTopup(ctx, token)
	if errors.Is(err, errTopupPaid) {
		return map[string]any{"paid": true, "stars": f.stars}, nil
	}
	if err != nil {
		return nil, err
	}
	rate := int64(s.settings.Get(ctx).StarsPerUSD)
	m := s.market.Refresh(ctx)
	list := make([]map[string]any, 0, len(Assets))
	for _, a := range Assets {
		bal, err := s.Balance(ctx, f.user, a.Code)
		if err != nil {
			return nil, err
		}
		item := map[string]any{"asset": a.Code, "name": a.Name, "balance": format(bal, a.Code), "amount": nil, "enough": false}
		if q, fresh := s.fresh(m, a.Code); fresh {
			if units, err := CryptoUnitsForStars(f.stars, rate, q.Price, a.Decimals); err == nil {
				item["amount"] = format(units, a.Code)
				item["enough"] = bal.Cmp(units) >= 0
			}
		}
		list = append(list, item)
	}
	return map[string]any{"paid": false, "stars": f.stars, "starsPerUSD": rate, "expiresAt": f.expires, "assets": list, "monetaryValue": false}, nil
}

// TopupQuote holds the price in one asset for this form. The quote pays
// only this form, and only through the app.
func (s *Service) TopupQuote(ctx context.Context, token string, data map[string]any) (map[string]any, error) {
	f, err := s.openTopup(ctx, token)
	if err != nil {
		return nil, err
	}
	a, found := Lookup(jsString(data["asset"]))
	if !found {
		return nil, fail(400, "Choose a supported asset.")
	}
	rate := int64(s.settings.Get(ctx).StarsPerUSD)
	m := s.market.Refresh(ctx)
	p, fresh := s.fresh(m, a.Code)
	if !fresh {
		return nil, fail(503, "A fresh market price is unavailable. Try again when market data returns.")
	}
	units, err := CryptoUnitsForStars(f.stars, rate, p.Price, a.Decimals)
	if err != nil {
		return nil, fail(503, "The current market price cannot produce a valid quote.")
	}
	bal, err := s.Balance(ctx, f.user, a.Code)
	if err != nil {
		return nil, err
	}
	if bal.Cmp(units) < 0 {
		return nil, errInsufficient
	}
	q := starsQuote{id: randomHex(24), asset: a.Code, units: units, stars: f.stars, rate: rate, price: p.Price, source: m.Source, updated: p.UpdatedAt,
		expires: min(s.unix()+180, f.expires)}
	_, err = s.db.Exec(ctx, `INSERT INTO crypto_stars_quotes(id, user_id, asset, units, stars, stars_per_usd, market_price, market_source, market_updated_at, expires_at, form_id)
		VALUES ($1, $2, $3, $4::numeric, $5, $6, $7, $8, $9, $10, $11)`, q.id, f.user, q.asset, units.String(), q.stars, q.rate, q.price, q.source, q.updated, q.expires, f.id)
	if err != nil {
		return nil, err
	}
	out := q.view()
	out["name"] = a.Name
	return out, nil
}

var (
	errTopupCredentials = tgerr.New(400, "PAYMENT_CREDENTIALS_INVALID")
	errTopupQuoteOld    = tgerr.New(400, "PAYMENT_CREDENTIALS_EXPIRED")
	errTopupDuplicate   = tgerr.New(400, "FORM_SUBMIT_DUPLICATE")
	errTopupBalance     = tgerr.New(400, "BALANCE_TOO_LOW")
)

// PayTopup pays form with the quote its page made, in one transaction:
// the form is claimed, the asset leaves the wallet and the Stars land, or
// none of it happens. invoiceStars is what the app's invoice asked for (0
// when it didn't say); it has to be the form's. It returns the Stars
// balance after.
func (s *Service) PayTopup(ctx context.Context, user, form int64, quoteID string, invoiceStars int64) (int64, error) {
	if !quoteIDRe.MatchString(quoteID) {
		return 0, errTopupCredentials
	}
	if err := s.requirePerson(ctx, s.db, user); err != nil {
		return 0, err
	}
	var balance int64
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := lock(ctx, tx, user); err != nil {
			return err
		}
		f, err := stars.ClaimForm(ctx, tx, user, form, stars.KindTopup, s.unix())
		if err != nil {
			return err
		}
		if invoiceStars != 0 && invoiceStars != f.Stars {
			return errTopupCredentials
		}
		q := starsQuote{id: quoteID}
		var raw string
		var result []byte
		err = tx.QueryRow(ctx, "SELECT asset, units::text, stars, expires_at, result FROM crypto_stars_quotes WHERE id=$1 AND user_id=$2 AND form_id=$3 FOR UPDATE",
			quoteID, user, form).Scan(&q.asset, &raw, &q.stars, &q.expires, &result)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return errTopupCredentials
		case err != nil:
			return err
		case result != nil:
			return errTopupDuplicate
		case q.expires <= s.unix():
			return errTopupQuoteOld
		case q.stars != f.Stars:
			return errTopupCredentials
		}
		q.units = bigOf(raw)
		if q.units.Sign() <= 0 {
			return errTopupCredentials
		}
		out, err := s.settle(ctx, tx, user, q)
		if errors.Is(err, errInsufficient) {
			return errTopupBalance
		}
		if err != nil {
			return err
		}
		balance = out["starsBalance"].(int64)
		return nil
	})
	return balance, err
}
