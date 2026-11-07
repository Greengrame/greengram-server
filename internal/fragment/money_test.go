package fragment

import (
	"math/big"
	"testing"
)

func TestMulDecimal(t *testing.T) {
	for _, c := range []struct{ a, b, want string }{
		{"2.5", "5.5", "13.75"},
		{"10", "3", "30"},
		{"0.000000001", "1.2e3", "0.0000012"},
		{"1.50", "2", "3"},
		{"0", "7.1", "0"},
	} {
		if got, ok := mulDecimal(c.a, c.b); !ok || got != c.want {
			t.Errorf("%s*%s = %q %v, want %q", c.a, c.b, got, ok, c.want)
		}
	}
	if _, ok := mulDecimal("-1", "2"); ok {
		t.Error("negative accepted")
	}
}

func TestUSDCents(t *testing.T) {
	if c, ok := usdCents(big.NewInt(2_500_000_000), "5.5"); !ok || c != 1375 {
		t.Fatalf("2.5 TON at $5.5 = %d %v", c, ok)
	}
	if c, _ := usdCents(big.NewInt(1), "5.5"); c != 0 {
		t.Fatalf("one nanogram rounds down, got %d", c)
	}
}

func TestDisplayTitle(t *testing.T) {
	it := item{Title: "+88812345678", CountryCode: "888"}
	if got := it.displayTitle(); got != "+888 12345678" {
		t.Fatal(got)
	}
	it.DisplayFormat = "+888 1234 5678"
	if got := it.displayTitle(); got != "+888 1234 5678" {
		t.Fatal(got)
	}
	if got := (item{Title: "@name"}).displayTitle(); got != "@name" {
		t.Fatal(got)
	}
}

func TestParseTON(t *testing.T) {
	if n, err := parseTON("1.5"); err != nil || n.String() != "1500000000" {
		t.Fatal(n, err)
	}
	if _, err := parseTON("1.0000000001"); err == nil || err.Error() != "Use up to 9 decimal places for TON." {
		t.Fatal(err)
	}
	if _, err := parseTON("1e3"); err == nil || err.Error() != "Choose a supported asset and a decimal amount." {
		t.Fatal(err)
	}
}
