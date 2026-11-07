package e2e

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/admin"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/stars"
)

// testCollection writes a collectibles bundle like scripts/collectibles.py
// makes, with made-up art: three models, two patterns, two backdrops.
func testCollection(t *testing.T, name, title string) string {
	t.Helper()
	dir := t.TempDir()
	assets := map[string][]byte{}
	asset := func(data []byte, ext string) map[string]any {
		sum := sha256.Sum256(data)
		h := hex.EncodeToString(sum[:])
		assets[h+"."+ext] = data
		return map[string]any{"file": "assets/" + h + "." + ext, "sha256": h, "size": len(data)}
	}
	tgs := func(n string) map[string]any {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		fmt.Fprintf(zw, `{"tgs":1,"v":"5.5.2","fr":60,"ip":0,"op":60,"w":512,"h":512,"nm":%q,"ddd":0,"assets":[],"layers":[{"ty":4,"nm":%q,"ks":{},"shapes":[]}]}`, n, n)
		zw.Close()
		return asset(b.Bytes(), "tgs")
	}
	pic := func(shade uint8) map[string]any {
		img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
		for i := range img.Pix {
			img.Pix[i] = shade
		}
		var b bytes.Buffer
		png.Encode(&b, img)
		return asset(b.Bytes(), "png")
	}
	set := map[string]any{"name": name, "title": title, "original": tgs(name + "-original"),
		"models": []any{
			map[string]any{"name": "Common", "rarityPermille": 500, "sticker": tgs(name + "-common")},
			map[string]any{"name": "Uncommon", "rarityPermille": 300, "sticker": tgs(name + "-uncommon")},
			map[string]any{"name": "Rare", "rarityPermille": 200, "sticker": tgs(name + "-rare")},
		},
		"patterns": []any{
			map[string]any{"name": "Star", "rarityPermille": 600, "sticker": tgs(name + "-star"), "image": pic(10)},
			map[string]any{"name": "Moon", "rarityPermille": 400, "sticker": tgs(name + "-moon"), "image": pic(200)},
		},
		"backdrops": []any{
			map[string]any{"name": "Onyx Black", "backdropId": 7, "rarityPermille": 700, "centerColor": 0x363738, "edgeColor": 0x0e0f0f, "patternColor": 0x6c6868, "textColor": 0x8c8f91},
			map[string]any{"name": "Pure Gold", "backdropId": 9, "rarityPermille": 300, "centerColor": 0xccab41, "edgeColor": 0x8e6b1c, "patternColor": 0x4f3a0a, "textColor": 0xfff4c2},
		},
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "source": "test", "collections": []any{set}})
	sum := sha256.Sum256(raw)
	build := hex.EncodeToString(sum[:])
	root := filepath.Join(dir, "builds", build)
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "manifest.json"), raw, 0o644)
	for n, b := range assets {
		os.WriteFile(filepath.Join(root, "assets", n), b, 0o644)
	}
	act, _ := json.Marshal(map[string]any{"version": 1, "build": build})
	os.WriteFile(filepath.Join(dir, "active.json"), act, 0o644)
	return dir
}

// nftKit is an instance with a collection imported and a gift that
// upgrades into it, and the checks every step makes.
type nftKit struct {
	t     *testing.T
	s     *stack
	ctx   context.Context
	set   int64
	gift  stars.Gift
	burnt int64 // Stars plain gift purchases took out of the ledger, as they always have
	start int64
}

