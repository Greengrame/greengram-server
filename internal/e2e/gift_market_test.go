package e2e

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/admin"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/stars"
)

// market is the resale screen as a test reads it.
type market struct {
	k    *nftKit
	gift int64
}

func (m market) page(ctx context.Context, c *tg.Client, req tg.PaymentsGetResaleStarGiftsRequest) (*tg.PaymentsResaleStarGifts, error) {
	req.GiftID = m.gift
	if req.Limit == 0 {
		req.Limit = 50
	}
	if req.Attributes == nil {
		req.Attributes = []tg.StarGiftAttributeIDClass{}
	}
	return c.PaymentsGetResaleStarGifts(ctx, &req)
}

// all walks every page, five at a time.
func (m market) all(ctx context.Context, c *tg.Client, req tg.PaymentsGetResaleStarGiftsRequest) ([]*tg.StarGiftUnique, int, error) {
	var out []*tg.StarGiftUnique
	req.Limit = 5
	count := -1
	for i := 0; i < 100; i++ {
		p, err := m.page(ctx, c, req)
		if err != nil {
			return nil, 0, err
		}
		if count < 0 {
			count = p.Count
		}
		for _, g := range p.Gifts {
			out = append(out, g.(*tg.StarGiftUnique))
		}
		next, ok := p.GetNextOffset()
		if !ok {
			return out, count, nil
		}
		req.Offset = next
	}
	return nil, 0, fmt.Errorf("pages never end")
}

func priceOf(u *tg.StarGiftUnique) int64 {
	if len(u.ResellAmount) != 1 {
		return -1
	}
	a, ok := u.ResellAmount[0].(*tg.StarsAmount)
	if !ok || a.Nanos != 0 {
		return -1
	}
	return a.Amount
}

// counters by attribute: "m:<doc>", "p:<doc>", "b:<id>"
func counters(p *tg.PaymentsResaleStarGifts) map[string]int {
	out := map[string]int{}
	for _, c := range p.Counters {
		switch v := c.Attribute.(type) {
		case *tg.StarGiftAttributeIDModel:
			out["m:"+strconv.FormatInt(v.DocumentID, 10)] = c.Count
		case *tg.StarGiftAttributeIDPattern:
			out["p:"+strconv.FormatInt(v.DocumentID, 10)] = c.Count
		case *tg.StarGiftAttributeIDBackdrop:
			out["b:"+strconv.Itoa(v.BackdropID)] = c.Count
		}
	}
	return out
}

// docs are the collection's attribute documents by name.
func (k *nftKit) docs() map[string]int64 {
	out := map[string]int64{}
	rows, err := k.s.db.Pool.Query(k.ctx, `SELECT name, document_id FROM collectible_models WHERE set_id=$1
		UNION ALL SELECT name, document_id FROM collectible_patterns WHERE set_id=$1`, k.set)
	if err != nil {
		k.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		var id int64
		rows.Scan(&n, &id)
		out[n] = id
	}
	return out
}

func resaleInv(slug string, to tg.InputPeerClass) *tg.InputInvoiceStarGiftResale {
	return &tg.InputInvoiceStarGiftResale{Slug: slug, ToID: to}
}

func formFor(ctx context.Context, c *tg.Client, inv tg.InputInvoiceClass) (int64, int64, error) {
	f, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
	if err != nil {
		return 0, 0, err
	}
	sf, ok := f.(*tg.PaymentsPaymentFormStarGift)
	if !ok || len(sf.Invoice.Prices) != 1 || sf.Invoice.Currency != "XTR" {
		return 0, 0, fmt.Errorf("form %+v", f)
	}
	return sf.FormID, sf.Invoice.Prices[0].Amount, nil
}

func sendForm(ctx context.Context, c *tg.Client, id int64, inv tg.InputInvoiceClass) (tg.UpdatesClass, error) {
	res, err := c.PaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: id, Invoice: inv})
	if err != nil {
		return nil, err
	}
	return res.(*tg.PaymentsPaymentResult).Updates, nil
}

