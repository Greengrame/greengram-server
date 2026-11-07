package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
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
	"github.com/Greengrame/greengram-server/internal/store"
	"github.com/Greengrame/greengram-server/internal/web"
)

// panel is the HTTP side only: sign-in, admin API and the static site.
type panel struct {
	db       *store.Store
	url      string
	chans    *channels.Service
	adminDir string
}

func startPanel(t *testing.T) *panel {
	t.Helper()
	st := db(t)
	inst := make([]byte, 32)
	rand.Read(inst)
	set := settings.New(st.Pool)
	accounts := account.New(st.Pool, secret.FromBytes(inst), set)
	chans := channels.New(st.Pool)

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "admin-assets"), 0o755)
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><div id=admin-app></div>"), 0o644)
	os.WriteFile(filepath.Join(dir, "admin-assets", "main.js"), []byte("console.log(1)"), 0o644)

	mux := http.NewServeMux()
	(&web.Auth{Accounts: accounts}).Register(mux)
	adm := admin.New(st.Pool, accounts, set, chans)
	adm.Register(mux)
	(&site.Site{AdminDir: dir, Admin: adm.Admin}).Register(mux)
	hs := httptest.NewServer(adm.Count(mux))
	t.Cleanup(hs.Close)
	return &panel{db: st, url: hs.URL, chans: chans, adminDir: dir}
}

type visitor struct {
	t    *testing.T
	base string
	c    *http.Client
}