func newNFTKit(t *testing.T, s *stack, supply int) *nftKit {
	t.Helper()
	k := &nftKit{t: t, s: s, ctx: context.Background()}
	dir := testCollection(t, "testpepe", "Test Pepe")
	for i := 0; i < 2; i++ {
		res, err := s.media.ImportCollectibles(k.ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		if res.Sets != 1 || res.Models != 3 || res.Patterns != 2 || res.Backdrops != 2 {
			t.Fatalf("import %+v", res)
		}
	}
	if n := k.int("SELECT count(*) FROM collectible_models"); n != 3 {
		t.Fatalf("importing twice left %d models", n)
	}
	k.set = k.int("SELECT id FROM collectible_sets WHERE name='testpepe'")
	if slug := k.str("SELECT slug FROM collectible_sets WHERE id=$1", k.set); slug != "TestPepe" {
		t.Fatalf("slug %q", slug)
	}
	var err error
	k.gift, err = s.stars.AddGift(k.ctx, stars.Gift{Title: "Test Pepe", Emoji: "🐸", Stars: 100, ConvertStars: 80})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.stars.SetGiftUpgrade(k.ctx, k.gift.ID, stars.UpgradeSettings{Set: k.set, Stars: 50, TransferStars: 25, Supply: supply}); err != nil {
		t.Fatal(err)
	}
	return k
}

func (k *nftKit) int(q string, args ...any) int64 {
	k.t.Helper()
	var n int64
	if err := k.s.db.Pool.QueryRow(k.ctx, q, args...).Scan(&n); err != nil {
		k.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (k *nftKit) str(q string, args ...any) string {
	k.t.Helper()
	var s string
	if err := k.s.db.Pool.QueryRow(k.ctx, q, args...).Scan(&s); err != nil {
		k.t.Fatalf("%s: %v", q, err)
	}
	return s
}

func (k *nftKit) total() int64 { return k.int("SELECT coalesce(sum(amount), 0) FROM star_ledger") }

func (k *nftKit) begin() { k.start = k.total() }

// same: upgrades and fees only move Stars, so the ledger adds up to what it
// did less what plain purchases took.
func (k *nftKit) same(step string) {
	k.t.Helper()
	if got := k.total(); got != k.start-k.burnt {
		k.t.Fatalf("%s: ledger total %d, want %d", step, got, k.start-k.burnt)
	}
}

func (k *nftKit) gifts() int64 {
	return k.int("SELECT coalesce(sum(amount), 0) FROM star_ledger l JOIN system_accounts s ON s.user_id = l.user_id WHERE s.kind='gift_upgrades'")
}

func (k *nftKit) issued() int64 { return k.int("SELECT issued FROM collectible_sets WHERE id=$1", k.set) }

func (k *nftKit) fund(p person, n int64, ref string) {
	k.t.Helper()
	if _, _, err := k.s.stars.Adjust(k.ctx, p.id, p.id, n, "starter", ref); err != nil {
		k.t.Fatal(err)
	}
}

func isErr(err error, kind string) bool { return tgerr.Is(err, kind) }

func expectErr(err error, kind, what string) error {
	if !isErr(err, kind) {
		return fmt.Errorf("%s: %v, want %s", what, err, kind)
	}
	return nil
}

// savedGifts is a profile's gifts as viewer sees them.
func savedGifts(ctx context.Context, c *tg.Client, peer tg.InputPeerClass) (*tg.PaymentsSavedStarGifts, error) {
	return c.PaymentsGetSavedStarGifts(ctx, &tg.PaymentsGetSavedStarGiftsRequest{Peer: peer, Limit: 50})
}

// mineByGift is my saved gift of the catalog gift, the newest first.
func mineByGift(ctx context.Context, c *tg.Client, gift int64) (tg.SavedStarGift, error) {
	list, err := savedGifts(ctx, c, &tg.InputPeerSelf{})
	if err != nil {
		return tg.SavedStarGift{}, err
	}
	for _, g := range list.Gifts {
		if sg, ok := g.Gift.(*tg.StarGift); ok && sg.ID == gift {
			return g, nil
		}
	}
	return tg.SavedStarGift{}, fmt.Errorf("no gift %d among %d", gift, len(list.Gifts))
}

func mineUnique(ctx context.Context, c *tg.Client, slug string) (tg.SavedStarGift, *tg.StarGiftUnique, error) {
	list, err := savedGifts(ctx, c, &tg.InputPeerSelf{})
	if err != nil {
		return tg.SavedStarGift{}, nil, err
	}
	for _, g := range list.Gifts {
		if u, ok := g.Gift.(*tg.StarGiftUnique); ok && u.Slug == slug {
			return g, u, nil
		}
	}
	return tg.SavedStarGift{}, nil, fmt.Errorf("no %s among %d", slug, len(list.Gifts))
}

func selfSaved(g tg.SavedStarGift) *tg.InputSavedStarGiftChat {
	return &tg.InputSavedStarGiftChat{Peer: &tg.InputPeerSelf{}, SavedID: g.SavedID}
}

func uniqueOf(up tg.UpdatesClass) (*tg.MessageActionStarGiftUnique, *tg.StarGiftUnique) {
	ups, ok := up.(*tg.Updates)
	if !ok {
		return nil, nil
	}
	for _, u := range ups.Updates {
		if nm, ok := u.(*tg.UpdateNewMessage); ok {
			if svc, ok := nm.Message.(*tg.MessageService); ok {
				if act, ok := svc.Action.(*tg.MessageActionStarGiftUnique); ok {
					return act, act.Gift.(*tg.StarGiftUnique)
				}
			}
		}
	}
	return nil, nil
}

// payResult pays a form and hands back what the reply carried.
func payResult(ctx context.Context, c *tg.Client, inv tg.InputInvoiceClass) (int64, tg.UpdatesClass, error) {
	form, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
	if err != nil {
		return 0, nil, err
	}
	f, ok := form.(*tg.PaymentsPaymentFormStarGift)
	if !ok {
		return 0, nil, fmt.Errorf("form %T", form)
	}
	res, err := c.PaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: f.FormID, Invoice: inv})
	if err != nil {
		return f.Invoice.Prices[0].Amount, nil, err
	}
	return f.Invoice.Prices[0].Amount, res.(*tg.PaymentsPaymentResult).Updates, nil
}

func attrsOf(u *tg.StarGiftUnique) (model *tg.StarGiftAttributeModel, pattern *tg.StarGiftAttributePattern, backdrop *tg.StarGiftAttributeBackdrop, od *tg.StarGiftAttributeOriginalDetails) {
	for _, a := range u.Attributes {
		switch v := a.(type) {
		case *tg.StarGiftAttributeModel:
			model = v
		case *tg.StarGiftAttributePattern:
			pattern = v
		case *tg.StarGiftAttributeBackdrop:
			backdrop = v
		case *tg.StarGiftAttributeOriginalDetails:
			od = v
		}
	}
	return
}

func TestCollectibleGiftUpgrades(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")
	k := newNFTKit(t, s, 3)
	k.fund(alice, 10000, "collectible-1")
	k.fund(bob, 1000, "collectible-2")
	k.begin()

	var bobGift, slug1 string
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		list, err := c.PaymentsGetStarGifts(ctx, 0)
		if err != nil {
			return err
		}
		found := false
		for _, g := range list.(*tg.PaymentsStarGifts).Gifts {
			if sg := g.(*tg.StarGift); sg.ID == k.gift.ID {
				found = sg.UpgradeStars == 50
			}
		}
		if !found {
			return fmt.Errorf("catalog doesn't offer the upgrade")
		}
		pv, err := c.PaymentsGetStarGiftUpgradePreview(ctx, k.gift.ID)
		if err != nil {
			return err
		}
		kinds := map[string]int{}
		for _, a := range pv.SampleAttributes {
			kinds[fmt.Sprintf("%T", a)]++
			if m, ok := a.(*tg.StarGiftAttributeModel); ok {
				if _, ok := m.Document.(*tg.Document); !ok {
					return fmt.Errorf("model without art: %+v", m)
				}
			}
		}
		if kinds["*tg.StarGiftAttributeModel"] != 3 || kinds["*tg.StarGiftAttributePattern"] != 2 || kinds["*tg.StarGiftAttributeBackdrop"] != 2 {
			return fmt.Errorf("preview %v", kinds)
		}
		all, err := c.PaymentsGetStarGiftUpgradeAttributes(ctx, k.gift.ID)
		if err != nil || len(all.Attributes) != 7 {
			return fmt.Errorf("attributes %v %v", all, err)
		}
		// a gift that doesn't upgrade has no preview
		if _, err := c.PaymentsGetStarGiftUpgradePreview(ctx, 1); !isErr(err, "STARGIFT_INVALID") {
			return fmt.Errorf("plain gift preview: %v", err)
		}
		// a plain gift to bob, with a note
		send := &tg.InputInvoiceStarGift{Peer: userPeer(bob.id), GiftID: k.gift.ID, Message: tg.TextWithEntities{Text: "for you"}}
		if _, price, err := pay(ctx, c, send); err != nil || price != 100 {
			return fmt.Errorf("gift: %d %v", price, err)
		}
		return nil
	})
	k.burnt += 100
	k.same("plain gift")

	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		g, err := mineByGift(ctx, c, k.gift.ID)
		if err != nil {
			return err
		}
		if !g.CanUpgrade || g.UpgradeStars != 0 || g.SavedID == 0 {
			return fmt.Errorf("saved gift %+v", g)
		}
		// nobody paid for it, so it isn't free
		if _, err := c.PaymentsUpgradeStarGift(ctx, &tg.PaymentsUpgradeStarGiftRequest{Stargift: selfSaved(g)}); !isErr(err, "PAYMENT_REQUIRED") {
			return fmt.Errorf("free upgrade: %v", err)
		}
		inv := &tg.InputInvoiceStarGiftUpgrade{Stargift: selfSaved(g), KeepOriginalDetails: true}
		price, up, err := payResult(ctx, c, inv)
		if err != nil || price != 50 {
			return fmt.Errorf("upgrade: %d %v", price, err)
		}
		act, u := uniqueOf(up)
		if act == nil || !act.Upgrade || !act.Saved || act.TransferStars != 25 {
			return fmt.Errorf("upgrade message %+v", act)
		}
		if u.Num != 1 || u.Slug != "TestPepe-1" || u.AvailabilityIssued != 1 || u.AvailabilityTotal != 3 || u.GiftID != k.gift.ID || u.Title != "Test Pepe" {
			return fmt.Errorf("collectible %+v", u)
		}
		if p, ok := u.OwnerID.(*tg.PeerUser); !ok || p.UserID != bob.id {
			return fmt.Errorf("owner %v", u.OwnerID)
		}
		model, pattern, backdrop, od := attrsOf(u)
		if model == nil || pattern == nil || backdrop == nil || od == nil {
			return fmt.Errorf("attributes %v", u.Attributes)
		}
		if r, ok := model.Rarity.(*tg.StarGiftAttributeRarity); !ok || r.Permille == 0 {
			return fmt.Errorf("model rarity %+v", model.Rarity)
		}
		if doc, ok := pattern.Document.(*tg.Document); !ok || len(doc.Thumbs) == 0 {
			return fmt.Errorf("pattern has no picture to draw: %+v", pattern.Document)
		}
		if sender, ok := od.SenderID.(*tg.PeerUser); !ok || sender.UserID != alice.id || od.Message.Text != "for you" || od.Date == 0 {
			return fmt.Errorf("original details %+v", od)
		}
		slug1 = u.Slug
		if bal, err := starsBalance(ctx, c); err != nil || bal != 950 {
			return fmt.Errorf("bob's balance %d %v", bal, err)
		}
		// again: the form says it's done, and an old form can't pay twice
		if _, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv}); !isErr(err, "STARGIFT_ALREADY_UPGRADED") {
			return fmt.Errorf("second upgrade form: %v", err)
		}
		// a collectible is worth nothing in Stars
		if _, err := c.PaymentsConvertStarGift(ctx, selfSaved(g)); err == nil {
			return fmt.Errorf("converted a collectible")
		}
		saved, u2, err := mineUnique(ctx, c, slug1)
		if err != nil {
			return err
		}
		if saved.TransferStars != 25 || saved.CanUpgrade || saved.SavedID != g.SavedID || u2.Num != 1 {
			return fmt.Errorf("saved collectible %+v", saved)
		}
		// the upgrade sits in the chat with alice, and the gift message points at it
		m, a, err := serviceAction[*tg.MessageActionStarGiftUnique](ctx, c, alice.id)
		if err != nil || !m.Out || !a.Upgrade {
			return fmt.Errorf("bob's upgrade message %+v %v", m, err)
		}
		_, old, err := serviceAction[*tg.MessageActionStarGift](ctx, c, alice.id)
		if err != nil || !old.Upgraded || old.UpgradeMsgID != m.ID {
			return fmt.Errorf("gift message after upgrade %+v %v", old, err)
		}
		bobGift = slug1
		return nil
	})
	k.same("paid upgrade")
	if k.gifts() != 50 || k.issued() != 1 {
		t.Fatalf("gifts account %d, issued %d", k.gifts(), k.issued())
	}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		m, a, err := serviceAction[*tg.MessageActionStarGiftUnique](ctx, c, bob.id)
		if err != nil || m.Out || !a.Upgrade || a.Gift.(*tg.StarGiftUnique).Slug != bobGift {
			return fmt.Errorf("alice's copy %+v %+v %v", m, a, err)
		}
		// anyone can open its page
		got, err := c.PaymentsGetUniqueStarGift(ctx, slug1)
		if err != nil {
			return err
		}
		u := got.Gift.(*tg.StarGiftUnique)
		if u.Num != 1 || len(got.Users) == 0 {
			return fmt.Errorf("page %+v", got)
		}
		if _, err := c.PaymentsGetUniqueStarGift(ctx, "TestPepe-99"); err == nil {
			return fmt.Errorf("a slug nobody has")
		}
		return nil
	})

	// prepaid: alice pays carol's upgrade with the gift
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		send := &tg.InputInvoiceStarGift{Peer: userPeer(carol.id), GiftID: k.gift.ID, IncludeUpgrade: true, HideName: true, Message: tg.TextWithEntities{Text: "secret"}}
		if _, price, err := pay(ctx, c, send); err != nil || price != 150 {
			return fmt.Errorf("gift with upgrade: %d %v", price, err)
		}
		return nil
	})
	k.burnt += 100
	k.same("prepaid gift")
	if k.gifts() != 100 {
		t.Fatalf("gifts account %d after a prepaid upgrade", k.gifts())
	}
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		g, err := mineByGift(ctx, c, k.gift.ID)
		if err != nil {
			return err
		}
		if !g.CanUpgrade || g.UpgradeStars != 50 {
			return fmt.Errorf("prepaid saved gift %+v", g)
		}
		_, act, err := serviceAction[*tg.MessageActionStarGift](ctx, c, alice.id)
		if err != nil || !act.CanUpgrade || act.UpgradeStars != 50 {
			return fmt.Errorf("prepaid gift message %+v %v", act, err)
		}
		if _, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: &tg.InputInvoiceStarGiftUpgrade{Stargift: selfSaved(g)}}); !isErr(err, "STARGIFT_UPGRADE_PREPAID") {
			return fmt.Errorf("paying a prepaid upgrade again: %v", err)
		}
		up, err := c.PaymentsUpgradeStarGift(ctx, &tg.PaymentsUpgradeStarGiftRequest{Stargift: selfSaved(g), KeepOriginalDetails: true})
		if err != nil {
			return err
		}
		act2, u := uniqueOf(up)
		if act2 == nil || !act2.PrepaidUpgrade || u.Num != 2 {
			return fmt.Errorf("prepaid upgrade %+v %+v", act2, u)
		}
		_, _, _, od := attrsOf(u)
		// alice hid her name: kept details say who got it, not who sent it
		if od == nil || od.SenderID != nil || od.Message.Text != "secret" {
			return fmt.Errorf("hidden sender's details %+v", od)
		}
		if bal, err := starsBalance(ctx, c); err != nil || bal != 0 {
			return fmt.Errorf("carol paid %d %v", bal, err)
		}
		if _, err := c.PaymentsUpgradeStarGift(ctx, &tg.PaymentsUpgradeStarGiftRequest{Stargift: selfSaved(g)}); !isErr(err, "STARGIFT_ALREADY_UPGRADED") {
			return fmt.Errorf("upgrading twice: %v", err)
		}
		return nil
	})
	k.same("prepaid upgrade")
	// alice's copy names carol: Web A reads who turned it from peer
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		_, act, err := serviceAction[*tg.MessageActionStarGiftUnique](ctx, c, carol.id)
		if err != nil {
			return err
		}
		if p, ok := act.Peer.(*tg.PeerUser); !ok || p.UserID != carol.id || act.SavedID == 0 || !act.PrepaidUpgrade || act.Saved {
			return fmt.Errorf("sender's prepaid upgrade message %+v", act)
		}
		return nil
	})

	// supply 3: one more prepaid takes the last number, so nobody can buy it
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		send := &tg.InputInvoiceStarGift{Peer: userPeer(bob.id), GiftID: k.gift.ID, IncludeUpgrade: true}
		if _, _, err := pay(ctx, c, send); err != nil {
			return err
		}
		if _, _, err := pay(ctx, c, send); !isErr(err, "STARGIFT_UPGRADE_UNAVAILABLE") {
			return fmt.Errorf("prepaying past the supply: %v", err)
		}
		// a plain one to herself can't be upgraded with money either
		if _, _, err := pay(ctx, c, &tg.InputInvoiceStarGift{Peer: &tg.InputPeerSelf{}, GiftID: k.gift.ID}); err != nil {
			return err
		}
		g, err := mineByGift(ctx, c, k.gift.ID)
		if err != nil {
			return err
		}
		if g.CanUpgrade {
			return fmt.Errorf("upgradable past the supply %+v", g)
		}
		if _, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: &tg.InputInvoiceStarGiftUpgrade{Stargift: selfSaved(g)}}); !isErr(err, "STARGIFT_USAGE_LIMITED") {
			return fmt.Errorf("upgrade past the supply: %v", err)
		}
		return nil
	})
	k.burnt += 200
	k.same("last number")
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		g, err := mineByGift(ctx, c, k.gift.ID)
		if err != nil {
			return err
		}
		up, err := c.PaymentsUpgradeStarGift(ctx, &tg.PaymentsUpgradeStarGiftRequest{Stargift: selfSaved(g)})
		if err != nil {
			return err
		}
		if _, u := uniqueOf(up); u == nil || u.Num != 3 || u.AvailabilityIssued != 3 {
			return fmt.Errorf("third %+v", u)
		}
		return nil
	})
	k.same("third upgrade")
	if k.issued() != 3 {
		t.Fatalf("issued %d", k.issued())
	}
	nums := k.str("SELECT string_agg(num::text, ',' ORDER BY created_at, num) FROM unique_gifts WHERE set_id=$1", k.set)
	if nums != "1,2,3" {
		t.Fatalf("numbers %s", nums)
	}
	// the admin can't shrink the supply under what's out
	if err := s.stars.SetGiftUpgrade(k.ctx, k.gift.ID, stars.UpgradeSettings{Set: k.set, Stars: 50, TransferStars: 25, Supply: 2}); err == nil {
		t.Fatal("supply below what's issued")
	}
	other, _ := s.stars.AddGift(k.ctx, stars.Gift{Title: "Other", Emoji: "🎈", Stars: 10})
	if err := s.stars.SetGiftUpgrade(k.ctx, other.ID, stars.UpgradeSettings{Set: k.set, Stars: 5, Supply: 10}); err == nil {
		t.Fatal("two gifts upgrade into one collection")
	}
}

