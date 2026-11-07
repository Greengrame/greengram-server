package e2e

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/builtin"
	"github.com/Greengrame/greengram-server/internal/fragment"
	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/miniapps"
	"github.com/Greengrame/greengram-server/internal/secret"
	"github.com/Greengrame/greengram-server/internal/wallet"
	"github.com/Greengrame/greengram-server/internal/web"
)

// launcher signs initData the way messages.requestWebView does.
type launcher struct {
	t    *testing.T
	bots *bots.Service
}

func (l launcher) launch(kind string, user int64, session string) string {
	l.t.Helper()
	b, ok, err := l.bots.ByKind(context.Background(), kind)
	if err != nil || !ok {
		l.t.Fatalf("bot %s: %v", kind, err)
	}
	who, _ := json.Marshal(map[string]any{"id": ids.Client(user), "first_name": "Test"})
	f := url.Values{"user": {string(who)}}
	if session != "" {
		f.Set("gg_session", bots.SessionTag(b.WebAppKey, account.Hash(session)))
	}
	return bots.InitData(b.WebAppKey, f, time.Now())
}

// app calls a mini app endpoint with launch data and no cookies.
func app(t *testing.T, base, method, path, init string, body any, header ...string) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if b, ok := body.([]byte); ok {
		r = bytes.NewReader(b)
	} else if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, base+path, r)
	if init != "" {
		req.Header.Set("X-Telegram-Init-Data", init)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestMiniAppLaunch: the built-in apps' APIs take the signed launch data
// instead of a cookie, from the right bot only, and act for the session
// that opened them.
func TestMiniAppLaunch(t *testing.T) {
	s := start(t)
	botSvc := bots.New(s.db.Pool)
	auth := &web.Auth{Accounts: s.accounts, Bots: botSvc}
	w := wallet.New(s.db.Pool, s.settings, fixedMarket{map[string]string{"TON": "5", "USDT": "1"}}, s.api.Notify).WithCharts(noCharts{})
	(&web.Wallet{Auth: auth, Wallet: w}).Register(s.mux)
	l := launcher{t, botSvc}

	ada := s.browser(t)
	adaID := int64(ada.must("/api/register", map[string]string{"username": "ada", "name": "Ada", "password": "correct horse 9"})["id"].(float64))
	bob := s.browser(t)
	bobID := int64(bob.must("/api/register", map[string]string{"username": "bob", "name": "Bob", "password": "correct horse 9"})["id"].(float64))
	base := s.http.URL

	if code, out := app(t, base, "GET", "/api/walt/session", l.launch(bots.Wallet, adaID, ""), nil); code != 200 || int64(out["user_id"].(float64)) != adaID {
		t.Fatalf("wallet launch: %d %v", code, out)
	}
	if code, _ := app(t, base, "GET", "/api/walt/session", l.launch(bots.Stickers, adaID, ""), nil); code != 401 {
		t.Fatalf("another bot's launch: %d", code)
	}
	forged := strings.Replace(l.launch(bots.Wallet, adaID, ""), url.QueryEscape(fmt.Sprint(ids.Client(adaID))), url.QueryEscape(fmt.Sprint(ids.Client(bobID))), 1)
	if code, _ := app(t, base, "GET", "/api/walt/session", forged, nil); code != 401 {
		t.Fatalf("edited launch: %d", code)
	}
	if code, _ := app(t, base, "GET", "/api/walt/session", "", nil); code != 401 {
		t.Fatalf("no launch, no cookie: %d", code)
	}

	// a passcode unlock from the app counts for the session that opened it
	launch := l.launch(bots.Wallet, adaID, ada.session())
	if code, out := app(t, base, "POST", "/api/walt/local/passcode/create", launch, map[string]any{"passcode": "123456", "confirmation": "123456"}); code != 200 || out["unlocked"] != true {
		t.Fatalf("passcode via launch: %d %v", code, out)
	}
	if st := ada.mustGet("/api/walt/local/passcode/status"); st["unlocked"] != true {
		t.Fatalf("cookie session not unlocked: %v", st)
	}
	ada.must("/api/logout", map[string]any{})
	if code, _ := app(t, base, "GET", "/api/walt/session", launch, nil); code != 401 {
		t.Fatalf("launch from an ended session: %d", code)
	}
	_ = bob
}

// TestStickersApp: uploads, packs and pages of the Stickers mini app.
func TestStickersApp(t *testing.T) {
	s := start(t)
	botSvc := bots.New(s.db.Pool)
	(&miniapps.Server{Bots: botSvc, Accounts: s.accounts, Media: s.media}).Register(s.mux)
	l := launcher{t, botSvc}
	base := s.http.URL
	ada := s.browser(t)
	adaID := int64(ada.must("/api/register", map[string]string{"username": "ada", "name": "Ada", "password": "correct horse 9"})["id"].(float64))
	bob := s.browser(t)
	bobID := int64(bob.must("/api/register", map[string]string{"username": "bob", "name": "Bob", "password": "correct horse 9"})["id"].(float64))
	adaApp, bobApp := l.launch(bots.Stickers, adaID, ""), l.launch(bots.Stickers, bobID, "")

	if code, _ := app(t, base, "POST", "/__miniapps/stickers/upload?kind=sticker", "", testPNG(64, 64)); code != 401 {
		t.Fatalf("upload without a launch: %d", code)
	}
	if code, out := app(t, base, "POST", "/__miniapps/stickers/upload?kind=sticker", adaApp, []byte("not an image")); code != 400 {
		t.Fatalf("junk upload: %d %v", code, out)
	}
	if code, out := app(t, base, "POST", "/__miniapps/stickers/upload?kind=emoji", adaApp, testPNG(200, 200)); code != 400 {
		t.Fatalf("200px emoji: %d %v", code, out)
	}
	var docs []string
	for range 2 {
		code, out := app(t, base, "POST", "/__miniapps/stickers/upload?kind=sticker", adaApp, testPNG(128, 128))
		if code != 200 || out["doc"] == nil {
			t.Fatalf("upload: %d %v", code, out)
		}
		docs = append(docs, out["doc"].(string))
	}
	rpc := func(init string, body map[string]any) (int, map[string]any) {
		return app(t, base, "POST", "/__miniapps/stickers/api", init, body)
	}
	if code, out := rpc(adaApp, map[string]any{"method": "checkShortName", "value": "ada_pack"}); code != 200 || out["ok"] != true {
		t.Fatalf("free name: %d %v", code, out)
	}
	// bob can't put ada's uploads in his pack
	if code, _ := rpc(bobApp, map[string]any{"method": "createStickerSet", "title": "Stolen", "stickers": []any{map[string]any{"doc_id": docs[0], "emoji": "😀"}}}); code != 403 {
		t.Fatalf("someone else's upload: %d", code)
	}
	code, out := rpc(adaApp, map[string]any{"method": "createStickerSet", "title": "Ada Pack", "short_name": "ada_pack",
		"stickers": []any{map[string]any{"doc_id": docs[0], "emoji": "😀"}, map[string]any{"doc_id": docs[1], "emoji": "🔥"}}})
	if code != 200 || out["ok"] != true {
		t.Fatalf("create: %d %v", code, out)
	}
	pack := out["pack_id"].(string)
	if code, out := rpc(adaApp, map[string]any{"method": "checkShortName", "value": "ADA_PACK"}); code != 200 || out["ok"] != false {
		t.Fatalf("taken name: %d %v", code, out)
	}

	// the pack page, for the owner and for someone else
	page := func(init, path string) (int, map[string]any) {
		return app(t, base, "GET", path, init, nil, "X-Greengram-Page", "1")
	}
	code, out = page(adaApp, "/stickers/pack/"+pack)
	if code != 200 || !strings.Contains(out["markup"].(string), "Ada Pack") || !strings.Contains(out["markup"].(string), "js-delete-pack") {
		t.Fatalf("pack page: %d %v", code, out)
	}
	media := regexp.MustCompile(`/__miniapps/stickers/media/\d+\?s=[0-9a-f]+`).FindString(out["markup"].(string))
	if resp, err := http.Get(base + media); err != nil || resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("signed preview: %v %v", resp, err)
	}
	// the signature may already end in 0
	bad := media[:len(media)-1] + "0"
	if bad == media {
		bad = media[:len(media)-1] + "1"
	}
	if resp, err := http.Get(base + bad); err != nil || resp.StatusCode != 404 {
		t.Fatalf("badly signed preview: %v %v", resp, err)
	}
	if code, out := page(bobApp, "/stickers/pack/ada_pack"); code != 200 || strings.Contains(out["markup"].(string), "js-delete-pack") {
		t.Fatalf("pack page for others: %d %v", code, out)
	}
	if code, _ := page(bobApp, "/stickers/editpack/"+pack); code != 403 {
		t.Fatalf("edit page for others: %d", code)
	}
	if code, out := page(adaApp, "/stickers"); code != 200 || !strings.Contains(out["markup"].(string), "/addstickers/ada_pack") {
		t.Fatalf("list: %d %v", code, out)
	}
	if code, _ := page("", "/stickers"); code != 401 {
		t.Fatalf("page without a launch: %d", code)
	}

	// edits: reorder, drop one; only the owner
	if code, _ := rpc(bobApp, map[string]any{"method": "updateStickerSet", "pack_id": pack, "stickers": []any{map[string]any{"sticker_raw": docs[1], "emoji": "🔥"}}}); code != 403 {
		t.Fatalf("someone else's edit: %d", code)
	}
	if code, out := rpc(adaApp, map[string]any{"method": "updateStickerSet", "pack_id": pack, "stickers": []any{map[string]any{"sticker_raw": docs[1], "emoji": "🔥"}}}); code != 200 {
		t.Fatalf("update: %d %v", code, out)
	}
	if code, out := page(adaApp, "/stickers/pack/"+pack); code != 200 || strings.Contains(out["markup"].(string), docs[0]) || strings.Count(out["markup"].(string), "tm-grid-item-sticker") != 1 {
		t.Fatalf("after update: %d %v", code, out)
	}
	if code, _ := rpc(bobApp, map[string]any{"method": "deleteStickerSet", "pack_id": pack}); code != 403 {
		t.Fatalf("someone else's delete: %d", code)
	}
	// what's on the statistics pages, before anything was sent
	if code, out := page(adaApp, "/stickers/stats/"+pack); code != 200 || !strings.Contains(out["markup"].(string), "chart_stickerset_use_wrap") {
		t.Fatalf("stats: %d %v", code, out)
	}
	if code, _ := page(adaApp, "/stickers/stats/"+pack+"/1"); code != 404 {
		t.Fatalf("stats of a sticker not in the pack: %d", code)
	}
	if code, out := page(adaApp, "/stickers/stats/"+pack+"/"+docs[1]); code != 200 || !strings.Contains(out["markup"].(string), "chart_sticker_send_wrap") {
		t.Fatalf("sticker stats: %d %v", code, out)
	}
	// a published pack has nothing to publish: the page sends you to the list
	if code, out := page(adaApp, "/stickers/publishpack/"+pack); code != 200 || out["state"].(map[string]any)["redirect"] != "/stickers" {
		t.Fatalf("publish page of a public pack: %d %v", code, out)
	}
	if code, _ := rpc(adaApp, map[string]any{"method": "publishStickerSet", "pack_id": pack, "title": "Ada Pack"}); code != 400 {
		t.Fatalf("publishing a public pack: %d", code)
	}

	// without a name it's a draft: only its maker can open it until it's published
	code, out = rpc(adaApp, map[string]any{"method": "createStickerSet", "title": "Draft", "stickers": []any{map[string]any{"doc_id": docs[0], "emoji": "😀"}}})
	if code != 200 || !strings.Contains(fmt.Sprint(out["redirect"]), "/stickers/publishpack/") {
		t.Fatalf("draft: %d %v", code, out)
	}
	draft := out["pack_id"].(string)
	if code, _ := page(bobApp, "/stickers/pack/"+draft); code != 404 {
		t.Fatalf("someone else's draft: %d", code)
	}
	if code, out := page(adaApp, "/stickers/publishpack/"+draft); code != 200 || !strings.Contains(out["markup"].(string), `name="short_name"`) {
		t.Fatalf("publish page: %d %v", code, out)
	}
	if code, _ := rpc(bobApp, map[string]any{"method": "publishStickerSet", "pack_id": draft, "title": "Mine", "short_name": "bobs_now"}); code != 403 {
		t.Fatalf("publishing someone else's draft: %d", code)
	}
	if code, _ := rpc(adaApp, map[string]any{"method": "publishStickerSet", "pack_id": draft, "title": "Draft", "short_name": "ada_pack"}); code != 409 {
		t.Fatalf("publishing under a taken name: %d", code)
	}
	if code, out := rpc(adaApp, map[string]any{"method": "publishStickerSet", "pack_id": draft, "title": "Public", "short_name": "ada_public"}); code != 200 {
		t.Fatalf("publish: %d %v", code, out)
	}
	if code, out := page(bobApp, "/stickers/pack/ada_public"); code != 200 || !strings.Contains(out["markup"].(string), "Public") {
		t.Fatalf("published pack for others: %d %v", code, out)
	}
	if code, _ := rpc(adaApp, map[string]any{"method": "deleteStickerSet", "pack_id": draft}); code != 200 {
		t.Fatalf("delete the published draft: %d", code)
	}

	if code, _ := rpc(adaApp, map[string]any{"method": "deleteStickerSet", "pack_id": pack}); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := page(adaApp, "/stickers/pack/"+pack); code != 404 {
		t.Fatalf("deleted pack: %d", code)
	}
}

