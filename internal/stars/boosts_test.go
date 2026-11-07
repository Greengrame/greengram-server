package stars

import "testing"

func TestBoostCurve(t *testing.T) {
	// small chat: the first boost makes level 1, each next level takes one more
	for _, c := range []struct{ members, level, boosts int }{
		{1, 1, 1}, {1, 2, 2}, {1, 8, 8}, {1, 9, 9}, {1, 10, 10}, {1, 100, 100},
		{250, 1, 1}, {250, 8, 8}, {250, 9, 14}, {250, 10, 20},
		{1000, 1, 3}, {1000, 2, 5}, {1000, 8, 20}, {1000, 9, 35}, {1000, 10, 50},
	} {
		if got := boostsForLevel(c.level, c.members); got != c.boosts {
			t.Errorf("%d members level %d: %d boosts, want %d", c.members, c.level, got, c.boosts)
		}
	}
	for _, c := range []struct{ boosts, members, level int }{{0, 1, 0}, {1, 1, 1}, {2, 1, 2}, {3, 1, 3}, {1000, 1, 100}, {2, 1000, 0}, {3, 1000, 1}, {34, 1000, 8}, {35, 1000, 9}} {
		if got := boostLevel(c.boosts, c.members); got != c.level {
			t.Errorf("%d boosts, %d members: level %d, want %d", c.boosts, c.members, got, c.level)
		}
	}
}

func TestRatingLevels(t *testing.T) {
	for _, c := range []struct {
		stars        int64
		level        int
		current, next int64
	}{
		{0, 0, 0, 1}, {1, 1, 1, 5000}, {4999, 1, 1, 5000}, {5000, 2, 5000, 12000}, {12000, 3, 12000, 19000},
		{81000, 10, 81000, 94100}, {1000000, 80, 1000000, 1013157}, {2000000, 99, 1250000, 0}, {-80, -1, -80, 0},
	} {
		l, cur, next := ratingLevel(c.stars)
		if l != c.level || cur != c.current || next != c.next {
			t.Errorf("%d stars: level %d (%d..%d), want %d (%d..%d)", c.stars, l, cur, next, c.level, c.current, c.next)
		}
	}
	if s := ratingLevelStars(75); s < 930000 || s > 940000 {
		t.Errorf("level 75 at %d, want about 934000", s)
	}
}
