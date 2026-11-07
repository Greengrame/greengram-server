package wallet

import (
	"context"
	"net/url"
	"testing"
)

func TestWaltLanguageFor(t *testing.T) {
	for in, want := range map[string]string{
		"uk": "uk", "ru-RU": "ru", "pt-br": "pt", "zh-hant": "zh_HANT", "zh-TW": "zh_HANT",
		"zh-hans": "", "zh": "", "fr": "", "": "",
	} {
		if got := waltLanguageFor(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestWithLaunchData(t *testing.T) {
	raw := url.Values{"user": {`{"id":1,"language_code":"uk"}`}}.Encode()
	if got, _ := WithLaunchData(context.Background(), raw).Value(launchLanguageKey{}).(string); got != "uk" {
		t.Fatalf("got %q", got)
	}
	if got, _ := WithLaunchData(context.Background(), "%zz").Value(launchLanguageKey{}).(string); got != "" {
		t.Fatalf("bad data gave %q", got)
	}
}