// raceForms pays forms[i] as people[i], all at the same moment.
func raceForms(s *stack, people []person, call func(i int, ctx context.Context, c *tg.Client) error) []error {
	errs := make([]error, len(people))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range people {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cl := s.client(people[i].storage)
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

func (k *nftKit) balance(p person) int64 {
	return k.int("SELECT coalesce(sum(amount), 0) FROM star_ledger WHERE user_id=$1", p.id)
}

func onlyOne(errs []error) error {
	ok := 0
	for _, e := range errs {
		if e == nil {
			ok++
		}
	}
	if ok != 1 {
		return fmt.Errorf("%d of %d went through: %v", ok, len(errs), errs)
	}
	return nil
}

// TestGiftMarketOffers: a gift sold as variants offers every combination
// nobody holds, filtered, counted and sorted the way Web A asks; buying one
// mints it with the next number, at its price, into the gifts account.
func TestGiftMarketOffers(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")
	k := newNFTKit(t, s, 0)
	m := market{k: k, gift: k.gift.ID}
	if err := s.stars.SetMarket(k.ctx, k.gift.ID, stars.Market{Enabled: true, Base: 200, RarityX100: 300}); err != nil {
		t.Fatal(err)
	}
	k.fund(alice, 10000, "market-1")
	k.fund(bob, 10000, "market-2")
	k.begin()
	doc := k.docs()
	var cheapest, priciest *tg.StarGiftUnique

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		// the shop: sold out as itself, on resale from the cheapest combination
		list, err := c.PaymentsGetStarGifts(ctx, 0)
		if err != nil {
			return err
		}
		var g *tg.StarGift
		for _, x := range list.(*tg.PaymentsStarGifts).Gifts {
			if sg := x.(*tg.StarGift); sg.ID == k.gift.ID {
				g = sg
			}
		}
		if g == nil || !g.SoldOut || !g.Limited || g.AvailabilityResale != 12 || g.ResellMinStars != 200 {
			return fmt.Errorf("shop tile %+v", g)
		}
		if _, _, err := pay(ctx, c, &tg.InputInvoiceStarGift{Peer: userPeer(bob.id), GiftID: k.gift.ID}); !isErr(err, "STARGIFT_USAGE_LIMITED") {
			return fmt.Errorf("plain gift sold as variants: %v", err)
		}

		// the first page: attributes, a hash, counters for every attribute
		p, err := m.page(ctx, c, tg.PaymentsGetResaleStarGiftsRequest{})
		if err != nil {
			return err
		}
		hash, ok := p.GetAttributesHash()
		if p.Count != 12 || len(p.Gifts) != 12 || !ok || hash == 0 || len(p.Attributes) != 7 {
			return fmt.Errorf("first page: count %d, %d gifts, hash %d, %d attributes", p.Count, len(p.Gifts), hash, len(p.Attributes))
		}
		cnt := counters(p)
		for name, want := range map[string]int{"m:" + fmt.Sprint(doc["Rare"]): 4, "p:" + fmt.Sprint(doc["Moon"]): 6, "b:9": 6, "b:7": 6} {
			if cnt[name] != want {
				return fmt.Errorf("counter %s %d, want %d (%v)", name, cnt[name], want, cnt)
			}
		}
		// the same hash: no attributes again, counters always
		p2, err := m.page(ctx, c, tg.PaymentsGetResaleStarGiftsRequest{AttributesHash: hash})
		if err != nil {
			return err
		}
		if len(p2.Attributes) != 0 || len(p2.Counters) != 7 {
			return fmt.Errorf("same hash: %d attributes, %d counters", len(p2.Attributes), len(p2.Counters))
		}

		// by date (no sort): a page's order holds while it's paged
		first, err := m.page(ctx, c, tg.PaymentsGetResaleStarGiftsRequest{Limit: 5})
		if err != nil {
			return err
		}
		next, _ := first.GetNextOffset()
		a, err := m.page(ctx, c, tg.PaymentsGetResaleStarGiftsRequest{Limit: 5, Offset: next})
		if err != nil {
			return err
		}
		b, err := m.page(ctx, c, tg.PaymentsGetResaleStarGiftsRequest{Limit: 5, Offset: next})
		if err != nil {
			return err
		}
		for i := range a.Gifts {
			if a.Gifts[i].(*tg.StarGiftUnique).Slug != b.Gifts[i].(*tg.StarGiftUnique).Slug || a.Gifts[i].(*tg.StarGiftUnique).ID != b.Gifts[i].(*tg.StarGiftUnique).ID {
				return fmt.Errorf("page 2 differs between loads")
			}
		}
		all, count, err := m.all(ctx, c, tg.PaymentsGetResaleStarGiftsRequest{})
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, u := range all {
			mo, pa, bd, od := attrsOf(u)
			if u.Num != 0 || u.OwnerID != nil || od != nil || mo == nil || pa == nil || bd == nil || priceOf(u) < 200 || priceOf(u) > 600 {
				return fmt.Errorf("offer %+v", u)
			}
			seen[mo.Name+"/"+pa.Name+"/"+bd.Name] = true
		}
		if count != 12 || len(all) != 12 || len(seen) != 12 {
			return fmt.Errorf("by date: count %d, %d offers, %d combinations", count, len(all), len(seen))
		}

		// by price: cheapest first, the most common combination at the
		// base, the rarest at three times it
		all, _, err = m.all(ctx, c, tg.PaymentsGetResaleStarGiftsRequest{SortByPrice: true})
		if err != nil {
			return err
		}
		if len(all) != 12 || !sort.SliceIsSorted(all, func(i, j int) bool { return priceOf(all[i]) < priceOf(all[j]) }) {
			return fmt.Errorf("by price: %d offers, not in order", len(all))
		}
		cheapest, priciest = all[0], all[11]
		cm, cp, cb, _ := attrsOf(cheapest)
		pm, pp, pb, _ := attrsOf(priciest)
		if priceOf(cheapest) != 200 || cm.Name != "Common" || cp.Name != "Star" || cb.Name != "Onyx Black" ||
			priceOf(priciest) != 600 || pm.Name != "Rare" || pp.Name != "Moon" || pb.Name != "Pure Gold" {
			return fmt.Errorf("cheapest %s/%s/%s %d, priciest %s/%s/%s %d", cm.Name, cp.Name, cb.Name, priceOf(cheapest), pm.Name, pp.Name, pb.Name, priceOf(priciest))
		}

		// filters: one model, one backdrop, any symbol
		f, err := m.page(ctx, c, tg.PaymentsGetResaleStarGiftsRequest{Attributes: []tg.StarGiftAttributeIDClass{
			&tg.StarGiftAttributeIDModel{DocumentID: doc["Rare"]}, &tg.StarGiftAttributeIDBackdrop{BackdropID: 9}}})
		if err != nil {
			return err
		}
		if f.Count != 2 || len(f.Gifts) != 2 {
			return fmt.Errorf("filtered: %d of %d", len(f.Gifts), f.Count)
		}
		for _, g := range f.Gifts {
			mo, _, bd, _ := attrsOf(g.(*tg.StarGiftUnique))
			if mo.Name != "Rare" || bd.Name != "Pure Gold" {
				return fmt.Errorf("filter let through %s/%s", mo.Name, bd.Name)
			}
		}
		if len(counters(f)) != 7 || counters(f)["m:"+fmt.Sprint(doc["Common"])] != 4 {
			return fmt.Errorf("a filter narrowed the counters: %v", counters(f))
		}
		// an offer opens like a collectible
		got, err := c.PaymentsGetUniqueStarGift(ctx, priciest.Slug)
		if err != nil || got.Gift.(*tg.StarGiftUnique).ID != priciest.ID || priceOf(got.Gift.(*tg.StarGiftUnique)) != 600 {
			return fmt.Errorf("offer by slug: %v", err)
		}

		// buying the rarest for herself: number 1, those attributes, 600
		inv := resaleInv(priciest.Slug, &tg.InputPeerSelf{})
		price, up, err := payResult(ctx, c, inv)
		if err != nil {
			return err
		}
		act, u := uniqueOf(up)
		if price != 600 || act == nil || u == nil || u.Num != 1 || u.Slug != "TestPepe-1" || len(u.ResellAmount) != 0 {
			return fmt.Errorf("bought %d: %+v %+v", price, act, u)
		}
		if amt, ok := act.ResaleAmount.(*tg.StarsAmount); !ok || amt.Amount != 600 || !act.Saved {
			return fmt.Errorf("service message %+v", act)
		}
		um, up2, ub, _ := attrsOf(u)
		if um.Name != "Rare" || up2.Name != "Moon" || ub.Name != "Pure Gold" {
			return fmt.Errorf("minted %s/%s/%s", um.Name, up2.Name, ub.Name)
		}
		if _, _, err := mineUnique(ctx, c, "TestPepe-1"); err != nil {
			return err
		}
		// gone from the offers
		if _, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv}); !isErr(err, "SLUG_INVALID") {
			return fmt.Errorf("the same combination again: %v", err)
		}
		p, err = m.page(ctx, c, tg.PaymentsGetResaleStarGiftsRequest{})
		if err != nil {
			return err
		}
		if p.Count != 11 || counters(p)["m:"+fmt.Sprint(doc["Rare"])] != 3 {
			return fmt.Errorf("after a sale: count %d, rare %d", p.Count, counters(p)["m:"+fmt.Sprint(doc["Rare"])])
		}
		// the transaction is a resale purchase
		st, err := c.PaymentsGetStarsStatus(ctx, &tg.PaymentsGetStarsStatusRequest{Peer: &tg.InputPeerSelf{}})
		if err != nil {
			return err
		}
		if h := st.History; len(h) == 0 || !h[0].StargiftResale || h[0].Amount.(*tg.StarsAmount).Amount != -600 {
			return fmt.Errorf("history %+v", h)
		}
		return nil
	})
	k.same("rarest bought")
	if k.gifts() != 600 || k.balance(alice) != 9400 || k.issued() != 1 {
		t.Fatalf("gifts %d, alice %d, issued %d", k.gifts(), k.balance(alice), k.issued())
	}

	// for someone else, with a note: theirs, with who gave it
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		inv := &tg.InputInvoiceStarGiftResale{Slug: cheapest.Slug, ToID: userPeer(bob.id), ShowName: true,
			Message: tg.TextWithEntities{Text: "for you", Entities: []tg.MessageEntityClass{}}}
		price, _, err := payResult(ctx, c, inv)
		if err != nil || price != 200 {
			return fmt.Errorf("gift to bob %d: %v", price, err)
		}
		return nil
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		_, u, err := mineUnique(ctx, c, "TestPepe-2")
		if err != nil {
			return err
		}
		if _, _, _, od := attrsOf(u); od == nil || od.SenderID == nil || od.Message.Text != "for you" {
			return fmt.Errorf("original details %+v", od)
		}
		return nil
	})
	k.same("gift to bob")

	// price changed since the form, not enough Stars, a replay
	var offer *tg.StarGiftUnique
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		p, err := m.page(ctx, c, tg.PaymentsGetResaleStarGiftsRequest{SortByPrice: true, Limit: 1})
		if err != nil {
			return err
		}
		offer = p.Gifts[0].(*tg.StarGiftUnique)
		inv := resaleInv(offer.Slug, &tg.InputPeerSelf{})
		id, price, err := formFor(ctx, c, inv)
		if err != nil || price != priceOf(offer) {
			return fmt.Errorf("form %d vs %d: %v", price, priceOf(offer), err)
		}
		if err := s.stars.SetMarket(ctx, k.gift.ID, stars.Market{Enabled: true, Base: 250, RarityX100: 300}); err != nil {
			return err
		}
		if _, err := sendForm(ctx, c, id, inv); !isErr(err, "STARGIFT_RESELL_PRICE_CHANGED") {
			return fmt.Errorf("price changed: %v", err)
		}
		if id, _, err = formFor(ctx, c, inv); err != nil {
			return err
		}
		if _, err := sendForm(ctx, c, id, inv); err != nil {
			return err
		}
		if _, err := sendForm(ctx, c, id, inv); !isErr(err, "FORM_SUBMIT_DUPLICATE") {
			return fmt.Errorf("replay: %v", err)
		}
		return nil
	})
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		p, err := m.page(ctx, c, tg.PaymentsGetResaleStarGiftsRequest{SortByPrice: true, Limit: 1})
		if err != nil {
			return err
		}
		inv := resaleInv(p.Gifts[0].(*tg.StarGiftUnique).Slug, &tg.InputPeerSelf{})
		id, _, err := formFor(ctx, c, inv)
		if err != nil {
			return err
		}
		_, err = sendForm(ctx, c, id, inv)
		return expectErr(err, "BALANCE_TOO_LOW", "carol with no Stars")
	})
	k.same("refusals")
	if k.issued() != 3 || k.int("SELECT count(*) FROM unique_gifts") != 3 {
		t.Fatalf("issued %d", k.issued())
	}

	// two buyers, one combination
	var slug string
	forms := make([]int64, 2)
	invs := make([]*tg.InputInvoiceStarGiftResale, 2)
	for i, p := range []person{alice, bob} {
		run(t, s.client(p.storage), func(ctx context.Context, c *tg.Client) error {
			if slug == "" {
				pg, err := m.page(ctx, c, tg.PaymentsGetResaleStarGiftsRequest{Limit: 1})
				if err != nil {
					return err
				}
				slug = pg.Gifts[0].(*tg.StarGiftUnique).Slug
			}
			invs[i] = resaleInv(slug, &tg.InputPeerSelf{})
			var err error
			forms[i], _, err = formFor(ctx, c, invs[i])
			return err
		})
	}
	before := k.gifts()
	errs := raceForms(s, []person{alice, bob}, func(i int, ctx context.Context, c *tg.Client) error {
		_, err := sendForm(ctx, c, forms[i], invs[i])
		return err
	})
	if err := onlyOne(errs); err != nil {
		t.Fatal(err)
	}
	for _, e := range errs {
		if e != nil && !isErr(e, "SLUG_INVALID") {
			t.Fatalf("the loser got %v", e)
		}
	}
	k.same("two buyers")
	if k.issued() != 4 || k.int("SELECT count(*) FROM unique_gifts") != 4 || k.gifts() == before {
		t.Fatalf("issued %d", k.issued())
	}

	// a supply limit: one left, then none, and the shop says so
	if err := s.stars.SetGiftUpgrade(k.ctx, k.gift.ID, stars.UpgradeSettings{Set: k.set, Stars: 50, TransferStars: 25, Supply: 5}); err != nil {
		t.Fatal(err)
	}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		p, err := m.page(ctx, c, tg.PaymentsGetResaleStarGiftsRequest{})
		if err != nil {
			return err
		}
		if p.Count != 1 || len(p.Gifts) != 1 {
			return fmt.Errorf("one left: count %d, %d gifts", p.Count, len(p.Gifts))
		}
		one := p.Gifts[0].(*tg.StarGiftUnique)
		_, up, err := payResult(ctx, c, resaleInv(one.Slug, &tg.InputPeerSelf{}))
		if err != nil {
			return err
		}
		if _, u := uniqueOf(up); u == nil || u.Num != 5 || u.AvailabilityTotal != 5 {
			return fmt.Errorf("last number %+v", u)
		}
		p, err = m.page(ctx, c, tg.PaymentsGetResaleStarGiftsRequest{})
		if err != nil || p.Count != 0 || len(p.Gifts) != 0 {
			return fmt.Errorf("sold out: %v %+v", err, p)
		}
		list, err := c.PaymentsGetStarGifts(ctx, 0)
		if err != nil {
			return err
		}
		for _, x := range list.(*tg.PaymentsStarGifts).Gifts {
			if sg := x.(*tg.StarGift); sg.ID == k.gift.ID && (sg.AvailabilityResale != 0 || !sg.SoldOut) {
				return fmt.Errorf("sold out tile %+v", sg)
			}
		}
		return nil
	})
	k.same("supply")
	nums := k.str("SELECT string_agg(num::text, ',' ORDER BY created_at, num) FROM unique_gifts WHERE set_id=$1", k.set)
	if nums != "1,2,3,4,5" {
		t.Fatalf("numbers %s", nums)
	}
}

