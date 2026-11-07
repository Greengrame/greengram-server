package wallet

import "testing"

// The page's Crypto tab shows only these two words, anything else leaves it empty.
func TestCoinsLandInTheCryptoTab(t *testing.T) {
	for _, a := range Assets {
		got := clientCategory(a)
		switch a.Category {
		case "":
			if got != "onchain" && got != "trading" {
				t.Errorf("%s: category %q is not on the Crypto tab", a.Code, got)
			}
		default:
			if got != a.Category {
				t.Errorf("%s: category %q, want %q", a.Code, got, a.Category)
			}
		}
	}
}
