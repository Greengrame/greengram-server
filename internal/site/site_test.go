package site

import (
	"net/url"
	"testing"
)

func TestClientLink(t *testing.T) {
	for in, want := range map[string]string{
		"/u/alice":            "tg://resolve?domain=alice",
		"/u/alice/7":          "tg://resolve?domain=alice&post=7",
		"/u/news?boost":       "tg://boost?domain=news",
		"/c/42/1?boost":       "tg://boost?channel=42",
		"/c/42/9":             "tg://privatepost?channel=42&post=9",
		"/+abc_D-1":           "tg://join?invite=abc_D-1",
		"/nft/PlushPepe-12":   "tg://nft?slug=PlushPepe-12",
		"/nft/PlushPepe":      "",
		"/u/nft/PlushPepe-12": "tg://nft?slug=PlushPepe-12",
		"/u/addstickers/Cats": "tg://addstickers?set=Cats",
		"/u/c/42/9":           "tg://privatepost?channel=42&post=9",
		"/u/nft":              "tg://resolve?domain=nft",
		"/nft/a%22-1":         "",
		"/c/42":               "",
		"/u/a/b/c":            "",
		"/u/%22%3E":           "",
		"/assets/main.js":     "",
		"/":                   "",
	} {
		u, err := url.Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if want != "" {
			got = "/#?tgaddr=" + url.QueryEscape(want)
		}
		if l := clientLink(u); l != got {
			t.Errorf("%s: %q, want %q", in, l, got)
		}
	}
}
