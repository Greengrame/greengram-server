package e2e

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/api"
)

// giveawayKit is a channel with people in it and what the giveaway tests
// check after every step.
type giveawayKit struct {
	t     *testing.T
	s     *stack
	ctx   context.Context
	start int64 // the ledger's total, which nothing may change
}

func newGiveawayKit(t *testing.T, s *stack) *giveawayKit {
	k := &giveawayKit{t: t, s: s, ctx: context.Background()}
	k.start = k.total()
	return k
}

func (k *giveawayKit) total() int64 {
	k.t.Helper()
	var n int64
	if err := k.s.db.Pool.QueryRow(k.ctx, "SELECT coalesce(sum(amount), 0) FROM star_ledger").Scan(&n); err != nil {
		k.t.Fatal(err)
	}
	return n
}

// same checks the ledger adds up to what it did, less what Premium cost
// (Premium is bought from the instance; those Stars leave, as on any
// Premium purchase).
func (k *giveawayKit) same(premiumSpent int64) {
	k.t.Helper()
	if got := k.total(); got != k.start-premiumSpent {
		k.t.Fatalf("ledger total %d, want %d", got, k.start-premiumSpent)
	}
}

func (k *giveawayKit) sql(q string, args ...any) {
	k.t.Helper()
	if _, err := k.s.db.Pool.Exec(k.ctx, q, args...); err != nil {
		k.t.Fatalf("%s: %v", q, err)
	}
}

func (k *giveawayKit) int(q string, args ...any) int64 {
	k.t.Helper()
	var n int64
	if err := k.s.db.Pool.QueryRow(k.ctx, q, args...).Scan(&n); err != nil {
		k.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (k *giveawayKit) channel(owner person, title, username string, members ...person) *tg.Channel {
	k.t.Helper()
	var ch *tg.Channel
	run(k.t, k.s.client(owner.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: true, Title: title})
		if err != nil {
			return err
		}
		ch = channelOf(k.t, res)
		_, err = c.ChannelsUpdateUsername(ctx, &tg.ChannelsUpdateUsernameRequest{Channel: inCh(ch), Username: username})
		return err
	})
	k.join(ch, members...)
	return ch
}

func (k *giveawayKit) join(ch *tg.Channel, members ...person) {
	k.t.Helper()
	for _, u := range members {
		run(k.t, k.s.client(u.storage), func(ctx context.Context, c *tg.Client) error {
			_, err := c.ChannelsJoinChannel(ctx, inCh(ch))
			return err
		})
	}
}

// drawn waits for giveaway (by its post) to be settled, after moving the
// clock past its end and drawing.
func (k *giveawayKit) draw(ch *tg.Channel, post int) {
	k.t.Helper()
	k.s.stars.Skip(10 * time.Minute)
	k.s.api.DrawGiveaways(k.ctx)
	k.waitState(ch, post, "running", false)
	// prize messages may be going out on the minute timer too
	deadline := time.Now().Add(20 * time.Second)
	for k.int("SELECT count(*) FROM giveaway_winners WHERE NOT notified") > 0 {
		if time.Now().After(deadline) {
			k.t.Fatal("prize messages never went out")
		}
		k.s.api.DrawGiveaways(k.ctx)
		time.Sleep(200 * time.Millisecond)
	}
}

func (k *giveawayKit) waitState(ch *tg.Channel, post int, state string, is bool) {
	k.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var st string
		if err := k.s.db.Pool.QueryRow(k.ctx, "SELECT state FROM giveaways WHERE channel_id=$1 AND post_id=$2", ch.ID, post).Scan(&st); err != nil {
			k.t.Fatal(err)
		}
		if (st == state) == is {
			return
		}
		if time.Now().After(deadline) {
			k.t.Fatalf("giveaway %d still %s", post, st)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func channelAll(ctx context.Context, c *tg.Client, ch *tg.Channel) ([]tg.MessageClass, error) {
	h, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peerCh(ch), Limit: 20})
	if err != nil {
		return nil, err
	}
	return h.(*tg.MessagesChannelMessages).Messages, nil
}

// giveawayPost finds the giveaway post and the launch message before it.
func giveawayPost(ctx context.Context, c *tg.Client, ch *tg.Channel) (*tg.Message, *tg.MessageMediaGiveaway, *tg.MessageActionGiveawayLaunch, error) {
	list, err := channelAll(ctx, c, ch)
	if err != nil {
		return nil, nil, nil, err
	}
	for i, m := range list {
		msg, ok := m.(*tg.Message)
		if !ok {
			continue
		}
		g, ok := msg.Media.(*tg.MessageMediaGiveaway)
		if !ok || i+1 >= len(list) {
			continue
		}
		s, ok := list[i+1].(*tg.MessageService)
		if !ok {
			return nil, nil, nil, fmt.Errorf("before the post: %+v", list[i+1])
		}
		l, ok := s.Action.(*tg.MessageActionGiveawayLaunch)
		if !ok {
			return nil, nil, nil, fmt.Errorf("before the post: %+v", s.Action)
		}
		return msg, g, l, nil
	}
	return nil, nil, nil, fmt.Errorf("no giveaway post in %+v", list)
}