func TestCollectibleGiftRefusals(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")
	k := newNFTKit(t, s, 10)
	k.fund(alice, 1000, "collectible-r1")
	k.begin()
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		for _, to := range []int64{bob.id, carol.id} {
			if _, _, err := pay(ctx, c, &tg.InputInvoiceStarGift{Peer: userPeer(to), GiftID: k.gift.ID}); err != nil {
				return err
			}
		}
		return nil
	})
	k.burnt += 200
	var bobSaved int64
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		g, err := mineByGift(ctx, c, k.gift.ID)
		bobSaved = g.SavedID
		return err
	})
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		g, err := mineByGift(ctx, c, k.gift.ID)
		if err != nil {
			return err
		}
		// not hers: bob's gift by his profile, or passed off as her own
		for _, in := range []tg.InputSavedStarGiftClass{
			&tg.InputSavedStarGiftChat{Peer: userPeer(bob.id), SavedID: bobSaved},
			&tg.InputSavedStarGiftChat{Peer: &tg.InputPeerSelf{}, SavedID: bobSaved},
		} {
			if _, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: &tg.InputInvoiceStarGiftUpgrade{Stargift: in}}); err == nil {
				return fmt.Errorf("carol upgrades bob's gift %+v", in)
			}
			if _, err := c.PaymentsUpgradeStarGift(ctx, &tg.PaymentsUpgradeStarGiftRequest{Stargift: in}); err == nil {
				return fmt.Errorf("carol upgrades bob's gift for free %+v", in)
			}
		}
		// carol has no Stars
		_, _, err = payResult(ctx, c, &tg.InputInvoiceStarGiftUpgrade{Stargift: selfSaved(g)})
		return expectErr(err, "BALANCE_TOO_LOW", "upgrade without Stars")
	})
	k.same("refused upgrades")
	if k.issued() != 0 || k.gifts() != 0 {
		t.Fatalf("a refused upgrade left issued %d, gifts %d", k.issued(), k.gifts())
	}
}

