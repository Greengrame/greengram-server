package webpage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/tg"
)

func TestPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false, "169.254.169.254": false,
		"100.64.0.1": false, "0.0.0.0": false, "::1": false, "fe80::1": false, "fd00::1": false, "::ffff:127.0.0.1": false,
		"64:ff9b::a00:1": false, "224.0.0.1": false,
	} {
		if got := Public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Public(%s) = %v", addr, got)
		}
	}
}

// The real fetcher refuses loopback, directly and through a redirect.
func TestFetcherRefusesPrivate(t *testing.T) {
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<meta property="og:title" content="secret">`))
	}))
	defer inside.Close()
	f := NewFetcher()
	if _, err := f.Fetch(context.Background(), inside.URL); !errors.Is(err, errBlocked) {
		t.Fatalf("loopback fetch: %v", err)
	}
	if _, err := f.Fetch(context.Background(), "file:///etc/passwd"); !errors.Is(err, errBlocked) {
		t.Fatalf("file url: %v", err)
	}
}

func TestParse(t *testing.T) {
	big := strings.Repeat("x", 3000)
	p := parse([]byte(`<html><head><title> Fallback  title </title>
		<meta property="og:title" content="The &amp; Title">
		<meta name="description" content="` + big + `">
		<meta property="og:image" content="/pic.png">
		</head><body><meta property="og:title" content="not this"></body></html>`))
	if p.Title != "The & Title" || p.Image != "/pic.png" || len([]rune(p.Description)) != maxField || p.Type != "article" {
		t.Fatalf("parse %+v", p)
	}
	if p := parse([]byte(`<title>Only a title</title>`)); p.Title != "Only a title" {
		t.Fatalf("title fallback %+v", p)
	}
}

func TestFetchFollowsAndResolvesImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<meta property="og:title" content="Moved"><meta property="og:image" content="img.png">`))
	}))
	defer srv.Close()
	f := NewFetcherFor(func(netip.Addr) bool { return true })
	p, err := f.Fetch(context.Background(), srv.URL+"/old")
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Moved" || p.URL != srv.URL+"/new" || p.Image != srv.URL+"/img.png" {
		t.Fatalf("page %+v", p)
	}
}

func TestFirstURL(t *testing.T) {
	text := "привет https://example.com/a?b=1. and more"
	if got := FirstURL(text, nil); got != "https://example.com/a?b=1" {
		t.Fatalf("plain link %q", got)
	}
	// "привет " is 7 UTF-16 units
	ent := []tg.MessageEntityClass{&tg.MessageEntityURL{Offset: 7, Length: 11}}
	if got := FirstURL("привет example.org rest", ent); got != "https://example.org" {
		t.Fatalf("url entity %q", got)
	}
	if got := FirstURL("x", []tg.MessageEntityClass{&tg.MessageEntityTextURL{Offset: 0, Length: 1, URL: "http://Example.NET/p#frag"}}); got != "http://example.net/p" {
		t.Fatalf("text url %q", got)
	}
	if got := FirstURL("no links here", nil); got != "" {
		t.Fatalf("no link %q", got)
	}
}