// TestBotFatherPages: the pages BotFather's adapter loads are the
// launching person's.
// The real pages are Telegram's markup and live in the mini apps bundle;
// testdata/miniapps has stand-ins with the same values in them.
func TestBotFatherPages(t *testing.T) {
	s := start(t)
	botSvc := bots.New(s.db.Pool)
	botSvc.TokenKey = secret.FromBytes(make([]byte, 32))
	(&miniapps.Server{Dir: filepath.Join("testdata", "miniapps"), Bots: botSvc, Accounts: s.accounts, Media: s.media}).Register(s.mux)
	l := launcher{t, botSvc}
	base := s.http.URL
	ada := person{id: register(t, s, "ada")}
	bob := person{id: register(t, s, "bob")}
	adaApp, bobApp := l.launch(bots.BotFather, ada.id, ""), l.launch(bots.BotFather, bob.id, "")

	code, out := app(t, base, "POST", "/__miniapps/botfather/api", adaApp, map[string]any{"method": "createBot", "title": "Echo", "username": "echo_test_bot"})
	if code != 200 || out["token"] == nil {
		t.Fatalf("create: %d %v", code, out)
	}
	id := fmt.Sprint(int64(out["bot_id"].(float64)))
	page := func(init, path string) (int, map[string]any) {
		return app(t, base, "GET", path, init, nil, "X-Greengram-Page", "1")
	}
	if code, out := page(adaApp, "/botfather"); code != 200 || !strings.Contains(out["markup"].(string), "/botfather/bot/"+id) {
		t.Fatalf("list: %d %v", code, out)
	}
	if code, out := page(adaApp, "/botfather/bot/"+id); code != 200 || !strings.Contains(out["markup"].(string), "@echo_test_bot") {
		t.Fatalf("bot page: %d %v", code, out)
	}
	if code, _ := page(bobApp, "/botfather/bot/"+id); code != 404 {
		t.Fatalf("someone else's bot: %d", code)
	}
	// the owner reads the token back from the page, and only the current one
	token := out["token"].(string)
	if code, got := page(adaApp, "/botfather/bot/"+id); code != 200 || !strings.Contains(got["markup"].(string), token) {
		t.Fatalf("token on the page: %d %v", code, got)
	}
	call := func(method string, args map[string]any) map[string]any {
		t.Helper()
		args["method"], args["bid"] = method, id
		code, got := app(t, base, "POST", "/__miniapps/botfather/api", adaApp, args)
		if code != 200 {
			t.Fatalf("%s: %d %v", method, code, got)
		}
		return got
	}
	got := call("revokeAccessToken", map[string]any{})
	if _, markup := page(adaApp, "/botfather/bot/"+id); strings.Contains(markup["markup"].(string), token) || !strings.Contains(markup["markup"].(string), got["token"].(string)) {
		t.Fatalf("token after revoking: %v", markup)
	}
	// commands: add, edit in place, drop through the list's order
	if got := call("setCommand", map[string]any{"command": "hello", "description": "Say hello", "replace": ""}); got["ok"] != true {
		t.Fatalf("add command: %v", got)
	}
	if got := call("setCommand", map[string]any{"command": "hello", "description": "Again", "replace": ""}); got["ok"] != false {
		t.Fatalf("duplicate command: %v", got)
	}
	if got := call("setCommand", map[string]any{"command": "hi", "description": "Say hi", "replace": "hello"}); got["ok"] != true {
		t.Fatalf("edit command: %v", got)
	}
	if _, markup := page(adaApp, "/botfather/bot/"+id+"/commands"); !strings.Contains(markup["markup"].(string), "/hi") || strings.Contains(markup["markup"].(string), "Say hello") {
		t.Fatalf("commands page: %v", markup)
	}
	if got := call("reorderCommands", map[string]any{"commands": []any{}}); got["ok"] != true {
		t.Fatalf("drop commands: %v", got)
	}
	// a switch with nothing behind it is refused, not stored
	if got := call("changeSettings", map[string]any{"settings": map[string]any{"mgr": 1}}); got["ok"] != false {
		t.Fatalf("mgr: %v", got)
	}
	if got := call("changeSettings", map[string]any{"settings": map[string]any{"privacy_policy_url": "nope"}}); got["ok"] != false {
		t.Fatalf("bad privacy url: %v", got)
	}
	if got := call("changeSettings", map[string]any{"settings": map[string]any{"privacy_policy_url": "https://example.org/p", "grps": 0, "group_admin_rights": []any{"other"}}}); got["ok"] != true {
		t.Fatalf("settings: %v", got)
	}
	saved, _, err := botSvc.OwnedByUsername(context.Background(), ada.id, "echo_test_bot")
	if err != nil || saved.State.AllowGroups() || saved.State.Settings.PrivacyURL != "https://example.org/p" || len(saved.State.Settings.GroupRights) != 1 {
		t.Fatalf("saved state: %+v %v", saved.State, err)
	}
	// About is the profile text, the welcome message is the description
	if got := call("updateBotProfile", map[string]any{"title": "Echo", "about": "short", "welcome_msg": "long"}); got["ok"] != true {
		t.Fatalf("profile: %v", got)
	}
	saved, _, _ = botSvc.OwnedByUsername(context.Background(), ada.id, "echo_test_bot")
	if saved.About != "short" || saved.Description != "long" {
		t.Fatalf("profile saved: %+v", saved)
	}
	if code, out := page(bobApp, "/botfather"); code != 200 || strings.Contains(out["markup"].(string), "echo_test_bot") {
		t.Fatalf("someone else's list: %d %v", code, out)
	}

	// a bot's picture: kept on upload, the bot's only once the page saves
	bid := id
	upload := func(init string, body []byte) (int, map[string]any) {
		return app(t, base, "POST", "/__miniapps/botfather/upload?target=bot_userpic", init, body, "Content-Type", "image/png")
	}
	if code, _ := upload("", testPNG(200, 200)); code != 401 {
		t.Fatalf("upload without a launch: %d", code)
	}
	if code, out := upload(adaApp, []byte("not a picture")); code != 400 {
		t.Fatalf("junk upload: %d %v", code, out)
	}
	code, out = upload(adaApp, testPNG(200, 200))
	if code != 200 {
		t.Fatalf("upload: %d %v", code, out)
	}
	pic := out["media"].(map[string]any)
	if code, out := page(adaApp, "/botfather/bot/"+bid); code != 200 || strings.Contains(out["markup"].(string), pic["src"].(string)) {
		t.Fatalf("picture before saving: %d %v", code, out)
	}
	save := func(init string) (int, map[string]any) {
		return app(t, base, "POST", "/__miniapps/botfather/api", init, map[string]any{"method": "updateBotProfile", "bid": bid, "title": "Echo", "about": "", "userpic": pic["photo_id"]})
	}
	// bob can't take ada's upload for his own bot
	code, out = app(t, base, "POST", "/__miniapps/botfather/api", bobApp, map[string]any{"method": "createBot", "title": "Bobs", "username": "bobs_test_bot", "userpic": pic["photo_id"]})
	if code != 200 || out["ok"] != false {
		t.Fatalf("someone else's upload: %d %v", code, out)
	}
	if code, out := save(adaApp); code != 200 || out["ok"] != true {
		t.Fatalf("save: %d %v", code, out)
	}
	code, out = page(adaApp, "/botfather/bot/"+bid)
	if code != 200 || !strings.Contains(out["markup"].(string), strings.ReplaceAll(pic["src"].(string), "&", "&amp;")) {
		t.Fatalf("picture after saving: %d %v", code, out)
	}
	resp, err := http.Get(base + pic["src"].(string))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("picture: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	resp, err = http.Get(base + "/__miniapps/botfather/photo/" + pic["photo_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("unsigned picture: %d", resp.StatusCode)
	}
}