// TestGiftMarketResale: owners list, change and take off their
// collectibles; a buyer pays the price, the seller gets it less the
// commission, the commission goes to the gifts account.
func TestGiftMarketResale(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")
	dave := s.signUp(t, "dave")
	k := newNFTKit(t, s, 0)
	m := market{k: k, gift: k.gift.ID}
	k.fund(alice, 1000, "resale-1")
	k.fund(bob, 5000, "resale-2")
	k.fund(carol, 5000, "resale-3")
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		_, _, err := pay(ctx, c, &tg.InputInvoiceStarGift{Peer: &tg.InputPeerSelf{}, GiftID: k.gift.ID, IncludeUpgrade: true})
		return err
	})
	if err := s.stars.SetCommission(k.ctx, 150); err != nil {
		t.Fatal(err)
	}
	k.begin()
	slug := "TestPepe-1"
	price := func(n int64) *tg.StarsAmount { return &tg.StarsAmount{Amount: n} }
	bySlug := &tg.InputSavedStarGiftSlug{Slug: slug}

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		g, err := mineByGift(ctx, c, k.gift.ID)
		if err != nil {
			return err
		}
		if _, err := c.PaymentsUpgradeStarGift(ctx, &tg.PaymentsUpgradeStarGiftRequest{Stargift: selfSaved(g)}); err != nil {
			return err
		}
		// she wears it, then lists it: the status comes off
		if ok, err := s.accounts.SetCollectibleStatus(ctx, alice.id, k.int("SELECT id FROM unique_gifts WHERE slug=$1", slug), 0); err != nil || !ok {
			return fmt.Errorf("wear: %v %v", ok, err)
		}
		for _, bad := range []tg.StarsAmountClass{price(5), price(stars.ResaleMaxStars + 1), &tg.StarsTonAmount{Amount: 1e9}} {
			if _, err := c.PaymentsUpdateStarGiftPrice(ctx, &tg.PaymentsUpdateStarGiftPriceRequest{Stargift: bySlug, ResellAmount: bad}); err == nil {
				return fmt.Errorf("listed at %+v", bad)
			}
		}
		if _, err := c.PaymentsUpdateStarGiftPrice(ctx, &tg.PaymentsUpdateStarGiftPriceRequest{Stargift: bySlug, ResellAmount: price(500)}); err != nil {
			return err
		}
		if n := k.int("SELECT count(*) FROM users WHERE id=$1 AND emoji_status_collectible IS NOT NULL", alice.id); n != 0 {
			return fmt.Errorf("still wearing a listed gift")
		}
		if ok, _ := s.accounts.SetCollectibleStatus(ctx, alice.id, k.int("SELECT id FROM unique_gifts WHERE slug=$1", slug), 0); ok {
			return fmt.Errorf("wore a listed gift")
		}
		sg, u, err := mineUnique(ctx, c, slug)
		if err != nil {
			return err
		}
		if priceOf(u) != 500 || sg.CanResellAt == 0 {
			return fmt.Errorf("listed as %+v / %+v", u.ResellAmount, sg)
		}
		_, err = c.PaymentsTransferStarGift(ctx, &tg.PaymentsTransferStarGiftRequest{Stargift: bySlug, ToID: userPeer(bob.id)})
		if !isErr(err, "STARGIFT_RESELL_LISTED") {
			_, err = c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: &tg.InputInvoiceStarGiftTransfer{Stargift: bySlug, ToID: userPeer(bob.id)}})
			if !isErr(err, "STARGIFT_RESELL_LISTED") {
				return fmt.Errorf("transferring a listed gift: %v", err)
			}
		}
		// her own
		_, err = c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: resaleInv(slug, &tg.InputPeerSelf{})})
		return expectErr(err, "STARGIFT_OWNER_INVALID", "buying her own")
	})

	// what the screen and the shop show: a listing with its owner
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		p, err := m.page(ctx, c, tg.PaymentsGetResaleStarGiftsRequest{})
		if err != nil {
			return err
		}
		if p.Count != 1 || len(p.Gifts) != 1 || len(p.Users) == 0 {
			return fmt.Errorf("listing page %+v", p)
		}
		u := p.Gifts[0].(*tg.StarGiftUnique)
		if owner, ok := u.OwnerID.(*tg.PeerUser); !ok || owner.UserID != alice.id || priceOf(u) != 500 || u.Num != 1 {
			return fmt.Errorf("listing %+v", u)
		}
		mo, _, _, _ := attrsOf(u)
		if counters(p)["m:"+fmt.Sprint(mo.Document.GetID())] != 1 {
			return fmt.Errorf("counters %v", counters(p))
		}
		list, err := c.PaymentsGetStarGifts(ctx, 0)
		if err != nil {
			return err
		}
		for _, x := range list.(*tg.PaymentsStarGifts).Gifts {
			if sg := x.(*tg.StarGift); sg.ID == k.gift.ID && (sg.AvailabilityResale != 1 || sg.ResellMinStars != 500 || sg.SoldOut) {
				return fmt.Errorf("tile %+v", sg)
			}
		}
		cfg, err := c.HelpGetAppConfig(ctx, 0)
		if err != nil {
			return err
		}
		for _, v := range cfg.(*tg.HelpAppConfig).Config.(*tg.JSONObject).Value {
			if v.Key == "stars_stargift_resale_commission_permille" && v.Value.(*tg.JSONNumber).Value != 850 {
				return fmt.Errorf("seller share %v", v.Value)
			}
		}

		// a form at 500, then she asks 700
		inv := resaleInv(slug, &tg.InputPeerSelf{})
		id, p500, err := formFor(ctx, c, inv)
		if err != nil || p500 != 500 {
			return fmt.Errorf("form %d: %v", p500, err)
		}
		if _, err := s.stars.SetResalePrice(ctx, stars.Holder{User: alice.id}, k.int("SELECT id FROM gift_receipts WHERE unique_id IS NOT NULL"), 700); err != nil {
			return err
		}
		if _, err := sendForm(ctx, c, id, inv); !isErr(err, "STARGIFT_RESELL_PRICE_CHANGED") {
			return fmt.Errorf("price changed: %v", err)
		}
		// a form at 700, then she takes it off
		if id, _, err = formFor(ctx, c, inv); err != nil {
			return err
		}
		if _, err := s.stars.SetResalePrice(ctx, stars.Holder{User: alice.id}, k.int("SELECT id FROM gift_receipts WHERE unique_id IS NOT NULL"), 0); err != nil {
			return err
		}
		if _, err := sendForm(ctx, c, id, inv); !isErr(err, "SLUG_INVALID") {
			return fmt.Errorf("unlisted: %v", err)
		}
		return nil
	})
	k.same("refusals")

	// taking it off while bob pays: one of them wins, never both
	receipt := k.int("SELECT id FROM gift_receipts WHERE unique_id IS NOT NULL")
	if _, err := s.stars.SetResalePrice(k.ctx, stars.Holder{User: alice.id}, receipt, 700); err != nil {
		t.Fatal(err)
	}
	inv := resaleInv(slug, &tg.InputPeerSelf{})
	var form int64
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		var err error
		form, _, err = formFor(ctx, c, inv)
		return err
	})
	errs := raceForms(s, []person{bob, alice}, func(i int, ctx context.Context, c *tg.Client) error {
		if i == 0 {
			_, err := sendForm(ctx, c, form, inv)
			return err
		}
		_, err := c.PaymentsUpdateStarGiftPrice(ctx, &tg.PaymentsUpdateStarGiftPriceRequest{Stargift: bySlug, ResellAmount: price(0)})
		return err
	})
	k.same("unlist while paying")
	owner := k.int("SELECT owner_id FROM gift_receipts WHERE id=$1", receipt)
	switch {
	case errs[0] == nil && owner == bob.id:
		// bob paid first; whatever alice did after found nothing of hers
		if k.balance(alice) != 1000-150+595 || k.balance(bob) != 5000-700 || k.gifts() != 50+105 {
			t.Fatalf("alice %d, bob %d, gifts %d", k.balance(alice), k.balance(bob), k.gifts())
		}
	case errs[1] == nil && errs[0] != nil && owner == alice.id:
		// she was first: bob paid nothing; list again and sell
		if k.balance(bob) != 5000 {
			t.Fatalf("bob %d", k.balance(bob))
		}
		if _, err := s.stars.SetResalePrice(k.ctx, stars.Holder{User: alice.id}, receipt, 700); err != nil {
			t.Fatal(err)
		}
		run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
			_, up, err := payResult(ctx, c, inv)
			if err != nil {
				return err
			}
			if act, u := uniqueOf(up); act == nil || u == nil || priceOf(u) != -1 || act.ResaleAmount.(*tg.StarsAmount).Amount != 700 {
				return fmt.Errorf("bought %+v %+v", act, u)
			}
			return nil
		})
	default:
		t.Fatalf("unlist and pay: %v / %v, owner %d", errs[0], errs[1], owner)
	}
	k.same("sold")
	// 700 split 85/15: 595 to alice, 105 to the gifts account
	if k.balance(alice) != 1000-150+595 || k.balance(bob) != 5000-700 || k.gifts() != 50+105 {
		t.Fatalf("alice %d, bob %d, gifts %d", k.balance(alice), k.balance(bob), k.gifts())
	}
	if k.int("SELECT count(*) FROM unique_gifts WHERE resale_stars IS NOT NULL") != 0 || k.int("SELECT owner_id FROM gift_receipts WHERE id=$1", receipt) != bob.id {
		t.Fatal("listing outlived the sale")
	}
	// alice was told what she got
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		h, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: userPeer(bob.id), Limit: 10})
		if err != nil {
			return err
		}
		mod, ok := h.AsModified()
		if !ok {
			return fmt.Errorf("history %T", h)
		}
		for _, msg := range mod.GetMessages() {
			if svc, ok := msg.(*tg.MessageService); ok {
				if act, ok := svc.Action.(*tg.MessageActionStarGiftUnique); ok {
					if amt, _ := act.ResaleAmount.(*tg.StarsAmount); amt != nil && amt.Amount == 595 {
						return nil
					}
				}
			}
		}
		return fmt.Errorf("no sale message for alice")
	})

	// bob relists; carol and dave pay at once; dave has no Stars, so first
	// carol and a funded dave race
	k.fund(dave, 5000, "resale-4")
	k.begin()
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.PaymentsUpdateStarGiftPrice(ctx, &tg.PaymentsUpdateStarGiftPriceRequest{Stargift: bySlug, ResellAmount: price(300)})
		return err
	})
	people := []person{carol, dave}
	forms := make([]int64, 2)
	for i, p := range people {
		run(t, s.client(p.storage), func(ctx context.Context, c *tg.Client) error {
			var err error
			forms[i], _, err = formFor(ctx, c, inv)
			return err
		})
	}
	errs = raceForms(s, people, func(i int, ctx context.Context, c *tg.Client) error {
		_, err := sendForm(ctx, c, forms[i], inv)
		return err
	})
	if err := onlyOne(errs); err != nil {
		t.Fatal(err)
	}
	k.same("two buyers")
	if k.balance(carol)+k.balance(dave) != 10000-300 {
		t.Fatalf("carol %d, dave %d", k.balance(carol), k.balance(dave))
	}
	// replaying the winner's form
	winner := people[map[bool]int{true: 0, false: 1}[errs[0] == nil]]
	run(t, s.client(winner.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := sendForm(ctx, c, forms[map[bool]int{true: 0, false: 1}[errs[0] == nil]], inv)
		return expectErr(err, "FORM_SUBMIT_DUPLICATE", "replay")
	})
	k.same("replay")

	// not enough Stars: the winner lists it for the most there is, bob can't pay that
	run(t, s.client(winner.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.PaymentsUpdateStarGiftPrice(ctx, &tg.PaymentsUpdateStarGiftPriceRequest{Stargift: bySlug, ResellAmount: price(stars.ResaleMaxStars)})
		return err
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		id, _, err := formFor(ctx, c, inv)
		if err != nil {
			return err
		}
		_, err = sendForm(ctx, c, id, inv)
		return expectErr(err, "BALANCE_TOO_LOW", "too poor")
	})
	k.same("too poor")
}

