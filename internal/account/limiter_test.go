package account

import (
	"testing"
	"time"
)

// One host owns a whole IPv6 /64; rotating through it must not reset the
// count.
func TestLimitIP(t *testing.T) {
	cases := map[string]string{
		"2001:db8:1:2:aaaa::1":   "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::9":   "2001:db8:1:2::/64",
		"2001:db8:1:3:0:0:0:abc": "2001:db8:1:3::/64",
		"::ffff:203.0.113.7":     "203.0.113.7",
		"203.0.113.7":            "203.0.113.7",
		"not an address":         "not an address",
	}
	for in, want := range cases {
		if got := limitIP(in); got != want {
			t.Errorf("limitIP(%q) = %q, want %q", in, got, want)
		}
	}

	l := limiter{m: map[string]*bucket{}}
	now := time.Unix(1_800_000_000, 0)
	for i, ip := range []string{"2001:db8::1", "2001:db8::2", "2001:db8::3"} {
		err := l.hit("x:"+limitIP(ip), 2, time.Minute, now)
		if (err != nil) != (i == 2) {
			t.Fatalf("hit %d from the same /64: %v", i+1, err)
		}
	}
}

// Ended windows go in a pass at most once a minute, not on every hit.
func TestLimiterSweep(t *testing.T) {
	l := limiter{m: map[string]*bucket{}}
	now := time.Unix(1_800_000_000, 0)
	l.hit("a", 5, time.Second, now)
	l.hit("b", 5, time.Second, now.Add(2*time.Second))
	if len(l.m) != 2 {
		t.Fatalf("swept before a minute passed: %d buckets", len(l.m))
	}
	l.hit("c", 5, time.Second, now.Add(limiterSweep+time.Second))
	if _, ok := l.m["a"]; ok || len(l.m) != 1 {
		t.Fatalf("ended windows kept: %d buckets", len(l.m))
	}
}