// TestWalletRecipients: the transfer sheet offers people you know, and
// finds others only by public username.
func TestWalletRecipients(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	botSvc := bots.New(s.db.Pool)
	auth := &web.Auth{Accounts: s.accounts, Bots: botSvc}
	w := wallet.New(s.db.Pool, s.settings, fixedMarket{map[string]string{"TON": "5", "USDT": "1"}}, s.api.Notify).WithCharts(noCharts{})
	(&web.Wallet{Auth: auth, Wallet: w}).Register(s.mux)
	l := launcher{t, botSvc}
	ada := register(t, s, "ada")
	bob := register(t, s, "bob_stone")
	carol := register(t, s, "carol")
	register(t, s, "carla")
	if _, err := s.db.Pool.Exec(ctx, "INSERT INTO contacts(owner_id, user_id, first_name, last_name) VALUES ($1, $2, 'Bob', '')", ada, bob); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Pool.Exec(ctx, "UPDATE users SET name='Carol Fox', username_public=false WHERE id=$1", carol); err != nil {
		t.Fatal(err)
	}
	find := func(init, q string) []string {
		t.Helper()
		code, out := app(t, s.http.URL, "GET", "/api/walt/telegram/recipients?q="+url.QueryEscape(q), init, nil)
		list, ok := out["users"].([]any)
		if code != 200 || !ok {
			t.Fatalf("recipients %q: %d %v", q, code, out)
		}
		names := []string{}
		for _, u := range list {
			names = append(names, u.(map[string]any)["mention"].(string))
		}
		return names
	}
	init := l.launch(bots.Wallet, ada, "")
	for _, c := range []struct{ q, want string }{
		{"", "[bob_stone]"},
		{"@bob", "[bob_stone]"},
		{"car", "[carla]"}, // carol hid her username and ada doesn't know her
		{"carol", "[]"},
		{"bob%", "[]"},
		{"_", "[]"},
		{"ada", "[]"},    // not yourself
		{"wallet", "[]"}, // not a bot
	} {
		if got := fmt.Sprint(find(init, c.q)); got != c.want {
			t.Errorf("recipients %q = %s, want %s", c.q, got, c.want)
		}
	}
	if _, err := s.db.Pool.Exec(ctx, "INSERT INTO contacts(owner_id, user_id, first_name, last_name) VALUES ($1, $2, 'C', '')", ada, carol); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(find(init, "fox")); got != "[carol]" {
		t.Errorf("a word of a known person's name: %s", got)
	}
	if code, _ := app(t, s.http.URL, "GET", "/api/walt/telegram/recipients", "", nil); code != 401 {
		t.Errorf("no launch: %d", code)
	}
}

