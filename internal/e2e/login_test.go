// Package e2e runs the whole server (Postgres, HTTP sign-in, MTProto) the
// way the apps use it.
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/session"
	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/telegram/dcs"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/transport"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/api"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/erase"
	"github.com/Greengrame/greengram-server/internal/legacy"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/messages"
	"github.com/Greengrame/greengram-server/internal/reports"
	"github.com/Greengrame/greengram-server/internal/secret"
	"github.com/Greengrame/greengram-server/internal/settings"
	"github.com/Greengrame/greengram-server/internal/stars"
	"github.com/Greengrame/greengram-server/internal/store"
	"github.com/Greengrame/greengram-server/internal/stories"
	"github.com/Greengrame/greengram-server/internal/wallet"
	"github.com/Greengrame/greengram-server/internal/web"
	"github.com/Greengrame/greengram-server/internal/webpage"
)

const dc = 2

type stack struct {
	key   *rsa.PrivateKey
	tcp   int
	http  *httptest.Server
	media *media.Service

	// for tests that wire more of the server than sign-in
	db       *store.Store
	accounts *account.Service
	settings *settings.Source
	mux      *http.ServeMux
	api      *api.API
	stars    *stars.Service
	secret   *secret.Key
	srv      *edge.Server
	reports  *reports.Service
	erase    *erase.Service
}

func db(t *testing.T) *store.Store {
	t.Helper()
	return dbWith(t, "")
}

// dbWith is db with more database URL parameters ("a=1&b=2").
func dbWith(t *testing.T, params string) *store.Store {
	t.Helper()
	url := os.Getenv("GREENGRAM_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("GREENGRAM_TEST_DATABASE_URL not set in CI")
		}
		t.Skip("GREENGRAM_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("e2e_%d", time.Now().UnixNano())
	if _, err := admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	dsn := url + sep + "search_path=" + schema
	if params != "" {
		dsn += "&" + params
	}
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func start(t *testing.T) *stack {
	t.Helper()
	inst := make([]byte, 32)
	rand.Read(inst)
	return startOn(t, db(t), inst)
}

// startOn runs the server on st with the given instance secret.
func startOn(t *testing.T, st *store.Store, inst []byte) *stack {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	salt := make([]byte, 32)
	rand.Read(salt)
	set := settings.New(st.Pool)
	accounts := account.New(st.Pool, secret.FromBytes(inst), set)
	srv := edge.New(edge.Config{RSA: k, Secret: salt, Keys: st, ExchangeDC: dc})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	a := api.Register(srv, api.Instance{DC: dc, Host: "127.0.0.1", Port: port, BaseURL: "http://greengram.test"}, accounts, messages.New(st.Pool), channels.New(st.Pool))
	blobs, err := testBlobs(t)
	if err != nil {
		t.Fatal(err)
	}
	med := media.New(st.Pool, media.Config{Blobs: blobs, Parts: t.TempDir(), DC: dc})
	a.RegisterMedia(med)
	// tests serve their pages from loopback
	a.RegisterPreviews(webpage.New(st.Pool, webpage.NewFetcherFor(func(netip.Addr) bool { return true }), a.PreviewPhoto))
	ss := stars.New(st.Pool, set)
	a.RegisterStars(ss)
	a.RegisterTopup(wallet.New(st.Pool, set, nil, a.Notify).WithSigningKey(secret.FromBytes(inst)))
	a.RegisterStories(stories.New(st.Pool))
	rep := reports.New(st.Pool)
	a.RegisterReports(rep)
	eraser := erase.New(st.Pool, set)
	a.RegisterErase(eraser)
	botSvc := bots.New(st.Pool)
	botSvc.TokenKey = secret.FromBytes(inst)
	botAPI, err := a.Bots(ctx, botSvc, "Greengram")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ctx, ln)
	go a.Tick(ctx)

	mux := http.NewServeMux()
	auth := &web.Auth{Accounts: accounts}
	auth.Register(mux)
	auth.RegisterLinks(mux, legacy.New(st.Pool))
	(&web.Broadcasts{Accounts: accounts, Bots: botSvc, Sender: a}).Register(mux)
	hs := httptest.NewServer(botAPI.Wrap(mux))
	t.Cleanup(hs.Close)
	return &stack{srv: srv, key: k, tcp: port, http: hs, media: med, db: st, accounts: accounts, settings: set, mux: mux, api: a, stars: ss, secret: secret.FromBytes(inst), reports: rep, erase: eraser}
}

// browser is one cookie jar talking to the HTTP API.
type browser struct {
	t    *testing.T
	base string
	c    *http.Client
}

func (s *stack) browser(t *testing.T) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, base: s.http.URL, c: &http.Client{Jar: jar}}
}