func starsGiveaway(ch *tg.Channel, stars int64, users int, until int, set func(*tg.InputStorePaymentStarsGiveaway)) *tg.InputInvoiceStars {
	p := &tg.InputStorePaymentStarsGiveaway{BoostPeer: peerCh(ch), Stars: stars, Currency: "XTR", Amount: stars, Users: users, RandomID: 1, UntilDate: until}
	if set != nil {
		set(p)
	}
	return &tg.InputInvoiceStars{Purpose: p}
}

func premiumGiveaway(ch *tg.Channel, o tg.PremiumGiftCodeOption, until int, set func(*tg.InputStorePaymentPremiumGiveaway)) *tg.InputInvoicePremiumGiftCode {
	p := &tg.InputStorePaymentPremiumGiveaway{BoostPeer: peerCh(ch), Currency: "XTR", Amount: o.Amount, RandomID: 1, UntilDate: until}
	if set != nil {
		set(p)
	}
	return &tg.InputInvoicePremiumGiftCode{Purpose: p, Option: o}
}

func info(ctx context.Context, c *tg.Client, ch *tg.Channel, msg int) (tg.PaymentsGiveawayInfoClass, error) {
	return c.PaymentsGetGiveawayInfo(ctx, &tg.PaymentsGetGiveawayInfoRequest{Peer: peerCh(ch), MsgID: msg})
}

func chatSize(ctx context.Context, c *tg.Client, peer int64) (int, error) {
	h, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: userPeer(peer), Limit: 50})
	if err != nil {
		return 0, err
	}
	return len(h.(tg.ModifiedMessagesMessages).GetMessages()), nil
}

func soon() int { return int(time.Now().Unix()) + 120 }

