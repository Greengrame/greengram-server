package wallet

import (
	"errors"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// Prices come from the market feeds as decimal strings ("0.0000123",
// "1.2e-7") and every conversion here is exact integer math on them, with
// the same rounding as crypto-stars-price.js and crypto-swap-price.js.

var decimalRe = regexp.MustCompile(`(?i)^(\d+)(?:\.(\d+))?(?:e([+-]?\d+))?$`)

var errPrice = errors.New("wallet: invalid market price")

func pow10(n int) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil) }

// decimal splits "12.5e3" into its digits and the exponent that puts the
// point back.
func decimal(value string) (digits *big.Int, scale int, ok bool) {
	m := decimalRe.FindStringSubmatch(value)
	if m == nil || len(m[1]) > 100 || len(m[2]) > 100 {
		return nil, 0, false
	}
	exp := 0
	if m[3] != "" {
		e, err := strconv.Atoi(m[3])
		if err != nil || e > 100 || e < -100 {
			return nil, 0, false
		}
		exp = e
	}
	digits, _ = new(big.Int).SetString(m[1]+m[2], 10)
	return digits, len(m[2]) - exp, true
}

// units is decimalUnits(): an integer and how many decimals it carries.
func units(value string) (*big.Int, int, bool) {
	d, scale, ok := decimal(value)
	if !ok {
		return nil, 0, false
	}
	if scale < 0 {
		return d.Mul(d, pow10(-scale)), 0, true
	}
	return d, scale, true
}

func formatScaled(n *big.Int, decimals int) string {
	if decimals == 0 {
		return n.String()
	}
	s := n.String()
	if len(s) < decimals+1 {
		s = strings.Repeat("0", decimals+1-len(s)) + s
	}
	whole, frac := s[:len(s)-decimals], strings.TrimRight(s[len(s)-decimals:], "0")
	if frac == "" {
		return whole
	}
	return whole + "." + frac
}

// MultiplyMarketAmount is amount * price, exactly, as a decimal string.
func MultiplyMarketAmount(amount, price string) (string, bool) {
	a, ad, ok := units(amount)
	if !ok {
		return "", false
	}
	p, pd, ok := units(price)
	if !ok {
		return "", false
	}
	return formatScaled(new(big.Int).Mul(a, p), ad+pd), true
}

// SumMarketAmounts adds decimal strings; "0" for none.
func SumMarketAmounts(values []string) (string, bool) {
	type num struct {
		n *big.Int
		d int
	}
	list := make([]num, 0, len(values))
	decimals := 0
	for _, v := range values {
		n, d, ok := units(v)
		if !ok {
			return "", false
		}
		list = append(list, num{n, d})
		decimals = max(decimals, d)
	}
	total := new(big.Int)
	for _, x := range list {
		total.Add(total, new(big.Int).Mul(x.n, pow10(decimals-x.d)))
	}
	return formatScaled(total, decimals), true
}

// CryptoUnitsForStars is what `stars` cost in base units of an asset at
// price USD per coin, rounded up so the instance never sells below rate.
func CryptoUnitsForStars(stars, starsPerUSD int64, price string, decimals int) (*big.Int, error) {
	if stars < 1 || stars > 1000000000 || starsPerUSD < 1 || starsPerUSD > 1000000 || decimals < 0 || decimals > 18 {
		return nil, errors.New("wallet: invalid Stars quantity or market price")
	}
	d, scale, ok := decimal(price)
	if !ok {
		return nil, errPrice
	}
	den := new(big.Int).Mul(d, big.NewInt(starsPerUSD))
	if den.Sign() < 1 {
		return nil, errors.New("wallet: a positive market price is required")
	}
	num := new(big.Int).Mul(big.NewInt(stars), pow10(decimals+max(0, scale)))
	if scale < 0 {
		den.Mul(den, pow10(-scale))
	}
	out := num.Add(num, den).Sub(num, big.NewInt(1)).Quo(num, den)
	if len(out.String()) > 60 {
		return nil, errors.New("wallet: the quoted crypto amount is too large")
	}
	return out, nil
}

// MaxStarsForCrypto is the most Stars a balance buys.
func MaxStarsForCrypto(available *big.Int, starsPerUSD int64, price string, decimals int) (int64, error) {
	if available.Sign() < 0 {
		return 0, errors.New("wallet: a nonnegative balance is required")
	}
	low, high := int64(0), int64(1000000000)
	for low < high {
		mid := (low + high + 1) / 2
		cost, err := CryptoUnitsForStars(mid, starsPerUSD, price, decimals)
		if err != nil {
			return 0, err
		}
		if cost.Cmp(available) <= 0 {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return low, nil
}

// fraction is a price as num/den.
func fraction(value string) (num, den *big.Int, err error) {
	d, scale, ok := decimal(value)
	if !ok {
		return nil, nil, errPrice
	}
	if d.Sign() < 1 {
		return nil, nil, errors.New("wallet: a positive market price is required")
	}
	return d.Mul(d, pow10(max(0, -scale))), pow10(max(0, scale)), nil
}

// SwapUnits converts amount base units of one asset into the other at the
// two USD prices. Sending rounds down; receive mode (amount is what to
// get) rounds the cost up.
func SwapUnits(amount *big.Int, fromPrice, toPrice string, fromDecimals, toDecimals int, receive bool) (*big.Int, error) {
	fn, fd, err := fraction(fromPrice)
	if err != nil {
		return nil, err
	}
	tn, td, err := fraction(toPrice)
	if err != nil {
		return nil, err
	}
	if amount.Sign() < 0 || fromDecimals < 0 || fromDecimals > 18 || toDecimals < 0 || toDecimals > 18 {
		return nil, errors.New("wallet: invalid exchange amount")
	}
	num := new(big.Int).Mul(fn, td)
	num.Mul(num, pow10(toDecimals))
	den := new(big.Int).Mul(fd, tn)
	den.Mul(den, pow10(fromDecimals))
	if receive {
		out := new(big.Int).Mul(amount, den)
		out.Add(out, num).Sub(out, big.NewInt(1))
		return out.Quo(out, num), nil
	}
	out := new(big.Int).Mul(amount, num)
	return out.Quo(out, den), nil
}

// SwapRate is how many `to` coins one `from` coin buys, to 24 places.
func SwapRate(fromPrice, toPrice string) (string, error) {
	fn, fd, err := fraction(fromPrice)
	if err != nil {
		return "", err
	}
	tn, td, err := fraction(toPrice)
	if err != nil {
		return "", err
	}
	n := new(big.Int).Mul(fn, td)
	n.Mul(n, pow10(24))
	n.Quo(n, new(big.Int).Mul(fd, tn))
	return formatScaled(n, 24), nil
}
