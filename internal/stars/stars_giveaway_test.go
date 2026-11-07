package stars

import "testing"

func TestGiveawayOptionsSplitEvenly(t *testing.T) {
	for _, stars := range giveawayStars {
		for _, n := range giveawayWinners {
			if stars%int64(n) != 0 {
				t.Errorf("%d stars don't split over %d winners", stars, n)
			}
			if n > maxGiveawayUsers {
				t.Errorf("%d winners is past the cap", n)
			}
		}
	}
}

func TestYearlyBoosts(t *testing.T) {
	const annual = 2250 // the default, 4 boosts for a year of Premium
	for _, c := range []struct {
		stars int64
		want  int
	}{{500, 1}, {2250, 4}, {2500, 4}, {100000, 177}} {
		if got := yearlyBoosts(c.stars, annual); got != c.want {
			t.Errorf("yearlyBoosts(%d) = %d, want %d", c.stars, got, c.want)
		}
	}
}

func TestBoostShareAddsUp(t *testing.T) {
	for _, c := range []struct{ boosts, quantity int }{{1, 100}, {4, 5}, {10, 3}, {177, 100}, {8, 2}} {
		sum := 0
		for i := 0; i < c.quantity; i++ {
			sum += boostShare(c.boosts, c.quantity, i)
		}
		if sum != c.boosts {
			t.Errorf("%d boosts over %d rows came to %d", c.boosts, c.quantity, sum)
		}
	}
}