func TestStarsGiveaway(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")
	dave := s.signUp(t, "dave")
	k := newGiveawayKit(t, s)
	if _, _, err := s.stars.Adjust(k.ctx, alice.id, alice.id, 3000, "starter", "e2e-giveaway-1"); err != nil {
		t.Fatal(err)
	}
	k.start = k.total()
	channel := k.channel(alice, "Draw", "drawchannel", bob, carol)

	// members only: the form is for an admin
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		if _, _, err := pay(ctx, c, starsGiveaway(channel, 500, 1, soon(), nil)); !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
			return fmt.Errorf("member bought a giveaway: %v", err)
		}
		return nil
	})

	var post *tg.Message
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		opts, err := c.PaymentsGetStarsGiveawayOptions(ctx)
		if err != nil {
			return err
		}
		if len(opts) == 0 {
			return fmt.Errorf("no options")
		}
		for _, o := range opts {
			if o.Currency != "XTR" || o.Amount != o.Stars || o.YearlyBoosts < 1 {
				return fmt.Errorf("option %+v", o)
			}
			for _, w := range o.Winners {
				if w.PerUserStars*int64(w.Users) != o.Stars {
					return fmt.Errorf("%d x %d isn't %d", w.PerUserStars, w.Users, o.Stars)
				}
			}
		}

		// numbers that aren't an option, and a price that isn't the amount
		bad := []*tg.InputStorePaymentStarsGiveaway{
			{BoostPeer: peerCh(channel), Stars: 777, Currency: "XTR", Amount: 777, Users: 1, UntilDate: soon()},
			{BoostPeer: peerCh(channel), Stars: 1000, Currency: "XTR", Amount: 1000, Users: 3, UntilDate: soon()},
			{BoostPeer: peerCh(channel), Stars: 1000, Currency: "XTR", Amount: 1, Users: 1, UntilDate: soon()},
			{BoostPeer: peerCh(channel), Stars: 1000, Currency: "USD", Amount: 1000, Users: 1, UntilDate: soon()},
			{BoostPeer: peerCh(channel), Stars: -1000, Currency: "XTR", Amount: -1000, Users: 1, UntilDate: soon()},
		}
		for _, p := range bad {
			if _, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: &tg.InputInvoiceStars{Purpose: p}}); !tgerr.Is(err, "INVOICE_INVALID") {
				return fmt.Errorf("%+v: %v", p, err)
			}
		}
		// an end date that passed, or past the longest a giveaway runs
		for _, until := range []int{int(time.Now().Unix()) - 10, int(time.Now().Unix()) + 40*86400} {
			if _, _, err := pay(ctx, c, starsGiveaway(channel, 500, 1, until, nil)); !tgerr.Is(err, "GIVEAWAY_UNTIL_DATE_INVALID") {
				return fmt.Errorf("until %d: %v", until, err)
			}
		}
		// the boost channel again as an extra one, and a made-up country
		if _, _, err := pay(ctx, c, starsGiveaway(channel, 500, 1, soon(), func(p *tg.InputStorePaymentStarsGiveaway) {
			p.AdditionalPeers = []tg.InputPeerClass{peerCh(channel)}
		})); !tgerr.Is(err, "ADDITIONAL_PEERS_INVALID") {
			return fmt.Errorf("boost channel as an extra one: %v", err)
		}
		if _, _, err := pay(ctx, c, starsGiveaway(channel, 500, 1, soon(), func(p *tg.InputStorePaymentStarsGiveaway) {
			p.CountriesISO2 = []string{"UKR"}
		})); !tgerr.Is(err, "COUNTRIES_ISO2_INVALID") {
			return fmt.Errorf("country: %v", err)
		}
		// more than she has: nothing moves
		if _, _, err := pay(ctx, c, starsGiveaway(channel, 5000, 2, soon(), nil)); !tgerr.Is(err, "BALANCE_TOO_LOW") {
			return fmt.Errorf("unaffordable: %v", err)
		}
		if bal, err := starsBalance(ctx, c); err != nil || bal != 3000 {
			return fmt.Errorf("balance after refusals: %d %v", bal, err)
		}

		inv := starsGiveaway(channel, 2500, 2, soon(), func(p *tg.InputStorePaymentStarsGiveaway) {
			p.WinnersAreVisible = true
			p.PrizeDescription = "a mug"
		})
		id, price, err := pay(ctx, c, inv)
		if err != nil || price != 2500 {
			return fmt.Errorf("pay: %d %v", price, err)
		}
		if _, err := c.PaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: id, Invoice: inv}); !tgerr.Is(err, "FORM_SUBMIT_DUPLICATE") {
			return fmt.Errorf("paid twice: %v", err)
		}
		if bal, err := starsBalance(ctx, c); err != nil || bal != 500 {
			return fmt.Errorf("balance after paying: %d %v", bal, err)
		}

		// the post carries what she picked; nobody has won anything yet
		var g *tg.MessageMediaGiveaway
		var l *tg.MessageActionGiveawayLaunch
		if post, g, l, err = giveawayPost(ctx, c, channel); err != nil {
			return err
		}
		if l.Stars != 2500 || g.Stars != 2500 || g.Quantity != 2 || !g.WinnersAreVisible || g.PrizeDescription != "a mug" || g.OnlyNewSubscribers ||
			len(g.Channels) != 1 || g.Channels[0] != channel.ID || g.UntilDate != inv.Purpose.(*tg.InputStorePaymentStarsGiveaway).UntilDate || len(g.CountriesISO2) != 0 {
			return fmt.Errorf("giveaway post %+v launch %+v", g, l)
		}

		// 2500 Stars is 4 boosts at the default price, there from the start
		st, err := c.PremiumGetBoostsStatus(ctx, peerCh(channel))
		if err != nil {
			return err
		}
		if st.Boosts != 4 || st.GiftBoosts != 4 {
			return fmt.Errorf("boosts %+v", st)
		}
		list, err := c.PremiumGetBoostsList(ctx, &tg.PremiumGetBoostsListRequest{Peer: peerCh(channel), Gifts: true, Limit: 10})
		if err != nil {
			return err
		}
		if len(list.Boosts) != 2 || list.Boosts[0].Multiplier != 2 || list.Boosts[0].Stars != 1250 || !list.Boosts[0].Giveaway ||
			list.Boosts[0].UserID != 0 || list.Boosts[0].GiveawayMsgID != post.ID {
			return fmt.Errorf("boost list %+v", list.Boosts)
		}
		// the plain list is every boost, gifts included; counts are in boosts
		all, err := c.PremiumGetBoostsList(ctx, &tg.PremiumGetBoostsListRequest{Peer: peerCh(channel), Limit: 10})
		if err != nil {
			return err
		}
		if list.Count != 4 || all.Count != 4 || len(all.Boosts) != 2 || all.Boosts[0].Stars != 1250 {
			return fmt.Errorf("counts %d %d, all %+v", list.Count, all.Count, all.Boosts)
		}

		// the buyer runs the channel: she can't take part
		in, err := info(ctx, c, channel, post.ID)
		if err != nil {
			return err
		}
		if v, ok := in.(*tg.PaymentsGiveawayInfo); !ok || v.Participating || v.AdminDisallowedChatID != channel.ID || v.PreparingResults || v.StartDate == 0 {
			return fmt.Errorf("buyer's info %+v", in)
		}
		// a message that isn't a giveaway
		if _, err := info(ctx, c, channel, 1); !tgerr.Is(err, "MESSAGE_ID_INVALID") {
			return fmt.Errorf("info on another message: %v", err)
		}
		return nil
	})
	k.same(0)
	if n := k.int("SELECT coalesce(sum(amount), 0) FROM star_ledger WHERE reason='giveaway_hold'"); n != 2500 {
		t.Fatalf("held %d", n)
	}

	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		in, err := info(ctx, c, channel, post.ID)
		if err != nil {
			return err
		}
		if v, ok := in.(*tg.PaymentsGiveawayInfo); !ok || !v.Participating || v.AdminDisallowedChatID != 0 {
			return fmt.Errorf("member's info %+v", in)
		}
		return nil
	})
	run(t, s.client(dave.storage), func(ctx context.Context, c *tg.Client) error {
		in, err := info(ctx, c, channel, post.ID)
		if err != nil {
			return err
		}
		if v, ok := in.(*tg.PaymentsGiveawayInfo); !ok || v.Participating {
			return fmt.Errorf("outsider's info %+v", in)
		}
		return nil
	})

	// not due yet: nothing happens
	s.api.DrawGiveaways(k.ctx)
	k.waitState(channel, post.ID, "running", true)

	// past the end, with two workers on each of two servers: one draw
	inst := make([]byte, 32)
	s2 := startOn(t, s.db, inst)
	s.stars.Skip(10 * time.Minute)
	s2.stars.Skip(10 * time.Minute)
	var wg sync.WaitGroup
	for _, a := range []*api.API{s.api, s.api, s2.api, s2.api} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.DrawGiveaways(k.ctx)
		}()
	}
	wg.Wait()
	k.draw(channel, post.ID)
	s2.api.DrawGiveaways(k.ctx)
	if n := k.int("SELECT count(*) FROM giveaway_winners"); n != 2 {
		t.Fatalf("%d winners", n)
	}
	if n := k.int("SELECT count(*) FROM star_ledger WHERE reason IN ('stars_giveaway_prize', 'giveaway_release', 'giveaway_refund')"); n != 3 {
		t.Fatalf("%d draw rows", n)
	}
	if n := k.int("SELECT coalesce(sum(amount), 0) FROM star_ledger WHERE user_id=(SELECT user_id FROM system_accounts WHERE kind='giveaway_escrow')"); n != 0 {
		t.Fatalf("escrow still holds %d", n)
	}
	k.same(0)

	// each winner: their half, a prize message from the service account,
	// a transaction naming the channel and the post
	for _, u := range []person{bob, carol} {
		run(t, s.client(u.storage), func(ctx context.Context, c *tg.Client) error {
			if bal, err := starsBalance(ctx, c); err != nil || bal != 1250 {
				return fmt.Errorf("prize: %d %v", bal, err)
			}
			st, err := c.PaymentsGetStarsTransactions(ctx, &tg.PaymentsGetStarsTransactionsRequest{Peer: &tg.InputPeerSelf{}, Limit: 10})
			if err != nil {
				return err
			}
			if len(st.History) != 1 || len(st.Chats) != 1 {
				return fmt.Errorf("history %+v chats %+v", st.History, st.Chats)
			}
			txn := st.History[0]
			peer, ok := txn.Peer.(*tg.StarsTransactionPeer)
			if !ok {
				return fmt.Errorf("peer %T", txn.Peer)
			}
			if p, ok := peer.Peer.(*tg.PeerChannel); !ok || p.ChannelID != channel.ID || txn.GiveawayPostID != post.ID {
				return fmt.Errorf("transaction %+v", txn)
			}
			m, a, err := serviceAction[*tg.MessageActionPrizeStars](ctx, c, 777000)
			if err != nil {
				return err
			}
			if a.Stars != 1250 || a.TransactionID != txn.ID || a.GiveawayMsgID != post.ID || a.BoostPeer.(*tg.PeerChannel).ChannelID != channel.ID {
				return fmt.Errorf("prize action %+v", a)
			}
			if from, ok := m.FromID.(*tg.PeerUser); ok && from.UserID != 777000 {
				return fmt.Errorf("prize from %+v", m.FromID)
			}
			// nothing from the buyer
			if n, err := chatSize(ctx, c, alice.id); err != nil || n != 0 {
				return fmt.Errorf("buyer's chat has %d %v", n, err)
			}
			in, err := info(ctx, c, channel, post.ID)
			if err != nil {
				return err
			}
			if v, ok := in.(*tg.PaymentsGiveawayInfoResults); !ok || !v.Winner || v.StarsPrize != 1250 || v.WinnersCount != 2 || v.Refunded || v.FinishDate == 0 {
				return fmt.Errorf("winner's info %+v", in)
			}
			return nil
		})
	}

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if bal, err := starsBalance(ctx, c); err != nil || bal != 500 {
			return fmt.Errorf("buyer after the draw: %d %v", bal, err)
		}
		list, err := channelAll(ctx, c, channel)
		if err != nil {
			return err
		}
		// newest first: the winners' list, then the results message answering the post
		res, ok := list[0].(*tg.Message).Media.(*tg.MessageMediaGiveawayResults)
		if !ok || res.WinnersCount != 2 || len(res.Winners) != 2 || res.Stars != 2500 || res.LaunchMsgID != post.ID || res.ChannelID != channel.ID || res.PrizeDescription != "a mug" {
			return fmt.Errorf("results post %+v", list[0])
		}
		svc := list[1].(*tg.MessageService)
		if a, ok := svc.Action.(*tg.MessageActionGiveawayResults); !ok || !a.Stars || a.WinnersCount != 2 || a.UnclaimedCount != 0 {
			return fmt.Errorf("results action %+v", svc.Action)
		}
		if r, ok := svc.ReplyTo.(*tg.MessageReplyHeader); !ok || r.ReplyToMsgID != post.ID {
			return fmt.Errorf("results reply %+v", svc.ReplyTo)
		}
		// the info sheet on the results post is the same giveaway's
		if in, err := info(ctx, c, channel, list[0].GetID()); err != nil {
			return err
		} else if v, ok := in.(*tg.PaymentsGiveawayInfoResults); !ok || v.Winner || v.WinnersCount != 2 {
			return fmt.Errorf("buyer's results %+v", in)
		}
		boosts, err := c.PremiumGetBoostsList(ctx, &tg.PremiumGetBoostsListRequest{Peer: peerCh(channel), Gifts: true, Limit: 10})
		if err != nil {
			return err
		}
		for _, b := range boosts.Boosts {
			if b.UserID != bob.id && b.UserID != carol.id {
				return fmt.Errorf("boost after the draw %+v", b)
			}
		}
		return nil
	})
}

