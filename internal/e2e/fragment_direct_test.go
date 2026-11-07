package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/fragment"
	"github.com/Greengrame/greengram-server/internal/miniapps"
	"github.com/Greengrame/greengram-server/internal/web"
)

// pageAs asks for a Fragment page the way fragment-adapter.js does, from a
// plain browser tab: no launch data, the browser's cookies if any.
func pageAs(t *testing.T, c *http.Client, base, path string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", base+path, nil)
	req.Header.Set("X-Greengram-Page", "1")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// sendAs is a Fragment API write from a page, with the browser's
// cookies and the given request headers.
func sendAs(t *testing.T, c *http.Client, base, method, path string, body any, header ...string) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, base+path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestFragmentDirect: the page Web A's "Learn more" opens in a new tab.
// Signed in, it is that account's Fragment; signed out, the item's public
// page; writes need the visitor's own session from this site.
func TestFragmentDirect(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	botSvc := bots.New(s.db.Pool)
	frag := fragment.New(s.db.Pool, s.secret, nil)
	(&web.Fragment{Auth: &web.Auth{Accounts: s.accounts, Bots: botSvc}, Fragment: frag}).Register(s.mux)
	dir := os.Getenv("GREENGRAM_TEST_MINIAPPS_DIR")
	if dir != "" {
		(&miniapps.Server{Dir: dir, Bots: botSvc, Accounts: s.accounts, Media: s.media, Fragment: frag}).Register(s.mux)
	}
	base := s.http.URL

	ada := s.browser(t)
	adaID := int64(ada.must("/api/register", map[string]string{"username": "ada", "name": "Opua YT", "password": "correct horse 9"})["id"].(float64))
	bob := s.browser(t)
	bob.must("/api/register", map[string]string{"username": "bob", "name": "Bob", "password": "correct horse 9"})
	if _, err := s.db.Pool.Exec(ctx, `INSERT INTO fragment_items(id, kind, title, units, owner_id, listed, purchased_at) VALUES
		('username:happy', 'username', '@happy', 67000000000, $1, false, 1762128000),
		('username:shiny', 'username', '@shiny', 5000000000, NULL, true, NULL),
		('number:88800001111', 'number', '+88800001111', 9000000000, $1, false, 1762128000)`, adaID); err != nil {
		t.Fatal(err)
	}
	same := []string{"Origin", base, "Sec-Fetch-Site", "same-origin"}

	// writes: no session, someone else's site, a made-up session, another person's item
	if code, _ := sendAs(t, http.DefaultClient, base, "POST", "/api/fragment/quote", map[string]any{"itemId": "username:shiny"}, same...); code != 401 {
		t.Fatalf("quote without a session: %d", code)
	}
	if code, _ := sendAs(t, bob.c, base, "POST", "/api/fragment/quote", map[string]any{"itemId": "username:shiny"}, "Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site"); code != 403 {
		t.Fatalf("cross-site quote: %d", code)
	}
	if code, _ := sendAs(t, bob.c, base, "POST", "/api/fragment/quote", map[string]any{"itemId": "username:shiny"}, "Origin", "https://evil.example"); code != 403 {
		t.Fatalf("quote from another origin: %d", code)
	}
	if code, _ := sendAs(t, bob.c, base, "DELETE", "/api/fragment/listing", map[string]any{"itemId": "username:shiny"}, "Sec-Fetch-Site", "same-site"); code != 403 {
		t.Fatalf("same-site delete: %d", code)
	}
	forged := &http.Client{}
	req, _ := http.NewRequest("POST", base+"/api/fragment/listing", strings.NewReader(`{"kind":"username","value":"@happy","amount":"1"}`))
	req.Header.Set("Origin", base)
	req.AddCookie(&http.Cookie{Name: "session", Value: strings.Repeat("a", 64)})
	resp, err := forged.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("made-up session: %d", resp.StatusCode)
	}
	if code, out := sendAs(t, bob.c, base, "POST", "/api/fragment/listing", map[string]any{"kind": "username", "value": "@happy", "amount": "1"}, same...); code != 403 {
		t.Fatalf("bob lists ada's username: %d %v", code, out)
	}
	if code, out := sendAs(t, bob.c, base, "POST", "/api/fragment/assign", map[string]any{"itemId": "number:88800001111", "password": "correct horse 9"}, same...); code < 400 {
		t.Fatalf("bob takes ada's number: %d %v", code, out)
	}
	// the right page with the right session gets past the door to the shop's own rules
	if code, out := sendAs(t, bob.c, base, "POST", "/api/fragment/quote", map[string]any{"itemId": "username:shiny"}, same...); code != 400 || out["error"] != "You do not have enough GRAM in Wallet." {
		t.Fatalf("same-origin quote: %d %v", code, out)
	}
	if owner, listed := itemState(t, s, "username:happy"); owner != adaID || listed {
		t.Fatalf("happy changed: %d %v", owner, listed)
	}

	if dir == "" {
		t.Skip("GREENGRAM_TEST_MINIAPPS_DIR is not set: pages not checked")
	}
	// signed out: the public page, never "Session expired"
	code, out := pageAs(t, http.DefaultClient, base, "/fragment/username/happy")
	markup, _ := out["markup"].(string)
	cfg, _ := out["config"].(map[string]any)
	if code != 200 || cfg["public"] != true || out["extra"] != nil {
		t.Fatalf("public page: %d %v", code, out)
	}
	for _, want := range []string{"@happy", "Opua YT", "Sold", "67", "Nov 3, 2025", `href="/login?next=%2Ffragment%2Fusername%2Fhappy"`} {
		if !strings.Contains(markup, want) {
			t.Fatalf("public page lacks %q: %s", want, markup)
		}
	}
	if strings.Contains(markup, "fragment-detail-action") || strings.Contains(markup, "fragment-balance") {
		t.Fatalf("public page has controls: %s", markup)
	}
	if code, out := pageAs(t, http.DefaultClient, base, "/fragment/number/88800001111?account=2"); code != 200 || strings.Contains(out["markup"].(string), "Opua YT") || !strings.Contains(out["markup"].(string), "account=2") {
		t.Fatalf("public number page: %d %v", code, out)
	}
	if code, out := pageAs(t, http.DefaultClient, base, "/fragment"); code != 200 || !strings.Contains(out["markup"].(string), "/login?next=%2Ffragment") {
		t.Fatalf("public list page: %d %v", code, out)
	}
	if code, _ := pageAs(t, http.DefaultClient, base, "/fragment/username/nosuch"); code != 404 {
		t.Fatalf("public unknown item: %d", code)
	}

	// signed in: the same page as in the app, for that account
	code, out = pageAs(t, bob.c, base, "/fragment/username/shiny")
	if code != 200 || !strings.Contains(out["markup"].(string), "Buy now") || !strings.Contains(out["extra"].(string), "fragment-confirm") {
		t.Fatalf("signed-in page: %d %v", code, out)
	}
	if c := out["config"].(map[string]any); c["public"] != nil || c["slot"] != float64(1) {
		t.Fatalf("signed-in config: %v", c)
	}
	code, out = pageAs(t, ada.c, base, "/fragment/username/happy")
	if code != 200 || !strings.Contains(out["markup"].(string), "Sell") {
		t.Fatalf("owner's page: %d %v", code, out)
	}
	// a cookie for slot 1 doesn't open slot 2
	if code, out := pageAs(t, bob.c, base, "/fragment/username/shiny?account=2"); code != 200 || out["config"].(map[string]any)["public"] != true {
		t.Fatalf("empty slot: %d %v", code, out)
	}
}

func itemState(t *testing.T, s *stack, id string) (owner int64, listed bool) {
	t.Helper()
	if err := s.db.Pool.QueryRow(context.Background(), "SELECT coalesce(owner_id, 0), listed FROM fragment_items WHERE id=$1", id).Scan(&owner, &listed); err != nil {
		t.Fatal(err)
	}
	return
}
