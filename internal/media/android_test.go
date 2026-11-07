package media

import (
	"reflect"
	"testing"
)

func TestEmojiTokens(t *testing.T) {
	for in, want := range map[string][]string{
		"😺️👍":       {"😺", "👍"},
		"👍🏻 cat":          {"👍🏻"},
		"👩‍💻❤️": {"👩‍💻", "❤"},
		"🇺🇦🇩🇪":           {"🇺🇦", "🇩🇪"},
		"dogs":           nil,
	} {
		if got := EmojiTokens(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
