package e2e

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/admin"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/stars"
)

// TestUnlimitedCollection: ggcollectibles -gifts with no flags puts gifts on
// sale with no stock limit and leaves their collections unlimited, so
// upgrades keep numbering past the old 10000 default. An admin can put a
// limit back, never under what's issued plus what's paid for, and take it
// off again.
func TestUnlimitedCollection(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	boss := s.browser(t)
	boss.must("/api/register", map[string]string{"username": "boss", "name": "Boss", "password": "correct horse boss"})
	adm := admin.New(s.db.Pool, s.accounts, s.settings, channels.New(s.db.Pool))
	adm.Gifts = s.stars
	adm.Media = s.media
	adm.Register(s.mux)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")
	if _, err := s.media.ImportCollectibles(ctx, testCollection(t, "testpepe", "Test Pepe")); err != nil {
		t.Fatal(err)
	}
	res, err := s.stars.GiftsForSets(ctx, stars.SetGiftDefaults{Stars: 100, ConvertStars: 85, UpgradeStars: 50, TransferStars: 25})
	if err != nil || len(res.Created) != 1 {
		t.Fatalf("gifts for sets: %+v %v", res, err)
	}
	k := &nftKit{t: t, s: s, ctx: ctx}
	k.set = k.int("SELECT id FROM collectible_sets WHERE name='testpepe'")
	gift := k.int("SELECT id FROM gift_catalog WHERE upgrade_set_id=$1 AND enabled AND stock IS NULL AND stock_total IS NULL", k.set)
	if n := k.int("SELECT supply FROM collectible_sets WHERE id=$1", k.set); n != 0 {
		t.Fatalf("supply %d, want unlimited", n)
	}
	// as if 10000 were out already
	if _, err := s.db.Pool.Exec(ctx, "UPDATE collectible_sets SET issued=10000 WHERE id=$1", k.set); err != nil {
		t.Fatal(err)
	}
	k.fund(alice, 10000, "unlimited-1")
	k.fund(bob, 1000, "unlimited-2")
	k.begin()

	limit := func(supply any) (int, map[string]any) {
		return boss.call("PATCH", "/api/admin/gifts/"+strconv.FormatInt(gift, 10),
			map[string]any{"upgrade": map[string]any{"setId": k.set, "stars": 50, "transferStars": 25, "supply": supply}})
	}
	// issued is the collection's now, not the number's
	want := func(u *tg.StarGiftUnique, num, issued int) error {
		if u == nil || u.Num != num || u.Slug != "TestPepe-"+strconv.Itoa(num) || u.AvailabilityIssued != issued || u.AvailabilityTotal != 0 {
			return fmt.Errorf("collectible %+v, want #%d of an unlimited collection", u, num)
		}
		return nil
	}

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		list, err := c.PaymentsGetStarGifts(ctx, 0)
		if err != nil {
			return err
		}
		var g *tg.StarGift
		for _, x := range list.(*tg.PaymentsStarGifts).Gifts {
			if sg := x.(*tg.StarGift); sg.ID == gift {
				g = sg
			}
		}
		if g == nil || g.Limited || g.SoldOut || g.AvailabilityTotal != 0 || g.UpgradeStars != 50 {
			return fmt.Errorf("on sale as %+v", g)
		}
		for i := 0; i < 3; i++ {
			if _, _, err := pay(ctx, c, &tg.InputInvoiceStarGift{Peer: userPeer(bob.id), GiftID: gift}); err != nil {
				return err
			}
		}
		_, _, err = pay(ctx, c, &tg.InputInvoiceStarGift{Peer: userPeer(carol.id), GiftID: gift, IncludeUpgrade: true})
		return err
	})
	k.burnt += 400
	k.same("gifts")

	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		for num := 10001; num <= 10003; num++ {
			g, err := mineByGift(ctx, c, gift)
			if err != nil {
				return err
			}
			if !g.CanUpgrade {
				return fmt.Errorf("not upgradable: %+v", g)
			}
			_, up, err := payResult(ctx, c, &tg.InputInvoiceStarGiftUpgrade{Stargift: selfSaved(g)})
			if err != nil {
				return err
			}
			if _, u := uniqueOf(up); want(u, num, num) != nil {
				return fmt.Errorf("upgrade %d: %w", num-10000, want(u, num, num))
			}
		}
		got, err := c.PaymentsGetUniqueStarGift(ctx, "TestPepe-10002")
		if err != nil {
			return err
		}
		return want(got.Gift.(*tg.StarGiftUnique), 10002, 10003)
	})
	k.same("paid upgrades")
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		g, err := mineByGift(ctx, c, gift)
		if err != nil {
			return err
		}
		up, err := c.PaymentsUpgradeStarGift(ctx, &tg.PaymentsUpgradeStarGiftRequest{Stargift: selfSaved(g)})
		if err != nil {
			return err
		}
		act, u := uniqueOf(up)
		if act == nil || !act.PrepaidUpgrade {
			return fmt.Errorf("prepaid upgrade %+v", act)
		}
		return want(u, 10004, 10004)
	})
	k.same("prepaid upgrade")

	// one more paid for up front holds a number; a limit has to leave it
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		_, _, err := pay(ctx, c, &tg.InputInvoiceStarGift{Peer: userPeer(bob.id), GiftID: gift, IncludeUpgrade: true})
		return err
	})
	k.burnt += 100
	for _, bad := range []any{10003, 10004, -1} {
		if code, _ := limit(bad); code != 400 {
			t.Fatalf("supply %v: %d", bad, code)
		}
	}
	code, view := limit(10005)
	if up, _ := view["upgrade"].(map[string]any); code != 200 || up == nil || up["supply"] != float64(10005) || up["issued"] != float64(10004) {
		t.Fatalf("limit 10005: %d %v", code, view)
	}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if _, _, err := pay(ctx, c, &tg.InputInvoiceStarGift{Peer: userPeer(bob.id), GiftID: gift, IncludeUpgrade: true}); !isErr(err, "STARGIFT_UPGRADE_UNAVAILABLE") {
			return fmt.Errorf("prepaying past the limit: %v", err)
		}
		if _, _, err := pay(ctx, c, &tg.InputInvoiceStarGift{Peer: &tg.InputPeerSelf{}, GiftID: gift}); err != nil {
			return err
		}
		g, err := mineByGift(ctx, c, gift)
		if err != nil {
			return err
		}
		if g.CanUpgrade {
			return fmt.Errorf("upgradable past the limit %+v", g)
		}
		_, err = c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: &tg.InputInvoiceStarGiftUpgrade{Stargift: selfSaved(g)}})
		return expectErr(err, "STARGIFT_USAGE_LIMITED", "upgrade past the limit")
	})
	k.burnt += 100
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		g, err := mineByGift(ctx, c, gift)
		if err != nil {
			return err
		}
		up, err := c.PaymentsUpgradeStarGift(ctx, &tg.PaymentsUpgradeStarGiftRequest{Stargift: selfSaved(g)})
		if err != nil {
			return err
		}
		if _, u := uniqueOf(up); u == nil || u.Num != 10005 || u.AvailabilityIssued != 10005 || u.AvailabilityTotal != 10005 {
			return fmt.Errorf("last number %+v", u)
		}
		return nil
	})
	k.same("limited")

	// and off again
	if code, view := limit(nil); code != 200 || view["upgrade"].(map[string]any)["supply"] != float64(0) {
		t.Fatalf("unlimited again: %d %v", code, view)
	}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		g, err := mineByGift(ctx, c, gift)
		if err != nil {
			return err
		}
		_, up, err := payResult(ctx, c, &tg.InputInvoiceStarGiftUpgrade{Stargift: selfSaved(g)})
		if err != nil {
			return err
		}
		_, u := uniqueOf(up)
		return want(u, 10006, 10006)
	})
	k.same("unlimited again")
	nums := k.str("SELECT string_agg(num::text, ',' ORDER BY created_at, num) FROM unique_gifts WHERE set_id=$1", k.set)
	if nums != "10001,10002,10003,10004,10005,10006" || k.int("SELECT count(DISTINCT slug) FROM unique_gifts") != 6 {
		t.Fatalf("numbers %s", nums)
	}
	if k.issued() != 10006 || k.gifts() != 300 {
		t.Fatalf("issued %d, gifts account %d", k.issued(), k.gifts())
	}
}