func TestCollectibleGiftTransfers(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")
	k := newNFTKit(t, s, 10)
	k.fund(alice, 5000, "collectible-t1")
	k.fund(bob, 500, "collectible-t2")
	k.fund(carol, 500, "collectible-t3")
	k.begin()

	// bob gets two collectibles from alice, prepaid
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		for i := 0; i < 2; i++ {
			if _, _, err := pay(ctx, c, &tg.InputInvoiceStarGift{Peer: userPeer(bob.id), GiftID: k.gift.ID, IncludeUpgrade: true}); err != nil {
				return err
			}
		}
		return nil
	})
	k.burnt += 200
	var slugs []string
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		list, err := savedGifts(ctx, c, &tg.InputPeerSelf{})
		if err != nil {
			return err
		}
		for _, g := range list.Gifts {
			up, err := c.PaymentsUpgradeStarGift(ctx, &tg.PaymentsUpgradeStarGiftRequest{Stargift: selfSaved(g)})
			if err != nil {
				return err
			}
			_, u := uniqueOf(up)
			slugs = append(slugs, u.Slug)
		}
		return nil
	})
	k.same("upgrades")
	if len(slugs) != 2 {
		t.Fatalf("slugs %v", slugs)
	}

	var channel *tg.Channel
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: true, Title: "Carol's gifts", About: ""})
		if err != nil {
			return err
		}
		channel = channelOf(t, res)
		_, err = c.ChannelsUpdateUsername(ctx, &tg.ChannelsUpdateUsernameRequest{Channel: inCh(channel), Username: "carolgifts"})
		return err
	})

	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		saved, _, err := mineUnique(ctx, c, slugs[0])
		if err != nil {
			return err
		}
		// it has a fee, so not for free
		if _, err := c.PaymentsTransferStarGift(ctx, &tg.PaymentsTransferStarGiftRequest{Stargift: selfSaved(saved), ToID: userPeer(carol.id)}); !isErr(err, "PAYMENT_REQUIRED") {
			return fmt.Errorf("free transfer: %v", err)
		}
		// to himself is no transfer
		if _, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: &tg.InputInvoiceStarGiftTransfer{Stargift: selfSaved(saved), ToID: &tg.InputPeerSelf{}}}); err == nil {
			return fmt.Errorf("transfer to himself")
		}
		// a form for carol, then the gift goes to alice, then the old form
		toCarol := &tg.InputInvoiceStarGiftTransfer{Stargift: selfSaved(saved), ToID: userPeer(carol.id)}
		form, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: toCarol})
		if err != nil {
			return err
		}
		price, up, err := payResult(ctx, c, &tg.InputInvoiceStarGiftTransfer{Stargift: selfSaved(saved), ToID: userPeer(alice.id)})
		if err != nil || price != 25 {
			return fmt.Errorf("transfer to alice: %d %v", price, err)
		}
		if act, u := uniqueOf(up); act == nil || !act.Transferred || u.Slug != slugs[0] {
			return fmt.Errorf("transfer message %+v", act)
		}
		before, _ := starsBalance(ctx, c)
		_, err = c.PaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: form.(*tg.PaymentsPaymentFormStarGift).FormID, Invoice: toCarol})
		if err == nil {
			return fmt.Errorf("paid to transfer a gift he no longer has")
		}
		if after, _ := starsBalance(ctx, c); after != before || before != 475 {
			return fmt.Errorf("balance %d then %d", before, after)
		}
		if _, _, err := mineUnique(ctx, c, slugs[0]); err == nil {
			return fmt.Errorf("still on bob's profile")
		}
		// and a new form for it is refused too
		_, err = c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: toCarol})
		return expectErr(err, "SAVED_ID_EMPTY", "form for a gift he gave away")
	})
	k.same("transfer to alice")
	if k.gifts() != 125 {
		t.Fatalf("gifts account %d", k.gifts())
	}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		saved, u, err := mineUnique(ctx, c, slugs[0])
		if err != nil {
			return err
		}
		if p, ok := u.OwnerID.(*tg.PeerUser); !ok || p.UserID != alice.id || saved.Unsaved || saved.MsgID == 0 {
			return fmt.Errorf("alice's new gift %+v %+v", saved, u)
		}
		m, act, err := serviceAction[*tg.MessageActionStarGiftUnique](ctx, c, bob.id)
		if err != nil || m.Out || !act.Transferred || !act.Saved || m.ID != saved.MsgID {
			return fmt.Errorf("incoming transfer %+v %+v %v", m, act, err)
		}
		// on her profile for others too
		return nil
	})
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		list, err := savedGifts(ctx, c, userPeer(alice.id))
		if err != nil {
			return err
		}
		for _, g := range list.Gifts {
			if u, ok := g.Gift.(*tg.StarGiftUnique); ok && u.Slug == slugs[0] {
				if g.SavedID != 0 || g.TransferStars != 0 {
					return fmt.Errorf("carol sees alice's ids: %+v", g)
				}
				return nil
			}
		}
		return fmt.Errorf("not on alice's profile")
	})

	// to carol's channel, then out of it again by its admin
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		saved, _, err := mineUnique(ctx, c, slugs[1])
		if err != nil {
			return err
		}
		_, up, err := payResult(ctx, c, &tg.InputInvoiceStarGiftTransfer{Stargift: selfSaved(saved), ToID: peerCh(channel)})
		if err != nil {
			return err
		}
		if ups := up.(*tg.Updates); len(ups.Chats) == 0 {
			return fmt.Errorf("transfer to a channel names no chat")
		}
		return nil
	})
	k.same("transfer to a channel")
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		// anyone sees it on the channel's profile, without ids
		list, err := savedGifts(ctx, c, peerCh(channel))
		if err != nil {
			return err
		}
		if len(list.Gifts) != 1 || list.Gifts[0].SavedID != 0 || len(list.Chats) == 0 {
			return fmt.Errorf("channel's gifts for alice %+v", list)
		}
		u := list.Gifts[0].Gift.(*tg.StarGiftUnique)
		if p, ok := u.OwnerID.(*tg.PeerChannel); !ok || p.ChannelID != channel.ID {
			return fmt.Errorf("owner %v", u.OwnerID)
		}
		// she can't move the channel's gift
		in := &tg.InputSavedStarGiftChat{Peer: peerCh(channel), SavedID: 1}
		_, err = c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: &tg.InputInvoiceStarGiftTransfer{Stargift: in, ToID: &tg.InputPeerSelf{}}})
		return expectErr(err, "PEER_ID_INVALID", "alice moves the channel's gift")
	})
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		full, err := c.ChannelsGetFullChannel(ctx, inCh(channel))
		if err != nil {
			return err
		}
		if cf := full.FullChat.(*tg.ChannelFull); !cf.StargiftsAvailable || cf.StargiftsCount != 1 {
			return fmt.Errorf("channel full %+v", cf)
		}
		h, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peerCh(channel), Limit: 10})
		if err != nil {
			return err
		}
		found := false
		for _, m := range h.(*tg.MessagesChannelMessages).Messages {
			if svc, ok := m.(*tg.MessageService); ok {
				if act, ok := svc.Action.(*tg.MessageActionStarGiftUnique); ok && act.Transferred && act.SavedID != 0 {
					found = true
				}
			}
		}
		if !found {
			return fmt.Errorf("no transfer message in the channel")
		}
		list, err := savedGifts(ctx, c, peerCh(channel))
		if err != nil {
			return err
		}
		g := list.Gifts[0]
		if g.SavedID == 0 || g.TransferStars != 25 {
			return fmt.Errorf("the admin's view %+v", g)
		}
		in := &tg.InputSavedStarGiftChat{Peer: peerCh(channel), SavedID: g.SavedID}
		if _, _, err := payResult(ctx, c, &tg.InputInvoiceStarGiftTransfer{Stargift: in, ToID: userPeer(bob.id)}); err != nil {
			return err
		}
		if bal, _ := starsBalance(ctx, c); bal != 475 {
			return fmt.Errorf("carol paid %d", 500-bal)
		}
		return nil
	})
	k.same("transfer out of the channel")
	if k.gifts() != 175 {
		t.Fatalf("gifts account %d", k.gifts())
	}
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		_, _, err := mineUnique(ctx, c, slugs[1])
		return err
	})
}