// TestAdminGiftMarket: the panel turns a gift's market on, prices it and
// shows what's on sale against the collection's supply; the commission is
// one setting.
func TestAdminGiftMarket(t *testing.T) {
	s := start(t)
	boss := s.browser(t)
	boss.must("/api/register", map[string]string{"username": "boss", "name": "Boss", "password": "correct horse boss"})
	adm := admin.New(s.db.Pool, s.accounts, s.settings, channels.New(s.db.Pool))
	adm.Gifts = s.stars
	adm.Media = s.media
	adm.Register(s.mux)
	k := newNFTKit(t, s, 100)
	plain, err := s.stars.AddGift(k.ctx, stars.Gift{Title: "Plain", Emoji: "🌹", Stars: 10})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/admin/gifts/" + strconv.FormatInt(k.gift.ID, 10)
	code, view := boss.call("PATCH", path, map[string]any{"market": map[string]any{"enabled": true, "baseStars": 300, "rarityMultiplier": 2.5}})
	mk, _ := view["market"].(map[string]any)
	up, _ := view["upgrade"].(map[string]any)
	if code != 200 || mk == nil || mk["enabled"] != true || mk["baseStars"] != float64(300) || mk["rarityMultiplier"] != 2.5 ||
		mk["offers"] != float64(12) || mk["minStars"] != float64(300) || mk["maxStars"] != float64(750) || up["supply"] != float64(100) || up["issued"] != float64(0) {
		t.Fatalf("market on: %d %v", code, view)
	}
	for _, bad := range []map[string]any{{"rarityMultiplier": 0.5}, {"baseStars": 0}, {"enabled": "yes"}} {
		if code, _ := boss.call("PATCH", path, map[string]any{"market": bad}); code != 400 {
			t.Fatalf("market %v: %d", bad, code)
		}
	}
	if code, _ := boss.call("PATCH", "/api/admin/gifts/"+strconv.FormatInt(plain.ID, 10), map[string]any{"market": map[string]any{"enabled": true}}); code != 400 {
		t.Fatalf("a gift with no collection sold as variants: %d", code)
	}
	code, view = boss.call("PATCH", path, map[string]any{"market": map[string]any{"baseStars": nil}})
	if mk, _ = view["market"].(map[string]any); code != 200 || mk["baseStars"] != nil || mk["defaultBaseStars"] != float64(150) || mk["enabled"] != true {
		t.Fatalf("default base: %d %v", code, view)
	}
	code, cfg := boss.call("GET", "/api/admin/gift-market", nil)
	if code != 200 || cfg["commissionPermille"] != float64(100) {
		t.Fatalf("commission %d %v", code, cfg)
	}
	if code, _ := boss.call("PATCH", "/api/admin/gift-market", map[string]any{"commissionPermille": 950}); code != 400 {
		t.Fatalf("95%%: %d", code)
	}
	if code, cfg = boss.call("PATCH", "/api/admin/gift-market", map[string]any{"commissionPermille": 50}); code != 200 || cfg["commissionPermille"] != float64(50) {
		t.Fatalf("5%%: %d %v", code, cfg)
	}
	if n, _ := s.stars.Commission(k.ctx); n != 50 {
		t.Fatalf("saved %d", n)
	}
}
