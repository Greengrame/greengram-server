package wallet

import (
	"context"
	"errors"
	"math/big"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
)

// The DeFi account is a second local balance with no chain behind it;
// moves to and from main are quoted for five minutes, then confirmed.

var moveIDRe = regexp.MustCompile(`^[a-f0-9]{32}$`)

func defiBalance(ctx context.Context, q querier, user int64, code string) (*big.Int, error) {
	var units string
	err := q.QueryRow(ctx, "SELECT units::text FROM defi_balances WHERE user_id=$1 AND asset=$2", user, code).Scan(&units)
	if errors.Is(err, pgx.ErrNoRows) {
		return new(big.Int), nil
	}
	if err != nil {
		return nil, err
	}
	return bigOf(units), nil
}

func (s *Service) unitsByAsset(ctx context.Context, query string, user int64) (map[string]*big.Int, error) {
	rows, err := s.db.Query(ctx, query, user)
	if err != nil {
		return nil, err
	}
	out := map[string]*big.Int{}
	var asset, units string
	_, err = pgx.ForEachRow(rows, []any{&asset, &units}, func() error {
		out[asset] = bigOf(units)
		return nil
	})
	return out, err
}

func (s *Service) defiCreated(ctx context.Context, user int64) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM defi_accounts WHERE user_id=$1)", user).Scan(&ok)
	return ok, err
}

func (s *Service) DefiState(ctx context.Context, u account.User) (map[string]any, error) {
	created, err := s.defiCreated(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	// the home screen asks for this on every open, so one query per table
	parked, err := s.unitsByAsset(ctx, "SELECT asset, units::text FROM defi_balances WHERE user_id=$1", u.ID)
	if err != nil {
		return nil, err
	}
	main, err := s.unitsByAsset(ctx, "SELECT asset, units::text FROM crypto_accounts WHERE user_id=$1", u.ID)
	if err != nil {
		return nil, err
	}
	m := s.market.Read()
	fiat, rate := s.fiatFor(ctx, u.ID)
	values := []string{}
	balances := make([]map[string]any, 0, len(Assets))
	zero := new(big.Int)
	opensHere := false
	for _, a := range Assets {
		d, have := parked[a.Code]
		if !have {
			d = zero
		}
		opensHere = opensHere || d.Sign() > 0
		held, have := main[a.Code]
		if !have {
			held = zero
		}
		if q, fresh := s.fresh(m, a.Code); fresh && d.Sign() > 0 {
			if v, ok := MultiplyMarketAmount(format(d, a.Code), q.Price); ok {
				values = append(values, convertFiat(v, rate))
			}
		}
		balances = append(balances, map[string]any{"asset": a.Code, "balance": format(d, a.Code), "main_balance": format(held, a.Code)})
	}
	total, _ := SumMarketAmounts(values)
	history := []map[string]any{}
	if created {
		rows, err := s.db.Query(ctx, `SELECT id, asset, units::text, direction, created_at FROM defi_moves
			WHERE user_id=$1 AND completed ORDER BY created_at DESC, id DESC LIMIT 50`, u.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, asset, units, dir string
			var at int
			if err := rows.Scan(&id, &asset, &units, &dir, &at); err != nil {
				rows.Close()
				return nil, err
			}
			history = append(history, map[string]any{"id": id, "asset": asset, "amount": format(bigOf(units), asset), "direction": dir, "created_at": at})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	// opens_here: a plain open of the wallet lands on this page, only for
	// someone with money on it. Everyone else starts on the main page.
	return map[string]any{"created": created, "opens_here": opensHere, "local": true, "monetary_value": false, "total_fiat": total, "fiat_currency": fiat,
		"capabilities": map[string]any{"internal_moves": true, "blockchain": false, "addresses": false, "recovery_phrase": false, "protocols": false},
		"balances":     balances, "history": history}, nil
}

func (s *Service) CreateDefi(ctx context.Context, u account.User) (map[string]any, error) {
	if _, err := s.db.Exec(ctx, "INSERT INTO defi_accounts(user_id, created_at) VALUES ($1, $2) ON CONFLICT DO NOTHING", u.ID, s.unix()); err != nil {
		return nil, err
	}
	return s.DefiState(ctx, u)
}

func (s *Service) QuoteDefiMove(ctx context.Context, u account.User, data map[string]any) (map[string]any, error) {
	code := Canonical(jsString(data["asset"]))
	units, err := parseAmount(data["amount"], code)
	if err != nil {
		return nil, err
	}
	dir, _ := data["direction"].(string)
	created, err := s.defiCreated(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	switch {
	case !created:
		return nil, fail(409, "Create your local DeFi account first.")
	case dir != "to_defi" && dir != "to_main":
		return nil, fail(400, "Choose a transfer direction.")
	case units.Sign() < 1:
		return nil, fail(400, "Choose a positive amount.")
	}
	avail, err := s.Balance(ctx, u.ID, code)
	if dir == "to_main" {
		avail, err = defiBalance(ctx, s.db, u.ID, code)
	}
	if err != nil {
		return nil, err
	}
	if units.Cmp(avail) > 0 {
		return nil, fail(400, "Insufficient balance.")
	}
	id := randomHex(16)
	_, err = s.db.Exec(ctx, "INSERT INTO defi_moves(id, user_id, asset, units, direction, expires_at, created_at) VALUES ($1, $2, $3, $4::numeric, $5, $6, $7)",
		id, u.ID, code, units.String(), dir, s.unix()+300, s.unix())
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "asset": code, "amount": format(units, code), "direction": dir, "expires_in": 300}, nil
}

func (s *Service) ConfirmDefiMove(ctx context.Context, u account.User, data map[string]any) (map[string]any, error) {
	id, _ := data["id"].(string)
	if !moveIDRe.MatchString(id) {
		return nil, fail(400, "Invalid transfer request.")
	}
	done := map[string]any{"ok": true, "id": id}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := lock(ctx, tx, u.ID); err != nil {
			return err
		}
		var code, raw, dir string
		var expires int
		var completed bool
		err := tx.QueryRow(ctx, "SELECT asset, units::text, direction, expires_at, completed FROM defi_moves WHERE id=$1 AND user_id=$2 FOR UPDATE",
			id, u.ID).Scan(&code, &raw, &dir, &expires, &completed)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(404, "Transfer request not found.")
		}
		if err != nil || completed {
			return err
		}
		if expires <= s.unix() {
			return fail(410, "Transfer request expired.")
		}
		units := bigOf(raw)
		main, err := Balance(ctx, tx, u.ID, code)
		if err != nil {
			return err
		}
		defi, err := defiBalance(ctx, tx, u.ID, code)
		if err != nil {
			return err
		}
		if dir == "to_defi" {
			main.Sub(main, units)
			defi.Add(defi, units)
		} else {
			main.Add(main, units)
			defi.Sub(defi, units)
		}
		if main.Sign() < 0 || defi.Sign() < 0 {
			return fail(400, "Insufficient balance.")
		}
		if err := setBalance(ctx, tx, u.ID, code, main); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO defi_balances(user_id, asset, units) VALUES ($1, $2, $3::numeric)
			ON CONFLICT (user_id, asset) DO UPDATE SET units = excluded.units`, u.ID, code, defi.String()); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE defi_moves SET completed=true WHERE id=$1", id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return done, nil
}