func TestCollectibleGiftsAtOnce(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")
	k := newNFTKit(t, s, 10)
	k.fund(alice, 1000, "collectible-c1")
	k.fund(bob, 1000, "collectible-c2")
	k.begin()
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		_, _, err := pay(ctx, c, &tg.InputInvoiceStarGift{Peer: userPeer(bob.id), GiftID: k.gift.ID})
		return err
	})
	k.burnt += 100

	// two upgrade forms for one gift, paid at the same moment
	var g tg.SavedStarGift
	var forms []int64
	inv := &tg.InputInvoiceStarGiftUpgrade{}
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		var err error
		if g, err = mineByGift(ctx, c, k.gift.ID); err != nil {
			return err
		}
		inv.Stargift = selfSaved(g)
		for i := 0; i < 2; i++ {
			f, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
			if err != nil {
				return err
			}
			forms = append(forms, f.(*tg.PaymentsPaymentFormStarGift).FormID)
		}
		return nil
	})
	race := func(n int, call func(i int, ctx context.Context, c *tg.Client) error) []error {
		errs := make([]error, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cl := s.client(bob.storage)
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				errs[i] = cl.Run(ctx, func(ctx context.Context) error {
					api := cl.API()
					if _, err := api.UpdatesGetState(ctx); err != nil {
						return err
					}
					<-start
					return call(i, ctx, api)
				})
			}()
		}
		time.Sleep(2 * time.Second)
		close(start)
		wg.Wait()
		return errs
	}
	errs := race(2, func(i int, ctx context.Context, c *tg.Client) error {
		_, err := c.PaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: forms[i], Invoice: inv})
		return err
	})
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("two upgrades at once: %v / %v", errs[0], errs[1])
	}
	k.same("upgrades at once")
	if k.issued() != 1 || k.gifts() != 50 || k.int("SELECT count(*) FROM unique_gifts") != 1 {
		t.Fatalf("issued %d, gifts %d", k.issued(), k.gifts())
	}
	if bal := k.int("SELECT sum(amount) FROM star_ledger WHERE user_id=$1", bob.id); bal != 950 {
		t.Fatalf("bob paid %d", 1000-bal)
	}

	// and two transfers of it, to alice and to carol
	var tinv [2]*tg.InputInvoiceStarGiftTransfer
	forms = forms[:0]
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		for i, to := range []int64{alice.id, carol.id} {
			tinv[i] = &tg.InputInvoiceStarGiftTransfer{Stargift: selfSaved(g), ToID: userPeer(to)}
			f, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: tinv[i]})
			if err != nil {
				return err
			}
			forms = append(forms, f.(*tg.PaymentsPaymentFormStarGift).FormID)
		}
		return nil
	})
	errs = race(2, func(i int, ctx context.Context, c *tg.Client) error {
		_, err := c.PaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: forms[i], Invoice: tinv[i]})
		return err
	})
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("two transfers at once: %v / %v", errs[0], errs[1])
	}
	k.same("transfers at once")
	if k.gifts() != 75 {
		t.Fatalf("gifts account %d", k.gifts())
	}
	owner := k.int("SELECT owner_id FROM gift_receipts WHERE unique_id IS NOT NULL")
	if want := []int64{alice.id, carol.id}[map[bool]int{true: 0, false: 1}[errs[0] == nil]]; owner != want {
		t.Fatalf("owner %d, want %d", owner, want)
	}
}

