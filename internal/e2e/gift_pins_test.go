package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/stars"
)

// A collectible bought off the market for oneself: it's held, can be moved
// and listed, a resale keeps who gave it, and a profile pins its collectibles.
func TestMarketCollectibleLifecycle(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")
	k := newNFTKit(t, s, 0)
	m := market{k: k, gift: k.gift.ID}
	if err := s.stars.SetMarket(k.ctx, k.gift.ID, stars.Market{Enabled: true, Base: 200, RarityX100: 300}); err != nil {
		t.Fatal(err)
	}
	k.fund(alice, 10000, "lifecycle-1")
	k.fund(bob, 10000, "lifecycle-2")
	k.fund(carol, 10000, "lifecycle-3")
	k.begin()
	now := int(time.Now().Unix())

	// an offer, bought for myself
	buyOffer := func(ctx context.Context, c *tg.Client, i int) (string, error) {
		p, err := m.page(ctx, c, tg.PaymentsGetResaleStarGiftsRequest{SortByPrice: true})
		if err != nil {
			return "", err
		}
		slug := p.Gifts[i].(*tg.StarGiftUnique).Slug
		_, _, err = pay(ctx, c, resaleInv(slug, &tg.InputPeerSelf{}))
		if err != nil {
			return "", err
		}
		// the offer's slug names the combination; the minted one is numbered
		_, u, err := lastUnique(ctx, c)
		if err != nil {
			return "", err
		}
		return u.Slug, nil
	}

	var slug string
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		var err error
		if slug, err = buyOffer(ctx, c, 0); err != nil {
			return err
		}
		g, u, err := mineUnique(ctx, c, slug)
		if err != nil {
			return err
		}
		// what Web A needs for From, Owner, Transfer and Sell
		from, _ := g.GetFromID()
		owner, _ := u.GetOwnerID()
		at, _ := g.GetCanTransferAt()
		resell, _ := g.GetCanResellAt()
		if g.NameHidden || !samePeer(from, bob.id) || !samePeer(owner, bob.id) || at == 0 || at > now+5 || resell == 0 || resell > now+5 ||
			g.TransferStars != 25 || g.SavedID == 0 {
			return fmt.Errorf("bought offer as saved gift %+v, gift owner %v", g, owner)
		}
		// listed, then bought by carol
		if _, err := c.PaymentsUpdateStarGiftPrice(ctx, &tg.PaymentsUpdateStarGiftPriceRequest{Stargift: selfSaved(g), ResellAmount: &tg.StarsAmount{Amount: 300}}); err != nil {
			return err
		}
		return nil
	})
	k.same("bought and listed")

	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		up, err := sendResale(ctx, c, slug)
		if err != nil {
			return err
		}
		// "You bought ... for 300": a sale, not a gift, with the fee the
		// gift sheet opened from it needs
		act, _ := uniqueOf(up)
		if act == nil || act.Transferred || act.ResaleAmount == nil || act.ResaleAmount.(*tg.StarsAmount).Amount != 300 || act.TransferStars != 25 {
			return fmt.Errorf("purchase message %+v", act)
		}
		// the sheet opened from it says who gave it, not the buyer
		if from, _ := act.GetFromID(); !samePeer(from, bob.id) {
			return fmt.Errorf("purchase message from %v", from)
		}
		g, u, err := mineUnique(ctx, c, slug)
		if err != nil {
			return err
		}
		// From stays who gave it (bob bought it for himself), not the buyer
		from, _ := g.GetFromID()
		owner, _ := u.GetOwnerID()
		if samePeer(from, carol.id) || !samePeer(owner, carol.id) {
			return fmt.Errorf("after the resale: from %v, owner %v", from, owner)
		}
		// moved on with the transfer fee
		inv := &tg.InputInvoiceStarGiftTransfer{Stargift: selfSaved(g), ToID: &tg.InputPeerUser{UserID: alice.id}}
		if _, price, err := pay(ctx, c, inv); err != nil || price != 25 {
			return fmt.Errorf("transfer: %d %v", price, err)
		}
		return nil
	})
	k.same("resold and transferred")
	if got := k.int("SELECT coalesce(r.owner_id, 0) FROM gift_receipts r JOIN unique_gifts u ON u.id = r.unique_id WHERE u.slug=$1", slug); got != alice.id {
		t.Fatalf("holder %d, want alice", got)
	}

	// alice pins two of her three, in her order
	var slugs []string
	pin := func(ctx context.Context, c *tg.Client, list ...tg.InputSavedStarGiftClass) error {
		_, err := c.PaymentsToggleStarGiftsPinnedToTop(ctx, &tg.PaymentsToggleStarGiftsPinnedToTopRequest{Peer: &tg.InputPeerSelf{}, Stargift: list})
		return err
	}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		for i := 0; i < 2; i++ {
			sl, err := buyOffer(ctx, c, 0)
			if err != nil {
				return err
			}
			slugs = append(slugs, sl)
		}
		g1, _, err := mineUnique(ctx, c, slug)
		if err != nil {
			return err
		}
		g2, _, err := mineUnique(ctx, c, slugs[0])
		if err != nil {
			return err
		}
		return pin(ctx, c, selfSaved(g2), selfSaved(g1))
	})
	// as bob sees her profile: pinned first, in her order, flagged
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		list, err := savedGifts(ctx, c, &tg.InputPeerUser{UserID: alice.id})
		if err != nil {
			return err
		}
		var got []string
		for _, g := range list.Gifts {
			got = append(got, fmt.Sprintf("%s:%v", g.Gift.(*tg.StarGiftUnique).Slug, g.PinnedToTop))
		}
		if want := []string{slugs[0] + ":true", slug + ":true", slugs[1] + ":false"}; fmt.Sprint(got) != fmt.Sprint(want) {
			return fmt.Errorf("pinned order %v, want %v", got, want)
		}
		// someone else's gift can't go on my profile
		return expectErr(pin(ctx, c, &tg.InputSavedStarGiftSlug{Slug: slug}), "SAVED_ID_EMPTY", "pinning alice's gift as bob")
	})
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		g1, _, err := mineUnique(ctx, c, slug)
		if err != nil {
			return err
		}
		g2, _, err := mineUnique(ctx, c, slugs[0])
		if err != nil {
			return err
		}
		// hiding one unpins it; an empty list unpins the rest
		if _, err := c.PaymentsSaveStarGift(ctx, &tg.PaymentsSaveStarGiftRequest{Unsave: true, Stargift: selfSaved(g2)}); err != nil {
			return err
		}
		if n := k.int("SELECT count(*) FROM gift_receipts WHERE owner_id=$1 AND pinned IS NOT NULL", alice.id); n != 1 {
			return fmt.Errorf("%d pinned after hiding one", n)
		}
		if err := pin(ctx, c); err != nil {
			return err
		}
		if n := k.int("SELECT count(*) FROM gift_receipts WHERE pinned IS NOT NULL"); n != 0 {
			return fmt.Errorf("%d pinned after unpinning", n)
		}
		in := selfSaved(g1)
		return expectErr(pin(ctx, c, in, in, in, in, in, in, in), "STARGIFT_PIN_LIMIT_EXCEEDED", "seven pins")
	})
	k.same("pins")
}

func samePeer(p tg.PeerClass, id int64) bool {
	u, ok := p.(*tg.PeerUser)
	return ok && u.UserID == id
}

func sendResale(ctx context.Context, c *tg.Client, slug string) (tg.UpdatesClass, error) {
	inv := resaleInv(slug, &tg.InputPeerSelf{})
	id, _, err := formFor(ctx, c, inv)
	if err != nil {
		return nil, err
	}
	return sendForm(ctx, c, id, inv)
}

// lastUnique is my newest collectible.
func lastUnique(ctx context.Context, c *tg.Client) (tg.SavedStarGift, *tg.StarGiftUnique, error) {
	list, err := savedGifts(ctx, c, &tg.InputPeerSelf{})
	if err != nil {
		return tg.SavedStarGift{}, nil, err
	}
	for _, g := range list.Gifts {
		if u, ok := g.Gift.(*tg.StarGiftUnique); ok && !g.PinnedToTop {
			return g, u, nil
		}
	}
	return tg.SavedStarGift{}, nil, fmt.Errorf("no collectible among %d", len(list.Gifts))
}