// TestGiveawayShortfall: fewer people than winners, nobody at all, and a
// channel deleted before the end.
func TestGiveawayShortfall(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")
	k := newGiveawayKit(t, s)
	if _, _, err := s.stars.Adjust(k.ctx, alice.id, alice.id, 100000, "starter", "e2e-giveaway-short"); err != nil {
		t.Fatal(err)
	}
	k.start = k.total()
	short := k.channel(alice, "Short", "shortdraw", bob, carol)
	empty := k.channel(alice, "Empty", "emptydraw")
	gone := k.channel(alice, "Gone", "gonedraw", bob)

	var shortPost, emptyPost, gonePost *tg.Message
	var option tg.PremiumGiftCodeOption
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if _, _, err := pay(ctx, c, starsGiveaway(short, 2500, 5, soon(), nil)); err != nil {
			return fmt.Errorf("five winners among two: %v", err)
		}
		var err error
		if shortPost, _, _, err = giveawayPost(ctx, c, short); err != nil {
			return err
		}
		opts, err := c.PaymentsGetPremiumGiftCodeOptions(ctx, &tg.PaymentsGetPremiumGiftCodeOptionsRequest{})
		if err != nil {
			return err
		}
		for _, o := range opts {
			if o.Users == 3 {
				option = o
				break
			}
		}
		if _, _, err := pay(ctx, c, premiumGiveaway(empty, option, soon(), nil)); err != nil {
			return fmt.Errorf("premium in an empty channel: %v", err)
		}
		if emptyPost, _, _, err = giveawayPost(ctx, c, empty); err != nil {
			return err
		}
		if _, _, err := pay(ctx, c, starsGiveaway(gone, 1000, 1, soon(), nil)); err != nil {
			return err
		}
		if gonePost, _, _, err = giveawayPost(ctx, c, gone); err != nil {
			return err
		}
		if bal, err := starsBalance(ctx, c); err != nil || bal != 100000-2500-option.Amount-1000 {
			return fmt.Errorf("after paying: %d %v", bal, err)
		}
		_, err = c.ChannelsDeleteChannel(ctx, inCh(gone))
		return err
	})
	k.same(0)

	k.draw(short, shortPost.ID)
	k.waitState(empty, emptyPost.ID, "refunded", true)
	k.waitState(gone, gonePost.ID, "refunded", true)
	k.same(0)

	// two winners of five at 500 each; 1500 back. Of the 4 boosts booked
	// over five rows (1, 1, 1, 1, 0) the two won ones stay
	for _, u := range []person{bob, carol} {
		run(t, s.client(u.storage), func(ctx context.Context, c *tg.Client) error {
			if bal, err := starsBalance(ctx, c); err != nil || bal != 500 {
				return fmt.Errorf("short prize %d %v", bal, err)
			}
			return nil
		})
	}
	if n := k.int("SELECT refunded_stars FROM giveaways WHERE channel_id=$1", short.ID); n != 1500 {
		t.Fatalf("short refund %d", n)
	}
	if n := k.int("SELECT count(*) FROM channel_gift_boosts WHERE channel_id=$1", short.ID); n != 2 {
		t.Fatalf("short boosts %d", n)
	}
	if n := k.int("SELECT count(*) FROM channel_gift_boosts WHERE channel_id = ANY($1)", []int64{empty.ID, gone.ID}); n != 0 {
		t.Fatalf("refunded giveaways kept %d boosts", n)
	}
	// the deleted channel's giveaway posts nothing
	if n := k.int("SELECT count(*) FROM channel_messages WHERE channel_id=$1 AND id > $2", gone.ID, gonePost.ID); n != 0 {
		t.Fatalf("posted in a deleted channel: %d", n)
	}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if bal, err := starsBalance(ctx, c); err != nil || bal != 100000-1000 {
			return fmt.Errorf("after the refunds: %d %v", bal, err)
		}
		list, err := channelAll(ctx, c, short)
		if err != nil {
			return err
		}
		if a, ok := list[0].(*tg.MessageService).Action.(*tg.MessageActionGiveawayResults); !ok || a.WinnersCount != 2 || a.UnclaimedCount != 3 {
			return fmt.Errorf("short results %+v", list[0])
		}
		// winners aren't public here: no list
		if _, ok := list[1].(*tg.Message); !ok || list[1].GetID() != shortPost.ID {
			return fmt.Errorf("before the results %+v", list[1])
		}
		list, err = channelAll(ctx, c, empty)
		if err != nil {
			return err
		}
		if a, ok := list[0].(*tg.MessageService).Action.(*tg.MessageActionGiveawayResults); !ok || a.WinnersCount != 0 || a.UnclaimedCount != 3 || a.Stars {
			return fmt.Errorf("empty results %+v", list[0])
		}
		in, err := info(ctx, c, empty, emptyPost.ID)
		if err != nil {
			return err
		}
		if v, ok := in.(*tg.PaymentsGiveawayInfoResults); !ok || !v.Refunded || v.WinnersCount != 0 || v.Winner {
			return fmt.Errorf("empty info %+v", in)
		}
		st, err := c.PaymentsGetStarsTransactions(ctx, &tg.PaymentsGetStarsTransactionsRequest{Peer: &tg.InputPeerSelf{}, Inbound: true, Limit: 10})
		if err != nil {
			return err
		}
		refunds := 0
		for _, h := range st.History {
			if h.Title == "Giveaway refund" {
				refunds++
			}
		}
		if refunds != 3 {
			return fmt.Errorf("refund rows %+v", st.History)
		}
		return nil
	})
}

