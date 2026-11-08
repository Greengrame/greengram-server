package wallet

import (
	"context"
	"math/big"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// Moved is one asset that left a deleted account: Units in base units, From
// the place it was kept (main, defi or earn).
type Moved struct {
	Asset string `json:"asset"`
	From  string `json:"from"`
	Units string `json:"units"`
}

// MoveAll empties every balance user has, main, DeFi and Earn alike, into
// to's main account, inside the caller's transaction (both held under
// stars.Lock). Each move is a pair of events with the same key prefix, so
// the sum of all balances per asset is what it was before.
func MoveAll(ctx context.Context, tx pgx.Tx, user, to int64, keyPrefix string, now int) ([]Moved, error) {
	type held struct {
		from, asset string
		units       *big.Int
		campaign    int64
	}
	var list []held
	collect := func(from, sql string) error {
		rows, err := tx.Query(ctx, sql, user)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h held
			var units string
			if err := rows.Scan(&h.asset, &units, &h.campaign); err != nil {
				return err
			}
			h.from, h.units = from, bigOf(units)
			if h.units.Sign() > 0 {
				list = append(list, h)
			}
		}
		return rows.Err()
	}
	if err := collect("main", "SELECT asset, units::text, 0::bigint FROM crypto_accounts WHERE user_id=$1 AND units > 0 ORDER BY asset"); err != nil {
		return nil, err
	}
	if err := collect("defi", "SELECT asset, units::text, 0::bigint FROM defi_balances WHERE user_id=$1 AND units > 0 ORDER BY asset"); err != nil {
		return nil, err
	}
	if err := collect("earn", "SELECT '', units::text, campaign_id FROM earn_accounts WHERE user_id=$1 AND units > 0 ORDER BY campaign_id"); err != nil {
		return nil, err
	}
	var out []Moved
	for _, h := range list {
		key := keyPrefix + ":" + h.from + ":" + h.asset
		switch h.from {
		case "main":
			if err := setBalance(ctx, tx, user, h.asset, new(big.Int)); err != nil {
				return nil, err
			}
		case "defi":
			if _, err := tx.Exec(ctx, "UPDATE defi_balances SET units=0 WHERE user_id=$1 AND asset=$2", user, h.asset); err != nil {
				return nil, err
			}
		case "earn":
			c, ok := campaignByID(strconv.FormatInt(h.campaign, 10))
			if !ok {
				// a campaign this build doesn't know: leave it where it is
				continue
			}
			h.asset = Canonical(c.Code)
			key = keyPrefix + ":earn:" + c.Name
			if _, err := tx.Exec(ctx, "UPDATE earn_accounts SET units=0 WHERE user_id=$1 AND campaign_id=$2", user, h.campaign); err != nil {
				return nil, err
			}
		}
		if _, err := change(ctx, tx, to, h.asset, h.units); err != nil {
			return nil, err
		}
		if err := eraseEvent(ctx, tx, key, user, h.asset, new(big.Int).Neg(h.units), to, now); err != nil {
			return nil, err
		}
		if err := eraseEvent(ctx, tx, key+":in", to, h.asset, h.units, user, now); err != nil {
			return nil, err
		}
		out = append(out, Moved{Asset: h.asset, From: h.from, Units: h.units.String()})
	}
	// quotes still open would otherwise pay out or charge later
	for _, q := range []string{
		"UPDATE crypto_stars_quotes SET expires_at=0 WHERE user_id=$1 AND result IS NULL",
		"UPDATE crypto_transfer_quotes SET expires_at=0 WHERE (user_id=$1 OR recipient_id=$1) AND NOT completed",
		"UPDATE crypto_swap_quotes SET expires_at=0 WHERE user_id=$1 AND result IS NULL",
		"UPDATE defi_moves SET expires_at=0 WHERE user_id=$1 AND NOT completed",
		"DELETE FROM wallet_security WHERE user_id=$1",
	} {
		if _, err := tx.Exec(ctx, q, user); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func eraseEvent(ctx context.Context, tx pgx.Tx, key string, user int64, code string, units *big.Int, peer int64, now int) error {
	_, err := tx.Exec(ctx, "INSERT INTO crypto_events(event_key, user_id, asset, units, kind, peer_id, created_at) VALUES ($1, $2, $3, $4::numeric, 'account_erased', $5, $6)",
		key, user, code, units.String(), peer, now)
	return err
}

// Holding is what one account keeps of an asset, formatted: Main is the
// spendable balance, DeFi and Earn the parked ones.
type Holding struct {
	Asset string `json:"asset"`
	Main  string `json:"main"`
	DeFi  string `json:"defi"`
	Earn  string `json:"earn"`
}

// Holdings lists every asset user holds anything of, for the admin panel.
func Holdings(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, user int64) ([]Holding, error) {
	sums := map[string]*[3]*big.Int{}
	var order []string
	add := func(asset string, i int, units string) {
		h, ok := sums[asset]
		if !ok {
			h = &[3]*big.Int{new(big.Int), new(big.Int), new(big.Int)}
			sums[asset] = h
			order = append(order, asset)
		}
		h[i].Add(h[i], bigOf(units))
	}
	rows, err := q.Query(ctx, `SELECT asset, 0, units::text FROM crypto_accounts WHERE user_id=$1 AND units > 0
		UNION ALL SELECT asset, 1, units::text FROM defi_balances WHERE user_id=$1 AND units > 0
		UNION ALL SELECT campaign_id::text, 2, units::text FROM earn_accounts WHERE user_id=$1 AND units > 0`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var asset, units string
		var i int
		if err := rows.Scan(&asset, &i, &units); err != nil {
			return nil, err
		}
		if i == 2 {
			c, ok := campaignByID(asset)
			if !ok {
				continue
			}
			asset = c.Code
		}
		add(Canonical(asset), i, units)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Holding, 0, len(order))
	for _, asset := range order {
		h := sums[asset]
		out = append(out, Holding{Asset: asset, Main: FormatAmount(h[0], asset), DeFi: FormatAmount(h[1], asset), Earn: FormatAmount(h[2], asset)})
	}
	return out, nil
}