// TestBuiltinAvatars: every built-in bot gets its picture once, and one
// set or hidden on purpose stays that way.
func TestBuiltinAvatars(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	botSvc := bots.New(s.db.Pool)
	// the pictures come from the mini apps bundle; a missing one is no error
	if err := builtin.SeedAvatars(ctx, t.TempDir(), botSvc, s.media); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, kind := range []string{bots.BotFather, bots.Stickers, bots.Wallet, bots.Fragment, bots.BugsBot} {
		if err := os.WriteFile(filepath.Join(dir, kind+".jpg"), testPNG(160, 160), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := builtin.SeedAvatars(ctx, dir, botSvc, s.media); err != nil {
		t.Fatal(err)
	}
	photo := func(kind string) int64 {
		t.Helper()
		b, ok, err := botSvc.ByKind(ctx, kind)
		if err != nil || !ok {
			t.Fatalf("bot %s: %v", kind, err)
		}
		u, ok, err := s.accounts.User(ctx, b.UserID)
		if err != nil || !ok {
			t.Fatalf("user %s: %v", kind, err)
		}
		return u.PhotoID
	}
	first := map[string]int64{}
	for _, kind := range []string{bots.BotFather, bots.Stickers, bots.Wallet, bots.Fragment, bots.BugsBot} {
		if first[kind] = photo(kind); first[kind] == 0 {
			t.Fatalf("%s has no picture", kind)
		}
	}
	wallet, _, _ := botSvc.ByKind(ctx, bots.Wallet)
	fragment, _, _ := botSvc.ByKind(ctx, bots.Fragment)
	mine, err := s.media.NewPhoto(ctx, wallet.UserID, testPNG(200, 200))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.media.SetUserPhoto(ctx, wallet.UserID, mine.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.media.ClearUserPhoto(ctx, fragment.UserID); err != nil {
		t.Fatal(err)
	}
	if err := builtin.SeedAvatars(ctx, dir, botSvc, s.media); err != nil {
		t.Fatal(err)
	}
	if got := photo(bots.BotFather); got != first[bots.BotFather] {
		t.Fatalf("second run replaced BotFather's picture: %d", got)
	}
	if got := photo(bots.Wallet); got != mine.ID {
		t.Fatalf("a chosen picture was replaced: %d", got)
	}
	if got := photo(bots.Fragment); got != 0 {
		t.Fatalf("a hidden picture came back: %d", got)
	}
}

func register(t *testing.T, s *stack, name string) int64 {
	t.Helper()
	b := s.browser(t)
	return int64(b.must("/api/register", map[string]string{"username": name, "name": name, "password": "correct horse 9"})["id"].(float64))
}

// TestFragmentPage needs the captured Fragment files.
func TestFragmentPage(t *testing.T) {
	dir := os.Getenv("GREENGRAM_TEST_MINIAPPS_DIR")
	if dir == "" {
		t.Skip("GREENGRAM_TEST_MINIAPPS_DIR is not set: no captured Fragment page to test against")
	}
	s := start(t)
	botSvc := bots.New(s.db.Pool)
	frag := fragment.New(s.db.Pool, s.secret, nil)
	(&miniapps.Server{Dir: dir, Bots: botSvc, Accounts: s.accounts, Media: s.media, Fragment: frag}).Register(s.mux)
	(&web.Fragment{Auth: &web.Auth{Accounts: s.accounts, Bots: botSvc}, Fragment: frag}).Register(s.mux)
	l := launcher{t, botSvc}
	ada := person{id: register(t, s, "ada")}
	if _, err := s.db.Pool.Exec(context.Background(), "INSERT INTO fragment_items(id, kind, title, units, listed) VALUES ('username:shiny', 'username', '@shiny', 5000000000, true)"); err != nil {
		t.Fatal(err)
	}
	init := l.launch(bots.Fragment, ada.id, "")
	code, out := app(t, s.http.URL, "GET", "/fragment", init, nil, "X-Greengram-Page", "1")
	if code != 200 || !strings.Contains(out["markup"].(string), "fragment-items") || !strings.Contains(out["extra"].(string), "fragment-row-template") {
		t.Fatalf("page: %d %v", code, out)
	}
	if code, out := app(t, s.http.URL, "GET", "/fragment/username/shiny", init, nil, "X-Greengram-Page", "1"); code != 200 || !strings.Contains(out["markup"].(string), "Buy now") {
		t.Fatalf("item page: %d %v", code, out)
	}
	if code, out := app(t, s.http.URL, "GET", "/api/fragment/catalog", init, nil); code != 200 || out["balance"] == nil {
		t.Fatalf("catalog via launch: %d %v", code, out)
	}
}

// TestStickersZip: a whole pack as an archive, and archives that try something.
func TestStickersZip(t *testing.T) {
	s := start(t)
	botSvc := bots.New(s.db.Pool)
	(&miniapps.Server{Bots: botSvc, Accounts: s.accounts, Media: s.media}).Register(s.mux)
	l := launcher{t, botSvc}
	base := s.http.URL
	ada := s.browser(t)
	adaID := int64(ada.must("/api/register", map[string]string{"username": "ada", "name": "Ada", "password": "correct horse 9"})["id"].(float64))
	adaApp := l.launch(bots.Stickers, adaID, "")
	archive := func(files ...any) []byte {
		var b bytes.Buffer
		w := zip.NewWriter(&b)
		for i := 0; i+1 < len(files); i += 2 {
			f, err := w.Create(files[i].(string))
			if err != nil {
				t.Fatal(err)
			}
			f.Write(files[i+1].([]byte))
		}
		w.Close()
		return b.Bytes()
	}
	lottie := func(fr int) []byte {
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		fmt.Fprintf(zw, `{"v":"5.5.2","fr":%d,"ip":0,"op":60,"w":512,"h":512,"layers":[{"ty":4}]}`, fr)
		zw.Close()
		return gz.Bytes()
	}
	tgs := lottie(60)

	if code, _ := app(t, base, "POST", "/__miniapps/stickers/import?kind=emoji", "", archive("a.tgs", tgs)); code != 401 {
		t.Fatalf("import without a launch: %d", code)
	}
	if code, _ := app(t, base, "POST", "/__miniapps/stickers/import?kind=emoji", adaApp, []byte("not a zip")); code != 400 {
		t.Fatalf("not a zip: %d", code)
	}
	if code, _ := app(t, base, "POST", "/__miniapps/stickers/import?kind=emoji", adaApp, archive("../../evil.tgs", tgs)); code != 400 {
		t.Fatalf("traversal: %d", code)
	}
	var many []any
	for i := range 2001 {
		many = append(many, "f"+strconv.Itoa(i)+".tgs", []byte{})
	}
	if code, _ := app(t, base, "POST", "/__miniapps/stickers/import?kind=emoji", adaApp, archive(many...)); code != 413 {
		t.Fatalf("too many files: %d", code)
	}
	// a deflate bomb declares its real size, so the plan stops it unread
	var bomb bytes.Buffer
	bw := zip.NewWriter(&bomb)
	bf, _ := bw.Create("bomb.tgs")
	chunk := make([]byte, 1<<20)
	for range 300 {
		bf.Write(chunk)
	}
	bw.Close()
	if code, _ := app(t, base, "POST", "/__miniapps/stickers/import?kind=emoji", adaApp, bomb.Bytes()); code != 413 {
		t.Fatalf("bomb: %d", code)
	}

	code, out := app(t, base, "POST", "/__miniapps/stickers/import?kind=emoji", adaApp, archive(
		"happy/happy.tgs", tgs,
		"😀_smile/😀_smile.tgs", lottie(30),
		"fake/fake.tgs", []byte("<script>alert(1)</script>"),
		"fake/fake2.webm", []byte{0x1a, 0x45, 0xdf, 0xa3, 0x80},
		"dupe/happy.tgs", tgs,
		"pic/big.png", testPNG(700, 700),
		"__MACOSX/happy/._happy.tgs", []byte("x"),
	))
	if code != 200 {
		t.Fatalf("import: %d %v", code, out)
	}
	items := out["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("items: %v", out)
	}
	// in name order, so the copy in dupe/ is the one kept
	if items[0].(map[string]any)["name"] != "dupe/happy.tgs" || items[2].(map[string]any)["emoji"] != "😀" {
		t.Fatalf("items: %v", out)
	}
	var stickers []any
	for _, v := range items {
		it := v.(map[string]any)
		stickers = append(stickers, map[string]any{"doc_id": it["doc"], "emoji": it["emoji"]})
	}
	// the fake .tgs, the fake .webm and the duplicate
	if skipped := out["skipped"].([]any); len(skipped) != 3 {
		t.Fatalf("skipped: %v", skipped)
	}
	code, out = app(t, base, "POST", "/__miniapps/stickers/api", adaApp, map[string]any{"method": "createStickerSet", "title": "From a zip", "is_emoji": "1", "stickers": stickers})
	if code != 200 {
		t.Fatalf("save: %d %v", code, out)
	}
}