// TestGiveawayOptions: a Premium giveaway with every setting, each of them
// deciding who may win.
func TestGiveawayOptions(t *testing.T) {
	s := start(t)
	s.api.SetCountries(func(context.Context) ([]api.Country, error) {
		return []api.Country{{ISO2: "UA", Code: "380"}, {ISO2: "US", Code: "1"}, {ISO2: "CA", Code: "1"}}, nil
	})
	alice := s.signUp(t, "alice")
	k := newGiveawayKit(t, s)
	people := map[string]person{}
	for _, n := range []string{"bob", "carol", "dave", "erin", "frank", "gina", "henry"} {
		people[n] = s.signUp(t, n)
	}
	phone := func(p person, number string) {
		enc, err := s.secret.EncryptPhone(number, fmt.Sprintf("user:%d", p.id))
		if err != nil {
			t.Fatal(err)
		}
		k.sql("UPDATE users SET phone=$2 WHERE id=$1", p.id, enc)
	}
	for _, n := range []string{"bob", "carol", "erin", "gina", "henry"} {
		phone(people[n], "+380501234567")
	}
	phone(people["dave"], "+15550100")
	if _, _, err := s.stars.Adjust(k.ctx, alice.id, alice.id, 100000, "starter", "e2e-giveaway-opts"); err != nil {
		t.Fatal(err)
	}
	k.start = k.total()
	mainCh := k.channel(alice, "Main", "mainchan", people["bob"])
	extra := k.channel(alice, "Extra", "extrachan", people["bob"])
	// bob joined before the giveaway started
	k.sql("UPDATE channel_members SET date=date-1000 WHERE user_id=$1", people["bob"].id)

	var option tg.PremiumGiftCodeOption
	var post *tg.Message
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		opts, err := c.PaymentsGetPremiumGiftCodeOptions(ctx, &tg.PaymentsGetPremiumGiftCodeOptionsRequest{})
		if err != nil {
			return err
		}
		for _, o := range opts {
			if o.Users == 3 {
				option = o
				break
			}
		}
		inv := premiumGiveaway(mainCh, option, soon(), func(p *tg.InputStorePaymentPremiumGiveaway) {
			p.AdditionalPeers = []tg.InputPeerClass{peerCh(extra)}
			p.CountriesISO2 = []string{"ua"}
			p.OnlyNewSubscribers = true
			p.WinnersAreVisible = true
			p.PrizeDescription = "stickers"
		})
		if _, _, err := pay(ctx, c, inv); err != nil {
			return err
		}
		var g *tg.MessageMediaGiveaway
		var l *tg.MessageActionGiveawayLaunch
		if post, g, l, err = giveawayPost(ctx, c, mainCh); err != nil {
			return err
		}
		if l.Stars != 0 || g.Months != option.Months || g.Stars != 0 || g.Quantity != 3 || !g.OnlyNewSubscribers || !g.WinnersAreVisible ||
			g.PrizeDescription != "stickers" || len(g.Channels) != 2 || g.Channels[1] != extra.ID || len(g.CountriesISO2) != 1 || g.CountriesISO2[0] != "UA" {
			return fmt.Errorf("giveaway post %+v", g)
		}
		return nil
	})
	// joined after it started: carol (in both, Ukrainian), dave (American),
	// erin (not in Extra), frank (no number), gina (runs Extra), henry (a bot)
	k.join(mainCh, people["carol"], people["dave"], people["erin"], people["frank"], people["gina"], people["henry"])
	k.join(extra, people["carol"], people["dave"], people["frank"], people["gina"], people["henry"])
	k.sql("UPDATE channel_members SET status='admin' WHERE channel_id=$1 AND user_id=$2", extra.ID, people["gina"].id)
	k.sql("UPDATE users SET is_bot=true WHERE id=$1", people["henry"].id)

	check := func(name string, want func(*tg.PaymentsGiveawayInfo) bool) {
		t.Helper()
		run(t, s.client(people[name].storage), func(ctx context.Context, c *tg.Client) error {
			in, err := info(ctx, c, mainCh, post.ID)
			if err != nil {
				return err
			}
			v, ok := in.(*tg.PaymentsGiveawayInfo)
			if !ok || !want(v) {
				return fmt.Errorf("%s's info %+v", name, in)
			}
			return nil
		})
	}
	check("bob", func(v *tg.PaymentsGiveawayInfo) bool { return !v.Participating && v.JoinedTooEarlyDate != 0 })
	check("carol", func(v *tg.PaymentsGiveawayInfo) bool { return v.Participating })
	check("dave", func(v *tg.PaymentsGiveawayInfo) bool { return !v.Participating && v.DisallowedCountry == "US" })
	check("erin", func(v *tg.PaymentsGiveawayInfo) bool { return !v.Participating && v.DisallowedCountry == "" && v.AdminDisallowedChatID == 0 })
	check("frank", func(v *tg.PaymentsGiveawayInfo) bool { return !v.Participating && v.DisallowedCountry == "XX" })
	check("gina", func(v *tg.PaymentsGiveawayInfo) bool { return !v.Participating && v.AdminDisallowedChatID == extra.ID })

	// only carol is in: one winner of three, two thirds back
	premiumBefore := k.int("SELECT premium_until FROM users WHERE id=$1", people["carol"].id)
	k.draw(mainCh, post.ID)
	per := option.Amount / 3
	k.same(per)
	if n := k.int("SELECT count(*) FROM giveaway_winners"); n != 1 {
		t.Fatalf("%d winners", n)
	}
	after := k.int("SELECT premium_until FROM users WHERE id=$1", people["carol"].id)
	if after <= premiumBefore {
		t.Fatalf("carol's Premium %d -> %d", premiumBefore, after)
	}
	run(t, s.client(people["carol"].storage), func(ctx context.Context, c *tg.Client) error {
		m, a, err := serviceAction[*tg.MessageActionGiftCode](ctx, c, 777000)
		if err != nil {
			return err
		}
		if !a.ViaGiveaway || a.Slug == "" || a.BoostPeer.(*tg.PeerChannel).ChannelID != mainCh.ID || a.Days == 0 {
			return fmt.Errorf("gift code %+v", a)
		}
		if from, ok := m.FromID.(*tg.PeerUser); ok && from.UserID != 777000 {
			return fmt.Errorf("gift code from %+v", m.FromID)
		}
		code, err := c.PaymentsCheckGiftCode(ctx, a.Slug)
		if err != nil {
			return err
		}
		if !code.ViaGiveaway || code.ToID != people["carol"].id || code.UsedDate == 0 || code.GiveawayMsgID != post.ID ||
			code.FromID.(*tg.PeerChannel).ChannelID != mainCh.ID {
			return fmt.Errorf("checked code %+v", code)
		}
		in, err := info(ctx, c, mainCh, post.ID)
		if err != nil {
			return err
		}
		if v, ok := in.(*tg.PaymentsGiveawayInfoResults); !ok || !v.Winner || v.GiftCodeSlug != a.Slug || v.ActivatedCount != 1 {
			return fmt.Errorf("winner's info %+v", in)
		}
		return nil
	})
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if bal, err := starsBalance(ctx, c); err != nil || bal != 100000-per {
			return fmt.Errorf("buyer: %d %v", bal, err)
		}
		list, err := channelAll(ctx, c, mainCh)
		if err != nil {
			return err
		}
		res, ok := list[0].(*tg.Message).Media.(*tg.MessageMediaGiveawayResults)
		if !ok || len(res.Winners) != 1 || res.Winners[0] != people["carol"].id || res.UnclaimedCount != 2 || res.Months != option.Months ||
			res.AdditionalPeersCount != 1 || !res.OnlyNewSubscribers {
			return fmt.Errorf("results %+v", list[0])
		}
		return nil
	})
}