func (p *panel) visit(t *testing.T) *visitor {
	jar, _ := cookiejar.New(nil)
	return &visitor{t: t, base: p.url, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (p *visitor) do(method, path string, body any) (int, map[string]any) {
	p.t.Helper()
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, p.base+path, r)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.c.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (p *visitor) ok(method, path string, body any) map[string]any {
	p.t.Helper()
	code, out := p.do(method, path, body)
	if code != 200 {
		p.t.Fatalf("%s %s: %d %v", method, path, code, out)
	}
	return out
}

func (p *visitor) want(code int, method, path string, body any) map[string]any {
	p.t.Helper()
	got, out := p.do(method, path, body)
	if got != code {
		p.t.Fatalf("%s %s: want %d, got %d %v", method, path, code, got, out)
	}
	return out
}

func userIn(t *testing.T, overview map[string]any, id float64) map[string]any {
	t.Helper()
	for _, u := range overview["users"].([]any) {
		if m := u.(map[string]any); m["id"] == id {
			return m
		}
	}
	t.Fatalf("user %v not in %v", id, overview["users"])
	return nil
}

func TestAdminPanel(t *testing.T) {
	p := startPanel(t)
	ada := p.visit(t)
	me := ada.ok("POST", "/api/register", map[string]string{"username": "ada", "name": "Ada", "password": "correct horse 1"})
	adaID := me["id"].(float64)
	bob := p.visit(t)
	bobID := bob.ok("POST", "/api/register", map[string]string{"username": "bob", "name": "Bob", "password": "correct horse 2"})["id"].(float64)

	// only a signed-in admin gets in
	p.visit(t).want(401, "GET", "/api/admin", nil)
	bob.want(403, "GET", "/api/admin", nil)
	bob.want(403, "GET", "/api/admin/sessions", nil)

	all := ada.ok("GET", "/api/admin", nil)
	if len(all["users"].([]any)) != 2 || all["settings"].(map[string]any)["siteName"] != "Greengram" {
		t.Fatalf("overview %v", all)
	}
	for _, key := range []string{"customCountries", "bots", "recent", "audit", "conversations", "activity", "starTransactions", "fragmentInventory"} {
		if _, ok := all[key].([]any); !ok {
			t.Fatalf("overview %s = %#v, the panel wants a list", key, all[key])
		}
	}
	if u := userIn(t, all, adaID); u["role"] != "admin" || u["sessionCount"] != 1.0 || u["accountState"] != "active" {
		t.Fatalf("ada in list %v", u)
	}

	// roles: promote, demote, and the instance keeps one admin
	ada.ok("PATCH", "/api/admin", map[string]any{"action": "role", "id": bobID, "role": "admin"})
	bob.ok("GET", "/api/admin", nil)
	ada.ok("PATCH", "/api/admin", map[string]any{"action": "role", "id": bobID, "role": "member"})
	bob.want(403, "GET", "/api/admin", nil)
	ada.want(400, "PATCH", "/api/admin", map[string]any{"action": "role", "id": adaID, "role": "member"})

	// premium for 30 days, then revoked
	out := ada.ok("PATCH", "/api/admin", map[string]any{"action": "premium", "id": bobID, "days": 30})
	if u := userIn(t, out, bobID); u["isPremium"] != true || u["premiumUnlimited"] != false {
		t.Fatalf("premium %v", u)
	}
	out = ada.ok("PATCH", "/api/admin", map[string]any{"action": "premium", "id": bobID, "unlimited": true})
	if u := userIn(t, out, bobID); u["premiumUnlimited"] != true {
		t.Fatalf("unlimited %v", u)
	}
	ada.want(400, "PATCH", "/api/admin", map[string]any{"action": "premium", "id": bobID, "days": 1, "startsAt": 5, "endsAt": 10})
	ada.want(501, "PATCH", "/api/admin", map[string]any{"action": "stars", "id": bobID, "amount": 5, "reference": "abcdefgh"})

	// disabling signs bob out everywhere
	ada.want(400, "PATCH", "/api/admin", map[string]any{"action": "disable", "id": adaID, "disabled": true})
	ada.ok("PATCH", "/api/admin", map[string]any{"action": "disable", "id": bobID, "disabled": true})
	bob.want(401, "GET", "/api/me", nil)
	p.visit(t).want(401, "POST", "/api/login", map[string]string{"username": "bob", "password": "correct horse 2"})
	ada.ok("PATCH", "/api/admin", map[string]any{"action": "disable", "id": bobID, "disabled": false})
	bob.ok("POST", "/api/login", map[string]string{"username": "bob", "password": "correct horse 2"})

	// profile edits and the detail view
	detail := ada.ok("PATCH", fmt.Sprintf("/api/admin/users/%v", bobID), map[string]any{"name": "Robert", "bio": "hi", "usernamePublic": false})
	if u := detail["user"].(map[string]any); u["name"] != "Robert" || u["usernamePublic"] != false || detail["ok"] != true {
		t.Fatalf("edit %v", detail)
	}
	ada.want(409, "PATCH", fmt.Sprintf("/api/admin/users/%v", bobID), map[string]any{"username": "ada"})
	// a Fragment listing holds its name; its owner may take it
	p.db.Pool.Exec(context.Background(), "INSERT INTO fragment_items(id, kind, title, units, owner_id, listed) VALUES ('username:prize', 'username', '@prize', 1, $1, false)", adaID)
	ada.want(409, "PATCH", fmt.Sprintf("/api/admin/users/%v", bobID), map[string]any{"username": "prize"})
	ada.ok("PATCH", fmt.Sprintf("/api/admin/users/%v", adaID), map[string]any{"username": "prize"})
	ada.ok("PATCH", fmt.Sprintf("/api/admin/users/%v", adaID), map[string]any{"username": "ada"})
	ada.want(400, "PATCH", fmt.Sprintf("/api/admin/users/%v", bobID), map[string]any{"role": "admin"})
	detail = ada.ok("GET", fmt.Sprintf("/api/admin/users/%v", bobID), nil)
	if s := detail["sessions"].([]any); len(s) != 1 || s[0].(map[string]any)["isCurrent"] != false {
		t.Fatalf("bob sessions %v", detail["sessions"])
	}

	// verification badges
	ada.ok("PATCH", "/api/admin/verification", map[string]any{"kind": "user", "id": bobID, "verified": true})
	if u := userIn(t, ada.ok("GET", "/api/admin", nil), bobID); u["isVerified"] != true {
		t.Fatalf("verified %v", u)
	}
	ada.want(404, "PATCH", "/api/admin/verification", map[string]any{"kind": "chat", "id": 99999, "verified": true})

	// sessions: list, the admin's own is protected, others can go
	sessions := ada.ok("GET", "/api/admin/sessions", nil)["sessions"].([]any)
	var mine, bobs string
	for _, s := range sessions {
		m := s.(map[string]any)
		if m["isCurrent"] == true {
			mine = m["id"].(string)
		}
		if m["userId"] == bobID {
			bobs = m["id"].(string)
		}
	}
	if mine == "" || bobs == "" {
		t.Fatalf("sessions %v", sessions)
	}
	ada.want(400, "DELETE", "/api/admin/sessions/"+mine, map[string]any{})
	ada.ok("DELETE", "/api/admin/sessions/"+bobs, map[string]any{})
	bob.want(401, "GET", "/api/me", nil)
	if r := ada.ok("DELETE", "/api/admin/sessions", map[string]any{"idleDays": 30}); r["revoked"] != 0.0 {
		t.Fatalf("idle prune %v", r)
	}

	// moderation states; deleting needs the username typed out
	bob.ok("POST", "/api/login", map[string]string{"username": "bob", "password": "correct horse 2"})
	u := ada.ok("PATCH", fmt.Sprintf("/api/admin/users/%v/access", bobID), map[string]any{"state": "frozen", "reason": "spam"})
	if u["accountState"] != "frozen" || u["disabled"] != false {
		t.Fatalf("frozen %v", u)
	}
	u = ada.ok("PATCH", fmt.Sprintf("/api/admin/users/%v/access", bobID), map[string]any{"state": "suspended"})
	if u["accountState"] != "suspended" || u["disabled"] != true {
		t.Fatalf("suspended %v", u)
	}
	bob.want(401, "GET", "/api/me", nil)
	ada.want(400, "PATCH", fmt.Sprintf("/api/admin/users/%v/access", bobID), map[string]any{"state": "deleted", "confirmation": "nope"})
	u = ada.ok("PATCH", fmt.Sprintf("/api/admin/users/%v/access", bobID), map[string]any{"state": "deleted", "confirmation": "bob"})
	if u["accountState"] != "deleted" || u["name"] != "Deleted Account" {
		t.Fatalf("deleted %v", u)
	}
	ada.want(409, "PATCH", fmt.Sprintf("/api/admin/users/%v/access", bobID), map[string]any{"state": "active"})
	ada.want(400, "PATCH", fmt.Sprintf("/api/admin/users/%v/access", adaID), map[string]any{"state": "frozen"})

	// settings go through the same checks as settings.js
	ada.want(400, "PATCH", "/api/admin/settings", map[string]any{"sessionDays": 0})
	ada.want(400, "PATCH", "/api/admin/settings", map[string]any{"nope": 1})
	if s := ada.ok("PATCH", "/api/admin/settings", map[string]any{"siteName": "  Moss ", "registrationEnabled": false, "groupMemberLimit": 50}); s["siteName"] != "Moss" || s["groupMemberLimit"] != 50.0 {
		t.Fatalf("settings %v", s)
	}
	if b := p.visit(t).ok("GET", "/api/branding", nil); b["name"] != "Moss" || b["hasCustomLogo"] != false {
		t.Fatalf("branding %v", b)
	}
	// registration closed is enforced by sign-up itself
	p.visit(t).want(403, "POST", "/api/register", map[string]string{"username": "carol", "name": "Carol", "password": "correct horse 3"})

	// custom countries show up in the public list
	ada.ok("POST", "/api/admin/countries", map[string]any{"name": "Moss Island", "countryCode": "+888", "iso2": "", "enabled": true})
	ada.want(400, "POST", "/api/admin/countries", map[string]any{"name": "x", "countryCode": "0", "enabled": true})
	custom := ada.ok("GET", "/api/admin", nil)["customCountries"].([]any)
	if len(custom) != 1 || custom[0].(map[string]any)["country_code"] != "888" {
		t.Fatalf("countries %v", custom)
	}
	found := false
	for _, c := range p.visit(t).ok("GET", "/api/countries", nil)["phoneCodes"].([]any) {
		if c.(map[string]any)["name"] == "Moss Island" {
			found = true
		}
	}
	if !found {
		t.Fatal("custom country not in /api/countries")
	}
	ada.ok("DELETE", "/api/admin/countries", map[string]any{"id": custom[0].(map[string]any)["id"]})

	// conversations can be removed by an admin
	ch, _, err := p.chans.Create(t.Context(), int64(adaID), "Lobby", "", true)
	if err != nil {
		t.Fatal(err)
	}
	conv := ada.ok("GET", "/api/admin", nil)["conversations"].([]any)
	if len(conv) != 1 || conv[0].(map[string]any)["kind"] != "group" || conv[0].(map[string]any)["member_count"] != 1.0 {
		t.Fatalf("conversations %v", conv)
	}
	ada.ok("PATCH", "/api/admin/verification", map[string]any{"kind": "chat", "id": ch.ID, "verified": true})
	ada.ok("DELETE", fmt.Sprintf("/api/admin/conversations/%d", ch.ID), map[string]any{})
	if conv := ada.ok("GET", "/api/admin", nil)["conversations"].([]any); len(conv) != 0 {
		t.Fatalf("after delete %v", conv)
	}

	// a logo upload replaces the default and is served back
	var img bytes.Buffer
	png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 4, 3)))
	req, _ := http.NewRequest("POST", p.url+"/api/admin/branding/logo", bytes.NewReader(img.Bytes()))
	req.Header.Set("Content-Type", "image/png")
	resp, err := ada.c.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("logo upload %v %v", err, resp.Status)
	}
	resp.Body.Close()
	resp, err = http.Get(p.url + "/branding/logo")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("Content-Type") != "image/png" || !bytes.Equal(got, img.Bytes()) {
		t.Fatalf("logo served %q %d bytes", resp.Header.Get("Content-Type"), len(got))
	}
	req, _ = http.NewRequest("POST", p.url+"/api/admin/branding/logo", strings.NewReader("<svg onload=alert(1)>"))
	if resp, _ := ada.c.Do(req); resp.StatusCode != 400 {
		t.Fatalf("svg logo accepted: %d", resp.StatusCode)
	}
	ada.ok("DELETE", "/api/admin/branding/logo", map[string]any{})

	// everything above left a trail
	log := ada.ok("GET", "/api/admin", nil)["audit"].([]any)
	actions := map[string]bool{}
	for _, e := range log {
		m := e.(map[string]any)
		actions[m["action"].(string)] = true
		if m["actor"] != "Ada" {
			t.Fatalf("audit actor %v", m)
		}
		var details any
		if json.Unmarshal([]byte(m["details"].(string)), &details) != nil {
			t.Fatalf("audit details not JSON text: %v", m["details"])
		}
	}
	for _, a := range []string{"role", "premium", "disable", "admin_profile_updated", "peer_verification_updated", "admin_sessions_revoked",
		"account_frozen", "account_deleted", "update_settings", "update_country", "delete_country", "delete_conversation", "instance_logo"} {
		if !actions[a] {
			t.Errorf("no audit entry for %s", a)
		}
	}

	h := ada.ok("GET", "/api/admin/health", nil)
	if h["tables"].(map[string]any)["users"] != 2.0 || h["requests"].(map[string]any)["total"].(float64) < 10 {
		t.Fatalf("health %v", h)
	}
	s := ada.ok("GET", "/api/admin/stats?days=7", nil)
	if len(s["series"].([]any)) != 7 || len(s["hours"].([]any)) != 24 {
		t.Fatalf("stats %v", s)
	}
	p.visit(t).want(404, "GET", "/api/nothing-here", nil)
}