func (b *browser) post(path string, body any) (int, map[string]any) {
	b.t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := b.c.Post(b.base+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (b *browser) must(path string, body any) map[string]any {
	b.t.Helper()
	code, out := b.post(path, body)
	if code != 200 {
		b.t.Fatalf("%s: %d %v", path, code, out)
	}
	return out
}

func (b *browser) session() string {
	for _, c := range b.c.Jar.Cookies(mustURL(b.base)) {
		if c.Name == "session" {
			return c.Value
		}
	}
	return ""
}

func (s *stack) client(storage telegram.SessionStorage) *telegram.Client {
	return telegram.NewClient(1, "test", telegram.Options{
		PublicKeys:     []telegram.PublicKey{{RSA: &s.key.PublicKey}},
		DC:             dc,
		DCList:         dcs.List{Options: []tg.DCOption{{ID: dc, IPAddress: "127.0.0.1", Port: s.tcp}}},
		Resolver:       dcs.Plain(dcs.PlainOptions{Protocol: transport.Intermediate}),
		NoUpdates:      true,
		SessionStorage: storage,
		Device:         telegram.DeviceConfig{DeviceModel: "e2e phone", AppVersion: "9.9"},
	})
}

func run(t *testing.T, c *telegram.Client, f func(context.Context, *tg.Client) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := c.Run(ctx, func(ctx context.Context) error { return f(ctx, c.API()) }); err != nil {
		t.Fatal(err)
	}
}

func TestSignUpThenAppSignIn(t *testing.T) {
	s := start(t)
	b := s.browser(t)

	// phone sign-up, like the website does it
	b.must("/api/auth/start", map[string]string{"mode": "phone"})
	b.must("/api/auth/intent", map[string]string{"intent": "register"})
	if out := b.must("/api/auth/code", map[string]string{"phone": "+1 555 0100"}); out["state"] != "registration" {
		t.Fatalf("code: %v", out)
	}
	out := b.must("/api/auth/register", map[string]string{"firstName": "Ada", "lastName": "Lovelace", "username": "ada", "password": "correct horse 1"})
	user := out["user"].(map[string]any)
	if out["state"] != "ready" || user["role"] != "admin" || user["phoneNumber"] != "+15550100" {
		t.Fatalf("first account should be admin with its phone: %v", out)
	}
	uid := int64(user["id"].(float64))

	// second account is a member; the username is taken
	b2 := s.browser(t)
	if code, _ := b2.post("/api/register", map[string]string{"username": "ADA", "name": "x", "password": "correct horse 2"}); code != 409 {
		t.Fatalf("duplicate username: %d", code)
	}
	if u := b2.must("/api/register", map[string]string{"username": "bob", "name": "Bob", "password": "correct horse 2"}); u["role"] != "member" {
		t.Fatalf("second account: %v", u)
	}

	// the Android app's phone sign-in: start, code, password
	app := s.browser(t)
	app.must("/api/auth/start", map[string]string{"mode": "phone"})
	if out := app.must("/api/auth/code", map[string]string{"phone": "+15550100"}); out["state"] != "password" {
		t.Fatalf("login code: %v", out)
	}
	if code, _ := app.post("/api/auth/password", map[string]string{"password": "wrong password!"}); code != 401 {
		t.Fatalf("wrong password: %d", code)
	}
	app.must("/api/auth/password", map[string]string{"password": "correct horse 1"})
	token := app.session()
	if len(token) != 64 {
		t.Fatalf("session cookie %q", token)
	}

	// MTProto: hand the cookie over, then use the account
	storage := &session.StorageMemory{}
	run(t, s.client(storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}}); !tgerr.Is(err, "AUTH_KEY_UNREGISTERED") {
			return fmt.Errorf("before sign-in want AUTH_KEY_UNREGISTERED, got %v", err)
		}
		if _, err := c.AuthImportAuthorization(ctx, &tg.AuthImportAuthorizationRequest{ID: uid + 1, Bytes: []byte(token)}); !tgerr.Is(err, "USER_ID_INVALID") {
			return fmt.Errorf("wrong user id: %v", err)
		}
		auth, err := c.AuthImportAuthorization(ctx, &tg.AuthImportAuthorizationRequest{ID: uid, Bytes: []byte(token)})
		if err != nil {
			return err
		}
		me := auth.(*tg.AuthAuthorization).User.(*tg.User)
		if me.ID != uid || !me.Self || me.Username != "ada" || me.FirstName != "Ada" || me.LastName != "Lovelace" || me.Phone != "15550100" {
			return fmt.Errorf("authorization user %+v", me)
		}
		full, err := c.UsersGetFullUser(ctx, &tg.InputUserSelf{})
		if err != nil || full.FullUser.ID != uid {
			return fmt.Errorf("getFullUser: %v %+v", err, full)
		}
		auths, err := c.AccountGetAuthorizations(ctx)
		if err != nil {
			return err
		}
		var current *tg.Authorization
		for i := range auths.Authorizations {
			if auths.Authorizations[i].Current {
				current = &auths.Authorizations[i]
			}
		}
		// the website session from sign-up, plus this app
		if len(auths.Authorizations) != 2 || current == nil || current.DeviceModel != "e2e phone" || current.Hash != 0 {
			return fmt.Errorf("authorizations %+v", auths.Authorizations)
		}
		// privacy: default for phone is contacts; set nobody and read it back
		p, err := c.AccountGetPrivacy(ctx, &tg.InputPrivacyKeyPhoneNumber{})
		if err != nil {
			return err
		}
		if _, ok := p.Rules[len(p.Rules)-1].(*tg.PrivacyValueAllowContacts); !ok {
			return fmt.Errorf("default phone privacy %+v", p.Rules)
		}
		if _, err := c.AccountSetPrivacy(ctx, &tg.AccountSetPrivacyRequest{Key: &tg.InputPrivacyKeyPhoneNumber{}, Rules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueDisallowAll{}}}); err != nil {
			return err
		}
		p, err = c.AccountGetPrivacy(ctx, &tg.InputPrivacyKeyPhoneNumber{})
		if err != nil {
			return err
		}
		if _, ok := p.Rules[len(p.Rules)-1].(*tg.PrivacyValueDisallowAll); !ok {
			return fmt.Errorf("phone privacy after set %+v", p.Rules)
		}
		if _, err := c.UpdatesGetState(ctx); err != nil {
			return err
		}
		return nil
	})

	// a reconnect with the same auth key is still signed in
	run(t, s.client(storage), func(ctx context.Context, c *tg.Client) error {
		users, err := c.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
		if err != nil || len(users) != 1 {
			return fmt.Errorf("after reconnect: %v %v", err, users)
		}
		if _, err := c.AuthLogOut(ctx); err != nil {
			return err
		}
		if _, err := c.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}}); !tgerr.Is(err, "AUTH_KEY_UNREGISTERED") {
			return fmt.Errorf("after logOut want AUTH_KEY_UNREGISTERED, got %v", err)
		}
		return nil
	})

	// logging out ended that session for HTTP too
	req, _ := http.NewRequest("GET", s.http.URL+"/api/me", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("/api/me after app logOut: %d", resp.StatusCode)
	}
}

func TestUsernameSignIn(t *testing.T) {
	s := start(t)
	b := s.browser(t)
	b.must("/api/register", map[string]string{"username": "old_timer", "name": "Old Timer", "password": "correct horse 3"})

	// one-step legacy login (the app uses it for username accounts)
	app := s.browser(t)
	if u := app.must("/api/login", map[string]string{"username": "@Old_Timer", "password": "correct horse 3"}); u["username"] != "old_timer" {
		t.Fatalf("legacy login: %v", u)
	}
	// flow-based username sign-in (the website)
	web := s.browser(t)
	web.must("/api/auth/start", map[string]string{"mode": "phone"})
	if out := web.must("/api/auth/username", map[string]string{"username": "old_timer", "password": "correct horse 3"}); out["state"] != "ready" {
		t.Fatalf("username flow: %v", out)
	}
	if code, _ := web.post("/api/auth/username", map[string]string{"username": "old_timer", "password": "correct horse 3"}); code != 410 {
		t.Fatalf("finished flow reused: %d", code)
	}
}

func mustURL(s string) *url.URL {
	u, _ := url.Parse(s)
	return u
}