func TestCollectibleEmojiStatus(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	k := newNFTKit(t, s, 10)
	k.fund(alice, 1000, "collectible-e1")
	if _, err := s.db.Pool.Exec(k.ctx, "UPDATE users SET premium_until=$2 WHERE id=ANY($1)", []int64{alice.id, bob.id}, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	var id int64
	var slug string
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if _, _, err := pay(ctx, c, &tg.InputInvoiceStarGift{Peer: &tg.InputPeerSelf{}, GiftID: k.gift.ID, IncludeUpgrade: true}); err != nil {
			return err
		}
		g, err := mineByGift(ctx, c, k.gift.ID)
		if err != nil {
			return err
		}
		up, err := c.PaymentsUpgradeStarGift(ctx, &tg.PaymentsUpgradeStarGiftRequest{Stargift: selfSaved(g)})
		if err != nil {
			return err
		}
		_, u := uniqueOf(up)
		id, slug = u.ID, u.Slug
		list, err := c.AccountGetCollectibleEmojiStatuses(ctx, 0)
		if err != nil {
			return err
		}
		sts := list.(*tg.AccountEmojiStatuses).Statuses
		if len(sts) != 1 || sts[0].(*tg.EmojiStatusCollectible).CollectibleID != id || sts[0].(*tg.EmojiStatusCollectible).Slug != slug {
			return fmt.Errorf("collectible statuses %+v", sts)
		}
		doc := sts[0].(*tg.EmojiStatusCollectible).DocumentID
		docs, err := c.MessagesGetCustomEmojiDocuments(ctx, []int64{doc})
		if err != nil || len(docs) != 1 {
			return fmt.Errorf("status emoji %v %v", docs, err)
		}
		if _, err := c.AccountUpdateEmojiStatus(ctx, &tg.InputEmojiStatusCollectible{CollectibleID: id}); err != nil {
			return err
		}
		us, err := c.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
		if err != nil {
			return err
		}
		if st, ok := us[0].(*tg.User).EmojiStatus.(*tg.EmojiStatusCollectible); !ok || st.CollectibleID != id || !strings.HasSuffix(st.Title, "#1") {
			return fmt.Errorf("status %+v", us[0].(*tg.User).EmojiStatus)
		}
		return nil
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.AccountUpdateEmojiStatus(ctx, &tg.InputEmojiStatusCollectible{CollectibleID: id})
		return expectErr(err, "COLLECTIBLE_INVALID", "wearing alice's gift")
	})
	// given away, it comes off
	if err := s.stars.SetGiftUpgrade(k.ctx, k.gift.ID, stars.UpgradeSettings{Set: k.set, Stars: 50, Supply: 10}); err != nil {
		t.Fatal(err)
	}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		saved, _, err := mineUnique(ctx, c, slug)
		if err != nil {
			return err
		}
		if _, err := c.PaymentsTransferStarGift(ctx, &tg.PaymentsTransferStarGiftRequest{Stargift: selfSaved(saved), ToID: userPeer(bob.id)}); err != nil {
			return err
		}
		us, err := c.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
		if err != nil {
			return err
		}
		if st := us[0].(*tg.User).EmojiStatus; st != nil {
			if _, ok := st.(*tg.EmojiStatusCollectible); ok {
				return fmt.Errorf("still wearing a gift she gave away")
			}
		}
		return nil
	})
}