// TestGiveawayAfterDowntime: a giveaway whose end passed while no server ran
// is drawn when one starts, and the buyer no longer running the channel
// doesn't stop it.
func TestGiveawayAfterDowntime(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	k := newGiveawayKit(t, s)
	if _, _, err := s.stars.Adjust(k.ctx, alice.id, alice.id, 1000, "starter", "e2e-giveaway-down"); err != nil {
		t.Fatal(err)
	}
	k.start = k.total()
	ch := k.channel(alice, "Later", "laterdraw", bob)
	var post *tg.Message
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if _, _, err := pay(ctx, c, starsGiveaway(ch, 1000, 1, soon(), nil)); err != nil {
			return err
		}
		var err error
		post, _, _, err = giveawayPost(ctx, c, ch)
		return err
	})
	k.sql("UPDATE channel_members SET status='member' WHERE channel_id=$1 AND user_id=$2", ch.ID, alice.id)
	// the end date passed while the server was down
	k.sql("UPDATE giveaways SET until_date=until_date-1000")
	startOn(t, s.db, make([]byte, 32))
	k.waitState(ch, post.ID, "drawn", true)
	deadline := time.Now().Add(20 * time.Second)
	for k.int("SELECT count(*) FROM giveaway_winners WHERE notified") != 1 {
		if time.Now().After(deadline) {
			t.Fatal("no prize message")
		}
		time.Sleep(100 * time.Millisecond)
	}
	k.same(0)
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		if bal, err := starsBalance(ctx, c); err != nil || bal != 1000 {
			return fmt.Errorf("prize %d %v", bal, err)
		}
		return nil
	})
}

