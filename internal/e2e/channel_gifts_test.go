package e2e

import (
	"context"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/tg"
)

// channelGiftPosts are the gift posts in a channel's history, newest first.
func channelGiftPosts(ctx context.Context, c *tg.Client, ch *tg.Channel) ([]*tg.MessageService, error) {
	h, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peerCh(ch), Limit: 50})
	if err != nil {
		return nil, err
	}
	var out []*tg.MessageService
	for _, m := range h.(*tg.MessagesChannelMessages).Messages {
		if svc, ok := m.(*tg.MessageService); ok {
			switch svc.Action.(type) {
			case *tg.MessageActionStarGift, *tg.MessageActionStarGiftUnique:
				out = append(out, svc)
			}
		}
	}
	return out, nil
}

// TestChannelGifts: an ordinary gift sent to a broadcast channel, paid by
// form, posted in the channel, on its profile, and upgraded and moved out
// by its admin. The refusals cost nothing.
func TestChannelGifts(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")
	dave := s.signUp(t, "dave")
	k := newNFTKit(t, s, 10)
	k.fund(alice, 5000, "channel-gifts-1")
	k.fund(carol, 500, "channel-gifts-2")
	k.begin()

	var channel, group, hidden *tg.Channel
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: true, Title: "Carol's channel", About: ""})
		if err != nil {
			return err
		}
		channel = channelOf(t, res)
		if _, err := c.ChannelsUpdateUsername(ctx, &tg.ChannelsUpdateUsernameRequest{Channel: inCh(channel), Username: "carolchan"}); err != nil {
			return err
		}
		res, err = c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Megagroup: true, Title: "Carol's group", About: ""})
		if err != nil {
			return err
		}
		group = channelOf(t, res)
		if _, err := c.ChannelsUpdateUsername(ctx, &tg.ChannelsUpdateUsernameRequest{Channel: inCh(group), Username: "carolgroup"}); err != nil {
			return err
		}
		res, err = c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: true, Title: "Carol's secret", About: ""})
		hidden = channelOf(t, res)
		return err
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.ChannelsJoinChannel(ctx, inCh(channel))
		return err
	})

	gift := func(ch *tg.Channel, hide, upgrade bool, note string) *tg.InputInvoiceStarGift {
		return &tg.InputInvoiceStarGift{Peer: peerCh(ch), GiftID: k.gift.ID, HideName: hide, IncludeUpgrade: upgrade,
			Message: tg.TextWithEntities{Text: note}}
	}

	// refusals: a group, a private channel she can't see, no Stars
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if _, _, err := pay(ctx, c, gift(group, false, false, "")); !isErr(err, "PEER_ID_INVALID") {
			return fmt.Errorf("gift to a group: %v", err)
		}
		if _, _, err := pay(ctx, c, gift(hidden, false, false, "")); err == nil {
			return fmt.Errorf("gift to a private channel she isn't in")
		}
		return nil
	})
	run(t, s.client(dave.storage), func(ctx context.Context, c *tg.Client) error {
		_, _, err := pay(ctx, c, gift(channel, false, false, ""))
		return expectErr(err, "BALANCE_TOO_LOW", "gift without Stars")
	})
	k.same("refusals")
	if n := k.int("SELECT count(*) FROM gift_receipts"); n != 0 {
		t.Fatalf("refusals left %d gifts", n)
	}

	// a plain gift with a note, then one with her name hidden and the upgrade paid
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		inv := gift(channel, false, false, "for the channel")
		form, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
		if err != nil {
			return err
		}
		f := form.(*tg.PaymentsPaymentFormStarGift)
		if f.Invoice.Prices[0].Amount != 100 {
			return fmt.Errorf("price %d", f.Invoice.Prices[0].Amount)
		}
		res, err := c.PaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: f.FormID, Invoice: inv})
		if err != nil {
			return err
		}
		if ups := res.(*tg.PaymentsPaymentResult).Updates.(*tg.Updates); len(ups.Chats) == 0 {
			return fmt.Errorf("the reply names no channel")
		}
		// the same form again is no second gift
		if _, err := c.PaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: f.FormID, Invoice: inv}); !isErr(err, "FORM_SUBMIT_DUPLICATE") {
			return fmt.Errorf("replayed form: %v", err)
		}
		if _, price, err := pay(ctx, c, gift(channel, true, true, "")); err != nil || price != 150 {
			return fmt.Errorf("hidden gift with its upgrade: %d %v", price, err)
		}
		if bal, _ := starsBalance(ctx, c); bal != 4750 {
			return fmt.Errorf("alice has %d", bal)
		}
		st, err := c.PaymentsGetStarsTransactions(ctx, &tg.PaymentsGetStarsTransactionsRequest{Peer: &tg.InputPeerSelf{}, Limit: 10})
		if err != nil {
			return err
		}
		for _, tr := range st.History {
			if tr.Gift {
				p, ok := tr.Peer.(*tg.StarsTransactionPeer)
				if !ok {
					return fmt.Errorf("purchase names %+v", tr.Peer)
				}
				if ch, ok := p.Peer.(*tg.PeerChannel); !ok || ch.ChannelID != channel.ID {
					return fmt.Errorf("purchase names %+v", p.Peer)
				}
			}
		}
		return nil
	})
	k.burnt += 200
	k.same("gifts to the channel")
	if k.gifts() != 50 {
		t.Fatalf("gifts account %d", k.gifts())
	}
	if n := k.int("SELECT count(*) FROM gift_receipts WHERE owner_channel_id=$1 AND owner_id IS NULL AND message_id IS NOT NULL", channel.ID); n != 2 {
		t.Fatalf("channel holds %d gifts", n)
	}

	// a subscriber sees the posts and the profile, the names Alice gave
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		posts, err := channelGiftPosts(ctx, c, channel)
		if err != nil {
			return err
		}
		if len(posts) != 2 {
			return fmt.Errorf("%d gift posts", len(posts))
		}
		hiddenPost, plain := posts[0].Action.(*tg.MessageActionStarGift), posts[1].Action.(*tg.MessageActionStarGift)
		if !posts[1].Post || posts[1].FromID != nil {
			return fmt.Errorf("the post isn't the channel's: %+v", posts[1])
		}
		if p, ok := plain.Peer.(*tg.PeerChannel); !ok || p.ChannelID != channel.ID || plain.SavedID == 0 || plain.Message.Text != "for the channel" {
			return fmt.Errorf("plain post %+v", plain)
		}
		if f, ok := plain.FromID.(*tg.PeerUser); !ok || f.UserID != alice.id || !plain.CanUpgrade {
			return fmt.Errorf("plain post from %+v", plain.FromID)
		}
		if !hiddenPost.NameHidden || hiddenPost.FromID != nil || hiddenPost.UpgradeStars != 50 {
			return fmt.Errorf("hidden post %+v", hiddenPost)
		}
		// the post is the channel's, so the history has to bring the sender along
		h, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peerCh(channel), Limit: 50})
		if err != nil {
			return err
		}
		named := false
		for _, u := range h.(*tg.MessagesChannelMessages).Users {
			if u, ok := u.(*tg.User); ok && u.ID == alice.id {
				named = true
			}
		}
		if !named {
			return fmt.Errorf("the history doesn't bring alice")
		}
		list, err := savedGifts(ctx, c, peerCh(channel))
		if err != nil {
			return err
		}
		if len(list.Gifts) != 2 || list.Gifts[0].SavedID != 0 || list.Gifts[0].FromID != nil || list.Gifts[1].FromID == nil {
			return fmt.Errorf("bob's view of the profile %+v", list.Gifts)
		}
		// and can't act on them
		in := &tg.InputSavedStarGiftChat{Peer: peerCh(channel), SavedID: plain.SavedID}
		if _, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: &tg.InputInvoiceStarGiftUpgrade{Stargift: in}}); !isErr(err, "PEER_ID_INVALID") {
			return fmt.Errorf("bob upgrades the channel's gift: %v", err)
		}
		if _, err := c.PaymentsUpgradeStarGift(ctx, &tg.PaymentsUpgradeStarGiftRequest{Stargift: &tg.InputSavedStarGiftChat{Peer: peerCh(channel), SavedID: hiddenPost.SavedID}}); !isErr(err, "PEER_ID_INVALID") {
			return fmt.Errorf("bob uses the prepaid upgrade: %v", err)
		}
		return nil
	})

	// the admin: hides one, upgrades both, moves one out
	var slug string
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		list, err := savedGifts(ctx, c, peerCh(channel))
		if err != nil {
			return err
		}
		prepaid, plain := list.Gifts[0], list.Gifts[1]
		if plain.SavedID == 0 || !plain.CanUpgrade || plain.ConvertStars != 0 || plain.MsgID != 0 || prepaid.UpgradeStars != 50 {
			return fmt.Errorf("the admin's view %+v", list.Gifts)
		}
		in := func(g tg.SavedStarGift) *tg.InputSavedStarGiftChat {
			return &tg.InputSavedStarGiftChat{Peer: peerCh(channel), SavedID: g.SavedID}
		}
		if _, err := c.PaymentsSaveStarGift(ctx, &tg.PaymentsSaveStarGiftRequest{Stargift: in(plain), Unsave: true}); err != nil {
			return err
		}
		posts, err := channelGiftPosts(ctx, c, channel)
		if err != nil {
			return err
		}
		if act := posts[1].Action.(*tg.MessageActionStarGift); act.Saved {
			return fmt.Errorf("the post still says it's on the profile")
		}
		// a channel's gift isn't converted: nowhere to put the Stars
		if _, err := c.PaymentsConvertStarGift(ctx, in(plain)); err == nil {
			return fmt.Errorf("converted a channel's gift")
		}
		// paid upgrade, keeping the details
		up := &tg.InputInvoiceStarGiftUpgrade{Stargift: in(plain), KeepOriginalDetails: true}
		form, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: up})
		if err != nil {
			return err
		}
		f := form.(*tg.PaymentsPaymentFormStarGift)
		if _, err := c.PaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: f.FormID, Invoice: up}); err != nil {
			return err
		}
		if _, err := c.PaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: f.FormID, Invoice: up}); err == nil {
			return fmt.Errorf("paid the upgrade twice")
		}
		// the prepaid one, free
		if _, err := c.PaymentsUpgradeStarGift(ctx, &tg.PaymentsUpgradeStarGiftRequest{Stargift: in(prepaid)}); err != nil {
			return err
		}
		if _, err := c.PaymentsUpgradeStarGift(ctx, &tg.PaymentsUpgradeStarGiftRequest{Stargift: in(prepaid)}); !isErr(err, "STARGIFT_ALREADY_UPGRADED") {
			return fmt.Errorf("upgraded twice: %v", err)
		}
		if bal, _ := starsBalance(ctx, c); bal != 450 {
			return fmt.Errorf("carol paid %d", 500-bal)
		}
		posts, err = channelGiftPosts(ctx, c, channel)
		if err != nil {
			return err
		}
		if len(posts) != 4 {
			return fmt.Errorf("%d gift posts after the upgrades", len(posts))
		}
		var kept *tg.StarGiftUnique
		for _, p := range posts[:2] {
			act, ok := p.Action.(*tg.MessageActionStarGiftUnique)
			if !ok || !act.Upgrade || act.SavedID == 0 || !p.Post {
				return fmt.Errorf("upgrade post %+v", p)
			}
			if u := act.Gift.(*tg.StarGiftUnique); !act.PrepaidUpgrade {
				kept = u
				// the admin who paid turned it, not the channel itself
				if f, ok := act.FromID.(*tg.PeerUser); !ok || f.UserID != carol.id {
					return fmt.Errorf("paid upgrade from %+v", act.FromID)
				}
			}
		}
		if old := posts[3].Action.(*tg.MessageActionStarGift); !old.Upgraded || old.CanUpgrade || old.UpgradeMsgID != posts[1].ID && old.UpgradeMsgID != posts[0].ID {
			return fmt.Errorf("the gift's post after its upgrade %+v", old)
		}
		_, _, _, od := attrsOf(kept)
		if od == nil {
			return fmt.Errorf("no original details")
		}
		if p, ok := od.RecipientID.(*tg.PeerChannel); !ok || p.ChannelID != channel.ID || od.Message.Text != "for the channel" {
			return fmt.Errorf("original details %+v", od)
		}
		if f, ok := od.SenderID.(*tg.PeerUser); !ok || f.UserID != alice.id {
			return fmt.Errorf("original sender %+v", od.SenderID)
		}
		slug = kept.Slug
		// out to bob, paying the fee
		list, err = savedGifts(ctx, c, peerCh(channel))
		if err != nil {
			return err
		}
		for _, g := range list.Gifts {
			if u, ok := g.Gift.(*tg.StarGiftUnique); ok && u.Slug == slug {
				_, _, err := payResult(ctx, c, &tg.InputInvoiceStarGiftTransfer{Stargift: in(g), ToID: userPeer(bob.id)})
				return err
			}
		}
		return fmt.Errorf("no %s on the channel", slug)
	})
	k.same("upgrades and the transfer")
	if k.gifts() != 125 || k.issued() != 2 {
		t.Fatalf("gifts account %d, issued %d", k.gifts(), k.issued())
	}
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		_, u, err := mineUnique(ctx, c, slug)
		if err != nil {
			return err
		}
		_, _, _, od := attrsOf(u)
		if p, ok := od.RecipientID.(*tg.PeerChannel); !ok || p.ChannelID != channel.ID {
			return fmt.Errorf("details after the move %+v", od)
		}
		return nil
	})
}