func TestAdminCollectibles(t *testing.T) {
	s := start(t)
	boss := s.browser(t)
	boss.must("/api/register", map[string]string{"username": "boss", "name": "Boss", "password": "correct horse boss"})
	adm := admin.New(s.db.Pool, s.accounts, s.settings, channels.New(s.db.Pool))
	adm.Gifts = s.stars
	adm.Media = s.media
	adm.Register(s.mux)
	if _, err := s.media.ImportCollectibles(context.Background(), testCollection(t, "testpepe", "Test Pepe")); err != nil {
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
	code, out := call("GET", "/api/admin/collectibles", nil)
	if code != 200 {
		t.Fatalf("collections: %d %v", code, out)
	}
	sets := out["collections"].([]any)
	if len(sets) != 1 {
		t.Fatalf("collections %v", sets)
	}
	set := sets[0].(map[string]any)
	if set["title"] != "Test Pepe" || set["models"] != 3.0 || set["patterns"] != 2.0 || set["backdrops"] != 2.0 || set["originalId"] == nil || len(set["preview"].([]any)) != 3 {
		t.Fatalf("collection %v", set)
	}
	// the original art is a sticker the panel can draw
	if code, _ := call("GET", "/api/sticker-media/"+set["originalId"].(string), nil); code != 200 {
		t.Fatalf("original art: %d", code)
	}
	setID := set["id"].(float64)
	for name, bad := range map[string]map[string]any{
		"no collection": {"title": "Pepe", "emoji": "🐸", "stars": 100, "upgrade": map[string]any{"stars": 50, "supply": 10}},
		"free upgrade":  {"title": "Pepe", "emoji": "🐸", "stars": 100, "upgrade": map[string]any{"setId": setID, "stars": 0, "supply": 10}},
		"no supply":     {"title": "Pepe", "emoji": "🐸", "stars": 100, "upgrade": map[string]any{"setId": setID, "stars": 50}},
		"no such set":   {"title": "Pepe", "emoji": "🐸", "stars": 100, "upgrade": map[string]any{"setId": 9999, "stars": 50, "supply": 10}},
		"negative fee":  {"title": "Pepe", "emoji": "🐸", "stars": 100, "upgrade": map[string]any{"setId": setID, "stars": 50, "supply": 10, "transferStars": -1}},
	} {
		if code, out := call("POST", "/api/admin/gifts", bad); code != 400 && code != 404 {
			t.Fatalf("%s: %d %v", name, code, out)
		}
	}
	// a refused upgrade doesn't leave the gift behind
	if n := len(func() []any { _, out := call("GET", "/api/admin/gifts", nil); return out["gifts"].([]any) }()); n != 38 {
		t.Fatalf("catalog of %d after refusals", n)
	}
	code, pepe := call("POST", "/api/admin/gifts", map[string]any{"title": "Pepe", "emoji": "🐸", "stars": 100,
		"upgrade": map[string]any{"setId": setID, "stars": 50, "supply": 10, "transferStars": 25}})
	up, _ := pepe["upgrade"].(map[string]any)
	if code != 200 || up == nil || up["stars"] != 50.0 || up["transferStars"] != 25.0 || up["supply"] != 10.0 || up["issued"] != 0.0 || up["title"] != "Test Pepe" {
		t.Fatalf("upgradable gift: %d %v", code, pepe)
	}
	id := strconv.Itoa(int(pepe["id"].(float64)))
	// a second gift can't take the same collection
	if code, out := call("POST", "/api/admin/gifts", map[string]any{"title": "Pepe 2", "emoji": "🐸", "stars": 100,
		"upgrade": map[string]any{"setId": setID, "stars": 50, "supply": 10}}); code != 400 {
		t.Fatalf("second gift on one collection: %d %v", code, out)
	}
	code, edited := call("PATCH", "/api/admin/gifts/"+id, map[string]any{"upgrade": map[string]any{"setId": setID, "stars": 75, "supply": 20}})
	if up, _ := edited["upgrade"].(map[string]any); code != 200 || up["stars"] != 75.0 || up["supply"] != 20.0 || up["transferStars"] != 25.0 {
		t.Fatalf("edit: %d %v", code, edited)
	}
	// editing something else keeps the upgrade
	code, edited = call("PATCH", "/api/admin/gifts/"+id, map[string]any{"stars": 120})
	if up, _ := edited["upgrade"].(map[string]any); code != 200 || up == nil || up["stars"] != 75.0 {
		t.Fatalf("edit price: %d %v", code, edited)
	}
	code, edited = call("PATCH", "/api/admin/gifts/"+id, map[string]any{"upgrade": nil})
	if code != 200 || edited["upgrade"] != nil {
		t.Fatalf("plain again: %d %v", code, edited)
	}
	_, out = call("GET", "/api/admin/collectibles", nil)
	if g := out["collections"].([]any)[0].(map[string]any)["giftId"]; g != nil {
		t.Fatalf("collection still taken by %v", g)
	}
}
