package e2e

import (
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/admin"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/secret"
	"github.com/Greengrame/greengram-server/internal/settings"
	"github.com/Greengrame/greengram-server/internal/site"
	"github.com/Greengrame/greengram-server/internal/web"
)

// The home page with real sessions: signed out it's the landing page and
// links go through /login, the cookie /login sets opens the app, and the
// server's own pages answer as before.
func TestLandingAndSessions(t *testing.T) {
	st := db(t)
	inst := make([]byte, 32)
	rand.Read(inst)
	set := settings.New(st.Pool)
	accounts := account.New(st.Pool, secret.FromBytes(inst), set)

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html>WEB-A-SHELL"), 0o644)
	adminDir := t.TempDir()
	os.WriteFile(filepath.Join(adminDir, "index.html"), []byte("<!doctype html>ADMIN"), 0o644)

	mux := http.NewServeMux()
	auth := &web.Auth{Accounts: accounts}
	auth.Register(mux)
	adm := admin.New(st.Pool, accounts, set, channels.New(st.Pool))
	adm.Register(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	(&site.Site{WebDir: dir, AdminDir: adminDir, Admin: adm.Admin, SignedIn: auth.SignedIn,
		Info: func(ctx context.Context) site.Landing {
			doc, _ := set.All(ctx)
			name, _ := doc["siteName"].(string)
			signup, _ := doc["registrationEnabled"].(bool)
			return site.Landing{Name: name, Signup: signup}
		}}).Register(mux)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string) (int, string, string) {
		t.Helper()
		resp, err := c.Get(hs.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Location"), string(b)
	}

	code, _, body := get("/")
	if code != 200 || strings.Contains(body, "WEB-A-SHELL") || !strings.Contains(body, "<title>Greengram, an unofficial Telegram clone") || !strings.Contains(body, "Create account") {
		t.Fatalf("/ signed out: %d %q", code, body)
	}
	for _, path := range []string{"/c/5/7", "/u/ada", "/ada", "/+invite", "/addstickers/Set", "/nft/Cap-3", "/k/", "/?tgaddr=x"} {
		if code, loc, _ := get(path); code != http.StatusSeeOther || !strings.HasPrefix(loc, "/login?next=") {
			t.Errorf("%s signed out: %d %q", path, code, loc)
		}
	}
	if code, loc, _ := get("/admin"); code != http.StatusSeeOther || loc != "/login?next=admin" {
		t.Errorf("/admin signed out: %d %q", code, loc)
	}
	if code, _, body := get("/login"); code != 200 || !strings.Contains(body, `id="phone"`) || !strings.Contains(body, "unofficial, open-source clone of Telegram") {
		t.Errorf("/login: %d", code)
	}
	if code, _, body := get("/healthz"); code != 200 || body != "ok\n" {
		t.Errorf("/healthz: %d %q", code, body)
	}
	if code, _, body := get("/api/branding"); code != 200 || !strings.Contains(body, `"name":"Greengram"`) {
		t.Errorf("/api/branding: %d %q", code, body)
	}
	if code, _, body := get("/branding/logo"); code != 200 || !strings.HasPrefix(body, "\x89PNG\r\n") {
		t.Errorf("default logo: %d %q", code, body)
	}

	// sign in the way /login does; the cookie it sets opens the app
	resp, err := c.Post(hs.URL+"/api/register", "application/json", strings.NewReader(`{"username":"ada","name":"Ada","password":"correct horse 1"}`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("register: %v %v", err, resp.Status)
	}
	resp.Body.Close()
	if code, _, body := get("/"); code != 200 || !strings.Contains(body, "WEB-A-SHELL") {
		t.Fatalf("/ signed in: %d %q", code, body)
	}
	if code, loc, _ := get("/u/ada"); code != http.StatusFound || !strings.HasPrefix(loc, "/#?tgaddr=") {
		t.Errorf("/u/ada signed in: %d %q", code, loc)
	}
	if code, _, body := get("/x/y/z"); code != 200 || !strings.Contains(body, "WEB-A-SHELL") {
		t.Errorf("/x/y/z signed in: %d", code)
	}
	// slot 2 is still signed out
	if code, loc, _ := get("/?account=2"); code != http.StatusSeeOther || loc != "/login?account=2&next=%2F%3Faccount%3D2" {
		t.Errorf("/?account=2: %d %q", code, loc)
	}

	// signed out again: back to the landing page
	resp, err = c.Post(hs.URL+"/api/logout", "application/json", strings.NewReader(`{}`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("logout: %v %v", err, resp.Status)
	}
	resp.Body.Close()
	if code, _, body := get("/"); code != 200 || strings.Contains(body, "WEB-A-SHELL") {
		t.Errorf("/ after logout: %d", code)
	}
}