func TestAdminPageAndWebToken(t *testing.T) {
	p := startPanel(t)
	ada := p.visit(t)
	ada.ok("POST", "/api/register", map[string]string{"username": "ada", "name": "Ada", "password": "correct horse 1"})
	bob := p.visit(t)
	bob.ok("POST", "/api/register", map[string]string{"username": "bob", "name": "Bob", "password": "correct horse 2"})

	get := func(c *visitor, path string) *http.Response {
		resp, err := c.c.Get(p.url + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if r := get(p.visit(t), "/admin"); r.StatusCode != 303 || r.Header.Get("Location") != "/login?next=admin" {
		t.Fatalf("signed out /admin: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r := get(bob, "/admin"); r.StatusCode != 403 {
		t.Fatalf("member /admin: %d", r.StatusCode)
	}
	if r := get(ada, "/admin"); r.StatusCode != 200 || r.Header.Get("Content-Security-Policy") == "" {
		t.Fatalf("admin /admin: %d", r.StatusCode)
	}
	if r := get(p.visit(t), "/admin-assets/main.js"); r.StatusCode != 200 {
		t.Fatalf("admin assets: %d", r.StatusCode)
	}
	if r := get(p.visit(t), "/login"); r.StatusCode != 200 {
		t.Fatalf("/login: %d", r.StatusCode)
	}
	if r := get(p.visit(t), "/__greengram/login/login.js"); r.StatusCode != 200 {
		t.Fatalf("login.js: %d", r.StatusCode)
	}

	// Web A gets the cookie session back to hand to auth.importAuthorization
	p.visit(t).want(401, "POST", "/api/auth/webtoken", map[string]any{})
	out := ada.ok("POST", "/api/auth/webtoken", map[string]any{})
	var cookie string
	for _, c := range ada.c.Jar.Cookies(mustURL(p.url)) {
		if c.Name == "session" {
			cookie = c.Value
		}
	}
	if out["token"] != cookie || len(cookie) != 64 || out["userId"] != 1.0 {
		t.Fatalf("webtoken %v (cookie %q)", out, cookie)
	}

	// Add Account: slot 2 has nothing until it signs in, then its own user,
	// and logging one out leaves the other alone
	ada.want(401, "POST", "/api/auth/webtoken?account=2", map[string]any{})
	ada.ok("POST", "/api/login?account=2", map[string]string{"username": "bob", "password": "correct horse 2"})
	if out := ada.ok("POST", "/api/auth/webtoken?account=2", map[string]any{}); out["userId"] != 2.0 || out["token"] == cookie {
		t.Fatalf("slot 2 webtoken %v", out)
	}
	if out := ada.ok("POST", "/api/auth/webtoken", map[string]any{}); out["userId"] != 1.0 {
		t.Fatalf("slot 1 webtoken after slot 2 signed in %v", out)
	}
	// the header and ?account= name the same slot; Wallet purchases rely on it
	req, _ := http.NewRequest("POST", p.url+"/api/auth/webtoken", strings.NewReader("{}"))
	req.Header.Set("X-Greengram-Account", "2")
	resp, err := ada.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var byHeader map[string]any
	json.NewDecoder(resp.Body).Decode(&byHeader)
	resp.Body.Close()
	if byHeader["userId"] != 2.0 {
		t.Fatalf("slot 2 by header %v", byHeader)
	}
	ada.ok("POST", "/api/logout?account=2", map[string]any{})
	ada.want(401, "POST", "/api/auth/webtoken?account=2", map[string]any{})
	ada.ok("POST", "/api/auth/webtoken", map[string]any{})
}
