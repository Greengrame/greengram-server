package fragment

import (
	"context"
	"errors"
	"math/big"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/wallet"
)

func tonBalance(ctx context.Context, q querier, user int64) (*big.Int, error) {
	var units string
	err := q.QueryRow(ctx, "SELECT units::text FROM crypto_accounts WHERE user_id=$1 AND asset='TON'", user).Scan(&units)
	if errors.Is(err, pgx.ErrNoRows) {
		return new(big.Int), nil
	}
	if err != nil {
		return nil, err
	}
	n, _ := new(big.Int).SetString(units, 10)
	return n, nil
}

var errNoGram = fail(400, "You do not have enough GRAM in Wallet.")

// moveTON changes a balance relative to what the row holds at that moment,
// so a wallet operation landing in between can't be overwritten; a debit
// that would go negative changes nothing.
func moveTON(ctx context.Context, tx pgx.Tx, user int64, delta *big.Int) error {
	if delta.Sign() >= 0 {
		_, err := tx.Exec(ctx, `INSERT INTO crypto_accounts(user_id, asset, units) VALUES ($1, 'TON', $2::numeric)
			ON CONFLICT (user_id, asset) DO UPDATE SET units = crypto_accounts.units + excluded.units`, user, delta.String())
		return err
	}
	debit := new(big.Int).Neg(delta).String()
	tag, err := tx.Exec(ctx, "UPDATE crypto_accounts SET units = units - $2::numeric WHERE user_id=$1 AND asset='TON' AND units >= $2::numeric", user, debit)
	if err == nil && tag.RowsAffected() != 1 {
		err = errNoGram
	}
	return err
}

func tonEvent(ctx context.Context, tx pgx.Tx, key string, user int64, units *big.Int, kind string, peer int64, now int) error {
	var p *int64
	if peer != 0 {
		p = &peer
	}
	_, err := tx.Exec(ctx, "INSERT INTO crypto_events(event_key, user_id, asset, units, kind, peer_id, created_at) VALUES ($1, $2, 'TON', $3::numeric, $4, $5, $6)",
		key, user, units.String(), kind, p, now)
	return err
}

var amountRe = regexp.MustCompile(`^\d+(?:\.\d+)?$`)

// parseTON is parseCryptoAmount for GRAM prices, with its two messages.
func parseTON(v string) (*big.Int, error) {
	if len(v) > 60 || !amountRe.MatchString(v) {
		return nil, fail(400, "Choose a supported asset and a decimal amount.")
	}
	n, ok := wallet.ParseAmount(v, "TON")
	if !ok {
		return nil, fail(400, "Use up to 9 decimal places for TON.")
	}
	return n, nil
}

var decimalRe = regexp.MustCompile(`(?i)^(\d+)(?:\.(\d+))?(?:e([+-]?\d+))?$`)

// decimal reads a market number as digits/10^scale, exactly.
func decimal(v string) (*big.Int, int, bool) {
	m := decimalRe.FindStringSubmatch(v)
	if m == nil || len(m[1]) > 100 || len(m[2]) > 100 {
		return nil, 0, false
	}
	n, _ := new(big.Int).SetString(m[1]+m[2], 10)
	scale := len(m[2])
	if m[3] != "" {
		e, ok := new(big.Int).SetString(strings.TrimPrefix(m[3], "+"), 10)
		if !ok || !e.IsInt64() || e.Int64() > 100 || e.Int64() < -100 {
			return nil, 0, false
		}
		scale -= int(e.Int64())
	}
	if scale < 0 {
		n.Mul(n, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-scale)), nil))
		scale = 0
	}
	return n, scale, true
}

// mulDecimal is multiplyMarketAmount: an exact product, trailing zeros cut.
func mulDecimal(a, b string) (string, bool) {
	x, xs, ok1 := decimal(a)
	y, ys, ok2 := decimal(b)
	if !ok1 || !ok2 {
		return "", false
	}
	p := new(big.Int).Mul(x, y)
	scale := xs + ys
	digits := p.String()
	if scale == 0 {
		return digits, true
	}
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	whole, frac := digits[:len(digits)-scale], strings.TrimRight(digits[len(digits)-scale:], "0")
	if frac == "" {
		return whole, true
	}
	return whole + "." + frac, true
}

// usdCents is the price of nanograms in whole cents, rounded down.
func usdCents(nano *big.Int, price string) (int64, bool) {
	p, scale, ok := decimal(price)
	if !ok {
		return 0, false
	}
	n := new(big.Int).Mul(nano, p)
	n.Mul(n, big.NewInt(100))
	n.Quo(n, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(9+scale)), nil))
	if !n.IsInt64() {
		return 0, false
	}
	return n.Int64(), true
}

func equalFold(a, b string) bool { return strings.EqualFold(a, b) }
