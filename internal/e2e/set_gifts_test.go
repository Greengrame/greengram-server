package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/admin"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/stars"
)

// TestGiftsForSets: every imported collection gets a catalog gift in one go
// (ggcollectibles -gifts -supply 1000 -off-sale), and one an admin turns on
// can be bought and upgraded into its collection. The defaults, on sale and
// unlimited, are TestUnlimitedCollection's.
func TestGiftsForSets(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	// the first account is the admin
	boss := s.browser(t)
	boss.must("/api/register", map[string]string{"username": "boss", "name": "Boss", "password": "correct horse boss"})
	adm := admin.New(s.db.Pool, s.accounts, s.settings, channels.New(s.db.Pool))
	adm.Gifts = s.stars
	adm.Media = s.media
	adm.Register(s.mux)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	for _, c := range [][2]string{{"plushfrog", "Plush Frog"}, {"deskclock", "Desk Clock"}, {"spicedtea", "Spiced Tea"}, {"oldlamp", "Old Lamp"}} {
		if _, err := s.media.ImportCollectibles(ctx, testCollection(t, c[0], c[1])); err != nil {
			t.Fatal(err)
		}
	}
	set := func(name string) int64 {
		var id int64
		if err := s.db.Pool.QueryRow(ctx, "SELECT id FROM collectible_sets WHERE name=$1", name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// one already has its gift, one's title is taken by a plain gift
	mine, err := s.stars.AddGift(ctx, stars.Gift{Title: "Lamp", Emoji: "🪔", Stars: 300, ConvertStars: 200})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.stars.SetGiftUpgrade(ctx, mine.ID, stars.UpgradeSettings{Set: set("oldlamp"), Stars: 70, Supply: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.stars.AddGift(ctx, stars.Gift{Title: "spiced tea", Emoji: "🍵", Stars: 40, ConvertStars: 30}); err != nil {
		t.Fatal(err)
	}
	before := len(func() []stars.Gift { l, _ := s.stars.Catalog(ctx); return l }())

	d := stars.SetGiftDefaults{Stars: 100, ConvertStars: 85, UpgradeStars: 50, TransferStars: 25, Supply: 1000, OffSale: true, NoVariants: true}
	for _, bad := range []stars.SetGiftDefaults{{Stars: 0, UpgradeStars: 50, Supply: 1}, {Stars: 100, UpgradeStars: 0, Supply: 1}, {Stars: 100, UpgradeStars: 50, Supply: -1}, {Stars: 100, ConvertStars: 101, UpgradeStars: 50, Supply: 1}} {
		if _, err := s.stars.GiftsForSets(ctx, bad); err == nil {
			t.Fatalf("defaults %+v taken", bad)
		}
	}
	res, err := s.stars.GiftsForSets(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(res.Created)
	if !slices.Equal(res.Created, []string{"Desk Clock", "Plush Frog"}) || !slices.Equal(res.Skipped, []string{"Spiced Tea"}) {
		t.Fatalf("made %v, skipped %v", res.Created, res.Skipped)
	}
	again, err := s.stars.GiftsForSets(ctx, d)
	if err != nil || len(again.Created) != 0 {
		t.Fatalf("again: %+v %v", again, err)
	}
	var id, sticker, original int64
	var enabled bool
	var stock, total, supply int
	err = s.db.Pool.QueryRow(ctx, `SELECT g.id, g.enabled, g.stock, g.stock_total, coalesce(g.sticker_id, 0), coalesce(s.original_document_id, -1), s.supply
		FROM gift_catalog g JOIN collectible_sets s ON s.id = g.upgrade_set_id WHERE s.name='plushfrog' AND g.stars=100 AND g.upgrade_stars=50 AND g.transfer_stars=25 AND g.convert_stars=85`).
		Scan(&id, &enabled, &stock, &total, &sticker, &original, &supply)
	if err != nil {
		t.Fatal(err)
	}
	if enabled || stock != 1000 || total != 1000 || supply != 1000 || sticker != original {
		t.Fatalf("gift %d: enabled %v, stock %d/%d, supply %d, art %d of %d", id, enabled, stock, total, supply, sticker, original)
	}
	if n := len(func() []stars.Gift { l, _ := s.stars.Catalog(ctx); return l }()); n != before {
		t.Fatalf("on sale now: %d, was %d", n, before)
	}

	// an admin turns one on
	raw, _ := json.Marshal(map[string]any{"enabled": true})
	req, _ := http.NewRequest("PATCH", boss.base+"/api/admin/gifts/"+strconv.FormatInt(id, 10), bytes.NewReader(raw))
	resp, err := boss.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("enable: %d", resp.StatusCode)
	}

	if _, _, err := s.stars.Adjust(ctx, alice.id, alice.id, 1000, "starter", "set-gifts-1"); err != nil {
		t.Fatal(err)
	}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		list, err := c.PaymentsGetStarGifts(ctx, 0)
		if err != nil {
			return err
		}
		var g *tg.StarGift
		for _, x := range list.(*tg.PaymentsStarGifts).Gifts {
			if sg := x.(*tg.StarGift); sg.ID == id {
				g = sg
			}
		}
		if g == nil || g.Title != "Plush Frog" || g.UpgradeStars != 50 || !g.Limited || g.AvailabilityTotal != 1000 {
			return fmt.Errorf("on sale as %+v", g)
		}
		if d, ok := g.Sticker.(*tg.Document); !ok || d.ID != original {
			return fmt.Errorf("art %+v, want document %d", g.Sticker, original)
		}
		_, _, err = pay(ctx, c, &tg.InputInvoiceStarGift{Peer: userPeer(bob.id), GiftID: id, IncludeUpgrade: true})
		return err
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		g, err := mineByGift(ctx, c, id)
		if err != nil {
			return err
		}
		up, err := c.PaymentsUpgradeStarGift(ctx, &tg.PaymentsUpgradeStarGiftRequest{Stargift: selfSaved(g)})
		if err != nil {
			return err
		}
		if _, u := uniqueOf(up); u == nil || u.Title != "Plush Frog" || u.Num != 1 || u.AvailabilityTotal != 1000 {
			return fmt.Errorf("upgraded into %+v", u)
		}
		return nil
	})
}
