package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/admin"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/stars"
)

func TestAdminGifts(t *testing.T) {
	s := start(t)
	boss := s.browser(t)
	boss.must("/api/register", map[string]string{"username": "boss", "name": "Boss", "password": "correct horse boss"})
	alice := s.signUp(t, "alice")
	adm := admin.New(s.db.Pool, s.accounts, s.settings, channels.New(s.db.Pool))
	adm.Gifts = s.stars
	adm.Media = s.media
	adm.Register(s.mux)
	if _, err := s.db.Pool.Exec(context.Background(), `INSERT INTO sticker_sets(id, access_hash, short_name, title, kind, builtin, created_at)
		VALUES (900100, 1, 'giftpick', 'Gift Pick', 'emoji', NULL, 1), (900101, 1, 'giftbuilt', 'Gift Built', 'emoji', 'gifttest', 1)`); err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, boss.base+path, bytes.NewReader(raw))
		resp, err := boss.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out := map[string]any{}
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// the migrations' gifts, none of them doubled
	code, out := call("GET", "/api/admin/gifts", nil)
	if code != 200 {
		t.Fatalf("list: %d %v", code, out)
	}
	seeded := out["gifts"].([]any)
	titles := map[string]bool{}
	for _, g := range seeded {
		title := g.(map[string]any)["title"].(string)
		if titles[title] {
			t.Fatalf("%s twice", title)
		}
		titles[title] = true
	}
	if len(seeded) != 38 || !titles["Teddy Bear"] || !titles["Race Car"] {
		t.Fatalf("catalog of %d: %v", len(seeded), titles)
	}

	for name, bad := range map[string]map[string]any{
		"no price":                     {"title": "Moon", "emoji": "🌕"},
		"free":                         {"title": "Moon", "emoji": "🌕", "stars": 0},
		"no title":                     {"title": " ", "emoji": "🌕", "stars": 10},
		"prints":                       {"title": "Moon", "emoji": "🌕", "stars": 10, "convertStars": 11},
		"bad sticker":                  {"title": "Moon", "emoji": "🌕", "stars": 10, "stickerId": 12345},
		"no emoji":                     {"title": "Moon", "stars": 10},
		"blank emoji":                  {"title": "Moon", "emoji": " ", "stars": 10},
		"blank emoji, no such sticker": {"title": "Moon", "emoji": "", "stars": 10, "stickerId": "12345"},
	} {
		if code, out := call("POST", "/api/admin/gifts", bad); code != 400 {
			t.Fatalf("%s: %d %v", name, code, out)
		}
	}

	code, moon := call("POST", "/api/admin/gifts", map[string]any{"title": "Moon", "emoji": "🌕", "stars": 40, "convertStars": 30, "stock": 2})
	if code != 200 || moon["limited"] != true || moon["stock"] != 2.0 || moon["stockTotal"] != 2.0 || moon["enabled"] != true {
		t.Fatalf("add: %d %v", code, moon)
	}
	id := int64(moon["id"].(float64))
	path := fmt.Sprintf("/api/admin/gifts/%d", id)
	if code, out := call("PATCH", path, map[string]any{"convertStars": 41}); code != 400 {
		t.Fatalf("edit past its price: %d %v", code, out)
	}
	if code, out := call("PATCH", path, map[string]any{"stars": 60, "convertStars": 50, "title": "Full Moon"}); code != 200 || out["stars"] != 60.0 || out["title"] != "Full Moon" || out["stock"] != 2.0 {
		t.Fatalf("edit: %d %v", code, out)
	}
	if code, out := call("PATCH", "/api/admin/gifts/99999999", map[string]any{"stars": 60}); code != 404 {
		t.Fatalf("edit nothing: %d %v", code, out)
	}

	// what people see follows; a bought gift can only leave the shop
	inCatalog := func(ctx context.Context, c *tg.Client) (*tg.StarGift, error) {
		gifts, err := c.PaymentsGetStarGifts(ctx, 0)
		if err != nil {
			return nil, err
		}
		for _, g := range gifts.(*tg.PaymentsStarGifts).Gifts {
			if g := g.(*tg.StarGift); g.ID == id {
				return g, nil
			}
		}
		return nil, nil
	}
	if _, _, err := s.stars.Adjust(context.Background(), alice.id, alice.id, 100, "starter", "admin-gifts-1"); err != nil {
		t.Fatal(err)
	}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		g, err := inCatalog(ctx, c)
		if err != nil || g == nil || g.Stars != 60 || g.Title != "Full Moon" {
			return fmt.Errorf("edited gift %+v %v", g, err)
		}
		_, _, err = pay(ctx, c, &tg.InputInvoiceStarGift{Peer: &tg.InputPeerSelf{}, GiftID: id})
		return err
	})
	if code, out := call("DELETE", path, nil); code != 200 || out["deleted"] != false {
		t.Fatalf("remove a bought gift: %d %v", code, out)
	}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if g, err := inCatalog(ctx, c); err != nil || g != nil {
			return fmt.Errorf("removed gift still on sale %+v %v", g, err)
		}
		// the gift she already has still draws
		if _, act, err := serviceAction[*tg.MessageActionStarGift](ctx, c, alice.id); err != nil || act.Gift.(*tg.StarGift).ID != id {
			return fmt.Errorf("her gift %+v %v", act, err)
		}
		saved, err := c.PaymentsGetSavedStarGifts(ctx, &tg.PaymentsGetSavedStarGiftsRequest{Peer: &tg.InputPeerSelf{}, Limit: 10})
		if err != nil || saved.Count != 1 {
			return fmt.Errorf("saved %+v %v", saved, err)
		}
		return nil
	})
	code, spare := call("POST", "/api/admin/gifts", map[string]any{"title": "Spare", "emoji": "🌕", "stars": 5})
	if code != 200 {
		t.Fatalf("add: %d %v", code, spare)
	}
	if code, out := call("DELETE", fmt.Sprintf("/api/admin/gifts/%d", int64(spare["id"].(float64))), nil); code != 200 || out["deleted"] != true {
		t.Fatalf("remove an unsold gift: %d %v", code, out)
	}

	// a custom emoji can stand in for the emoji, which comes from its pack.
	// Ids over 2^53 show they stay strings.
	ctx := context.Background()
	const inPack, loose = int64(9007199254740993), int64(9007199254740995)
	if _, err := s.db.Pool.Exec(ctx, "INSERT INTO files(key, size) VALUES (repeat('a', 64), 1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Pool.Exec(ctx, `INSERT INTO documents(id, access_hash, date, mime, kind, key, size)
		VALUES ($1, 1, 1, 'application/x-tgsticker', 'emoji', repeat('a', 64), 1), ($2, 1, 1, 'video/webm', 'emoji', repeat('a', 64), 1)`, inPack, loose); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Pool.Exec(ctx, "INSERT INTO sticker_set_items(set_id, position, document_id, emoji) VALUES (900100, 0, $1, '🦊')", inPack); err != nil {
		t.Fatal(err)
	}
	code, fox := call("POST", "/api/admin/gifts", map[string]any{"title": "Fox", "emoji": "", "stars": 5, "stickerId": strconv.FormatInt(inPack, 10)})
	if code != 200 || fox["stickerId"] != strconv.FormatInt(inPack, 10) || fox["emoji"] != "🦊" {
		t.Fatalf("custom emoji, no emoji: %d %v", code, fox)
	}
	code, bare := call("POST", "/api/admin/gifts", map[string]any{"title": "Bare", "stars": 5, "stickerId": strconv.FormatInt(loose, 10)})
	if code != 200 || bare["stickerId"] != strconv.FormatInt(loose, 10) || bare["emoji"] != "" {
		t.Fatalf("no pack, no emoji: %d %v", code, bare)
	}
	barePath := fmt.Sprintf("/api/admin/gifts/%d", int64(bare["id"].(float64)))
	if code, out := call("PATCH", barePath, map[string]any{"stickerId": nil}); code != 400 {
		t.Fatalf("left with neither: %d %v", code, out)
	}
	if code, out := call("PATCH", barePath, map[string]any{"stickerId": nil, "emoji": "🌕"}); code != 200 || out["stickerId"] != nil || out["emoji"] != "🌕" {
		t.Fatalf("back to an emoji: %d %v", code, out)
	}
	if got := stars.GiftText(stars.Gift{Title: "Bare"}, ""); got != "Bare" {
		t.Fatalf("gift text without an emoji: %q", got)
	}

	// add, edit, remove, add, remove, then two adds and an edit: every change is on the record
	var n int
	if err := s.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_log WHERE action LIKE 'gift_%'").Scan(&n); err != nil || n != 8 {
		t.Fatalf("audit %d %v", n, err)
	}

	// the form's picker, with the ids as strings
	if code, out := call("GET", "/api/admin/sticker-packs/900100/items", nil); code != 200 || len(out["items"].([]any)) != 1 ||
		out["items"].([]any)[0].(map[string]any)["id"] != strconv.FormatInt(inPack, 10) || out["items"].([]any)[0].(map[string]any)["emoji"] != "🦊" {
		t.Fatalf("pack items: %d %v", code, out)
	}
	if code, out := call("GET", "/api/admin/sticker-packs/900101/items", nil); code != 404 {
		t.Fatalf("items of a built-in pack: %d %v", code, out)
	}
	if code, out := call("GET", "/api/admin/sticker-packs/123/items", nil); code != 404 {
		t.Fatalf("items of no pack: %d %v", code, out)
	}

	// someone else gets nothing
	other := s.browser(t)
	other.must("/api/register", map[string]string{"username": "mallory", "name": "M", "password": "correct horse mallory"})
	req, _ := http.NewRequest("POST", other.base+"/api/admin/gifts", bytes.NewReader([]byte(`{"title":"X","emoji":"🌕","stars":1}`)))
	resp, err := other.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Fatalf("non-admin: %d", resp.StatusCode)
	}
}
