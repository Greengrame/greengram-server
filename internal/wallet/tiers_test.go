package wallet

import (
	"math/big"
	"testing"
	"time"
)

func TestTierAt(t *testing.T) {
	month := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		current, previous int64
		tier              string
		end               any
	}{
		{0, 0, "base", nil},
		{299, 0, "base", nil},
		{300, 0, "bronze", "2026-12-31T23:59:59Z"},
		{2000, 0, "silver", "2026-12-31T23:59:59Z"},
		{30000, 0, "gold", "2026-12-31T23:59:59Z"},
		{0, 2000, "silver", "2026-11-30T23:59:59Z"},
		{300, 30000, "gold", "2026-11-30T23:59:59Z"},
		{2000, 2000, "silver", "2026-12-31T23:59:59Z"},
	} {
		tier, end := tierAt(big.NewRat(c.current, 1), big.NewRat(c.previous, 1), month)
		if tier != c.tier || end != c.end {
			t.Errorf("%d this month, %d last: %s until %v, want %s until %v", c.current, c.previous, tier, end, c.tier, c.end)
		}
	}
}