// TestGiveawayChosenPeople: Premium for named people is theirs at payment,
// as gift codes from the service account.
func TestGiveawayChosenPeople(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	k := newGiveawayKit(t, s)
	if _, _, err := s.stars.Adjust(k.ctx, alice.id, alice.id, 100000, "starter", "e2e-giveaway-chosen"); err != nil {
		t.Fatal(err)
	}
	ch := k.channel(alice, "Gifts", "giftschan", bob)
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		opts, err := c.PaymentsGetPremiumGiftCodeOptions(ctx, &tg.PaymentsGetPremiumGiftCodeOptionsRequest{})
		if err != nil {
			return err
		}
		var o tg.PremiumGiftCodeOption
		for _, x := range opts {
			if x.Users == 1 {
				o = x
				break
			}
		}
		_, _, err = pay(ctx, c, &tg.InputInvoicePremiumGiftCode{Option: o, Purpose: &tg.InputStorePaymentPremiumGiftCode{
			Users: []tg.InputUserClass{&tg.InputUser{UserID: bob.id}}, BoostPeer: peerCh(ch), Currency: "XTR", Amount: o.Amount}})
		return err
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		_, a, err := serviceAction[*tg.MessageActionGiftCode](ctx, c, 777000)
		if err != nil {
			return err
		}
		if a.ViaGiveaway || a.Slug == "" || a.BoostPeer.(*tg.PeerChannel).ChannelID != ch.ID {
			return fmt.Errorf("gift code %+v", a)
		}
		code, err := c.PaymentsCheckGiftCode(ctx, a.Slug)
		if err != nil {
			return err
		}
		if code.ViaGiveaway || code.ToID != bob.id || code.UsedDate == 0 {
			return fmt.Errorf("checked code %+v", code)
		}
		if n, err := chatSize(ctx, c, alice.id); err != nil || n != 0 {
			return fmt.Errorf("buyer's chat has %d %v", n, err)
		}
		return nil
	})
}

