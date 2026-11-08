package site

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Greengrame/greengram-server/internal/account"
)

// webDir is a stand-in Web A build: the shell, a script, an icon and one of
// the build's own HTML pages.
func webDir(t *testing.T) string {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"index.html":                  "<!doctype html>SHELL",
		"assets/main-abc.js":          "console.log(1)",
		"favicon.ico":                 "ico",
		"img-apple-64/1f600.png":      "png",
		"get/index.html":              "<title>Other Desktop</title>",
		"google7077b308900504fb.html": "google-site-verification",
	} {
		file := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(file), 0o755)
		if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// testSite counts a request as signed in when it has cookie session=ok, or
// session2=ok for ?account=2.
func testSite(t *testing.T, info Landing) *httptest.Server {
	s := &Site{
		WebDir: webDir(t),
		SignedIn: func(r *http.Request) bool {
			name := "session"
			if v := r.URL.Query().Get("account"); v != "" && v != "1" {
				name += v
			}
			c, err := r.Cookie(name)
			return err == nil && c.Value == "ok"
		},
		Info: func(context.Context) Landing { return info },
	}
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type answer struct {
	code     int
	location string
	body     string
	header   http.Header
}

func get(t *testing.T, base, path string, cookies ...string) answer {
	t.Helper()
	req, _ := http.NewRequest("GET", base+path, nil)
	for _, c := range cookies {
		name, value, _ := strings.Cut(c, "=")
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return answer{resp.StatusCode, resp.Header.Get("Location"), string(b), resp.Header}
}

func TestLandingForGuests(t *testing.T) {
	srv := testSite(t, Landing{Name: "Greengram", Signup: true})

	a := get(t, srv.URL, "/")
	if a.code != 200 || strings.Contains(a.body, "SHELL") {
		t.Fatalf("/ signed out: %d %q", a.code, a.body)
	}
	notice := "Greengram is an unofficial, open-source clone of Telegram.</b> It is not affiliated with, endorsed by, or connected to Telegram. Never enter your Telegram account details here: Greengram accounts are separate."
	for _, want := range []string{"<title>Greengram, an unofficial Telegram clone</title>", `name="description" content="Greengram is an unofficial`,
		`href="/login"`, `href="/login?intent=register"`, `href="/login?next=%2F"`, "Download APK", notice} {
		if !strings.Contains(a.body, want) {
			t.Errorf("landing lacks %q", want)
		}
	}
	// the notice comes before anything else on the page
	if n, h := strings.Index(a.body, notice), strings.Index(a.body, "<h1>"); n < 0 || n > h {
		t.Error("the clone notice isn't above the page's heading")
	}
	// telegram is named only to say greengram isn't it
	for _, line := range strings.Split(a.body, "\n") {
		l := strings.ToLower(line)
		if strings.Contains(l, "telegram") && !strings.Contains(l, "unofficial") && !strings.Contains(l, "not affiliated") {
			t.Errorf("telegram outside the disclaimer: %q", line)
		}
	}
	if regexp.MustCompile(`<form|<input|password`).MatchString(a.body) {
		t.Error("the landing page asks for credentials")
	}
	if a.header.Get("X-Frame-Options") != "DENY" || !strings.Contains(a.header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Errorf("landing headers %v", a.header)
	}

	// every way into the app goes through /login and comes back
	for path, next := range map[string]string{
		"/c/42/9":              "/login?next=%2Fc%2F42%2F9",
		"/c/42":                "/login?next=%2Fc%2F42",
		"/u/alice":             "/login?next=%2Fu%2Falice",
		"/alice/7":             "/login?next=%2Falice%2F7",
		"/+abc_D-1":            "/login?next=%2F%2Babc_D-1",
		"/addstickers/Pack":    "/login?next=%2Faddstickers%2FPack",
		"/addemoji/Pack":       "/login?next=%2Faddemoji%2FPack",
		"/joinchat/abc":        "/login?next=%2Fjoinchat%2Fabc",
		"/nft/PlushPepe-12":    "/login?next=%2Fnft%2FPlushPepe-12",
		"/k/":                  "/login?next=%2Fk%2F",
		"/a/":                  "/login?next=%2Fa%2F",
		"/?tgaddr=tg%3A%2F%2F": "/login?next=%2F%3Ftgaddr%3Dtg%253A%252F%252F",
		"/?account=2":          "/login?account=2&next=%2F%3Faccount%3D2",
		"/u/bob?account=3":     "/login?account=3&next=%2Fu%2Fbob%3Faccount%3D3",
		"/index.html":          "/login?next=%2Findex.html",
		"/get/index.html":      "/login?next=%2Fget%2Findex.html",
		"/get/":                "/login?next=%2Fget%2F",
	} {
		a := get(t, srv.URL, path)
		if a.code != http.StatusSeeOther || a.location != next {
			t.Errorf("%s signed out: %d %q, want %q", path, a.code, a.location, next)
		}
	}
	// Telegram's own Search Console token isn't ours to serve
	if a := get(t, srv.URL, "/google7077b308900504fb.html"); a.code != http.StatusSeeOther {
		t.Errorf("verification file: %d", a.code)
	}

	// the build's files stay public: the shell's scripts and icons load
	// before there is a session, and emoji are fetched by URL
	for path, body := range map[string]string{"/assets/main-abc.js": "console.log(1)", "/favicon.ico": "ico", "/img-apple-64/1f600.png": "png"} {
		if a := get(t, srv.URL, path); a.code != 200 || a.body != body {
			t.Errorf("%s: %d %q", path, a.code, a.body)
		}
	}
	if a := get(t, srv.URL, "/missing.js"); a.code != 404 {
		t.Errorf("missing file: %d", a.code)
	}
	if a := get(t, srv.URL, "/api/nothing"); a.code != 404 || !strings.Contains(a.body, "Not found") {
		t.Errorf("unknown api path: %d %q", a.code, a.body)
	}
	if a := get(t, srv.URL, "/robots.txt"); a.code != 200 || !strings.Contains(a.body, "Allow: /$") || !strings.Contains(a.body, "Disallow: /\n") {
		t.Errorf("robots.txt: %d %q", a.code, a.body)
	}
	for _, f := range []string{"landing.css", "landing.js"} {
		if a := get(t, srv.URL, "/__greengram/landing/"+f); a.code != 200 || a.body == "" {
			t.Errorf("%s: %d", f, a.code)
		}
	}
}

func TestSignedInGetsTheApp(t *testing.T) {
	srv := testSite(t, Landing{Name: "Greengram", Signup: true})
	a := get(t, srv.URL, "/", "session=ok")
	if a.code != 200 || !strings.Contains(a.body, "SHELL") || a.header.Get("X-Robots-Tag") != "noindex" {
		t.Fatalf("/ signed in: %d %q %v", a.code, a.body, a.header)
	}
	if a := get(t, srv.URL, "/u/alice", "session=ok"); a.code != http.StatusFound || a.location != "/#?tgaddr=tg%3A%2F%2Fresolve%3Fdomain%3Dalice" {
		t.Errorf("/u/alice signed in: %d %q", a.code, a.location)
	}
	if a := get(t, srv.URL, "/x/y/z", "session=ok"); a.code != 200 || !strings.Contains(a.body, "SHELL") {
		t.Errorf("/x/y/z signed in: %d", a.code)
	}
	if a := get(t, srv.URL, "/google7077b308900504fb.html", "session=ok"); a.code != 200 {
		t.Errorf("build page signed in: %d", a.code)
	}
	// slot 2 has its own cookie: slot 1's doesn't open it
	if a := get(t, srv.URL, "/?account=2", "session=ok"); a.code != http.StatusSeeOther {
		t.Errorf("/?account=2 with slot 1 only: %d", a.code)
	}
	if a := get(t, srv.URL, "/?account=2", "session2=ok"); a.code != 200 || !strings.Contains(a.body, "SHELL") {
		t.Errorf("/?account=2 signed in: %d", a.code)
	}
	if a := get(t, srv.URL, "/", "session=expired"); a.code != 200 || strings.Contains(a.body, "SHELL") {
		t.Errorf("dead session: %d %q", a.code, a.body)
	}
}

func TestLandingSaysWhatTheInstanceIs(t *testing.T) {
	srv := testSite(t, Landing{Name: "Acme <Chat>", Signup: false, Contact: "admin@example.org"})
	a := get(t, srv.URL, "/")
	if !strings.Contains(a.body, "<title>Acme &lt;Chat&gt; · Greengram, an unofficial Telegram clone</title>") {
		t.Errorf("title: %q", a.body)
	}
	if strings.Contains(a.body, "intent=register") {
		t.Error("create account shown with registration off")
	}
	if !strings.Contains(a.body, `href="mailto:admin@example.org"`) {
		t.Error("no admin contact")
	}
	if strings.Contains(a.body, "Discord") {
		t.Error("community link without GREENGRAM_COMMUNITY_URL")
	}
	for in, want := range map[string]string{
		"https://discord.gg/abc":     `href="https://discord.gg/abc" rel="noopener" target="_blank">Discord</a>`,
		"https://chat.example.org/x": `>Community</a>`,
		"javascript:alert(1)":        "",
	} {
		b := get(t, testSite(t, Landing{Name: "Greengram", Community: in}).URL, "/").body
		if want == "" && strings.Contains(b, "alert(1)") || want != "" && !strings.Contains(b, want) {
			t.Errorf("community %q: %q", in, b)
		}
	}

	for contact, want := range map[string]string{
		"https://example.org/about": `<a href="https://example.org/about" rel="nofollow">https://example.org/about</a>`,
		"@alice on the instance":    `admin: @alice on the instance`,
		"javascript:alert(1)":       `admin: javascript:alert(1)`,
	} {
		srv := testSite(t, Landing{Name: "Greengram", Contact: contact})
		if a := get(t, srv.URL, "/"); !strings.Contains(a.body, want) {
			t.Errorf("contact %q: no %q", contact, want)
		}
	}
	srv = testSite(t, Landing{Name: "Greengram"})
	if a := get(t, srv.URL, "/"); strings.Contains(a.body, "admin:") {
		t.Error("contact line without a contact")
	}
}

// Without a panel build /admin says so, instead of reading as a link to @admin.
func TestAdminWithoutPanel(t *testing.T) {
	s := &Site{WebDir: webDir(t), SignedIn: func(*http.Request) bool { return true },
		Admin: func(*http.Request) (account.User, string, error) { return account.User{}, "", nil }}
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if a := get(t, srv.URL, "/admin", "session=ok"); a.code != 404 || !strings.Contains(a.body, "GREENGRAM_ADMIN_DIR") {
		t.Fatalf("/admin: %d %q %q", a.code, a.location, a.body)
	}
}