// TestOldGiveawayRows: giveaways drawn at payment before the draw existed
// still read the same.
func TestOldGiveawayRows(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	k := newGiveawayKit(t, s)
	ch := k.channel(alice, "Old", "oldchan", bob)
	now := time.Now().Unix()
	k.sql(`INSERT INTO star_ledger(user_id, amount, reason, reference, created_by, created_at, transaction_id, title, channel_id, post_id)
		VALUES ($1, 1000, 'starter', 'old:1', $1, $3, 'old-t1', '', NULL, 0),
			($1, -1000, 'stars_giveaway', 'stars_giveaway:1:out', $1, $3, 'old-t2', 'Stars giveaway', $4, 7),
			($2, 1000, 'stars_giveaway_prize', 'stars_giveaway:1:2', $1, $3, 'old-t3', 'Stars giveaway', $4, 7)`, alice.id, bob.id, now, ch.ID)
	k.sql(`INSERT INTO channel_gift_boosts(channel_id, user_id, assigned_at, expires_at, drawn, multiplier, stars) VALUES ($1, $2, $3, $4, true, 2, 1000)`,
		ch.ID, bob.id, now, now+365*86400)
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		st, err := c.PaymentsGetStarsTransactions(ctx, &tg.PaymentsGetStarsTransactionsRequest{Peer: &tg.InputPeerSelf{}, Limit: 10})
		if err != nil {
			return err
		}
		if len(st.History) != 1 || st.History[0].GiveawayPostID != 7 || st.History[0].Amount.(*tg.StarsAmount).Amount != 1000 {
			return fmt.Errorf("old prize %+v", st.History)
		}
		return nil
	})
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if bal, err := starsBalance(ctx, c); err != nil || bal != 0 {
			return fmt.Errorf("old buyer %d %v", bal, err)
		}
		list, err := c.PremiumGetBoostsList(ctx, &tg.PremiumGetBoostsListRequest{Peer: peerCh(ch), Gifts: true, Limit: 10})
		if err != nil {
			return err
		}
		if len(list.Boosts) != 1 || list.Boosts[0].UserID != bob.id || list.Boosts[0].Stars != 1000 || list.Boosts[0].Multiplier != 2 {
			return fmt.Errorf("old boosts %+v", list.Boosts)
		}
		st, err := c.PremiumGetBoostsStatus(ctx, peerCh(ch))
		if err != nil {
			return err
		}
		if st.GiftBoosts != 2 {
			return fmt.Errorf("old boost status %+v", st)
		}
		return nil
	})
}

// TestGiveawayWithoutCountryList: before the server has its country list a
// giveaway for some countries waits instead of finding nobody, and doesn't
// hold up the others.
func TestGiveawayWithoutCountryList(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	k := newGiveawayKit(t, s)
	if _, _, err := s.stars.Adjust(k.ctx, alice.id, alice.id, 2000, "starter", "e2e-giveaway-nolist"); err != nil {
		t.Fatal(err)
	}
	k.start = k.total()
	enc, err := s.secret.EncryptPhone("+380501234567", fmt.Sprintf("user:%d", bob.id))
	if err != nil {
		t.Fatal(err)
	}
	k.sql("UPDATE users SET phone=$2 WHERE id=$1", bob.id, enc)
	first := k.channel(alice, "First", "firstdraw", bob)
	second := k.channel(alice, "Second", "seconddraw", bob)
	var firstPost, secondPost *tg.Message
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if _, _, err := pay(ctx, c, starsGiveaway(first, 1000, 1, soon(), func(p *tg.InputStorePaymentStarsGiveaway) {
			p.CountriesISO2 = []string{"UA"}
		})); err != nil {
			return err
		}
		if _, _, err := pay(ctx, c, starsGiveaway(second, 1000, 1, soon()+60, nil)); err != nil {
			return err
		}
		var err error
		if firstPost, _, _, err = giveawayPost(ctx, c, first); err != nil {
			return err
		}
		secondPost, _, _, err = giveawayPost(ctx, c, second)
		return err
	})
	k.draw(second, secondPost.ID)
	k.waitState(first, firstPost.ID, "running", true)
	s.api.SetCountries(func(context.Context) ([]api.Country, error) {
		return []api.Country{{ISO2: "UA", Code: "380"}}, nil
	})
	k.draw(first, firstPost.ID)
	k.waitState(first, firstPost.ID, "drawn", true)
	k.same(0)
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		if bal, err := starsBalance(ctx, c); err != nil || bal != 2000 {
			return fmt.Errorf("prizes %d %v", bal, err)
		}
		return nil
	})
}
