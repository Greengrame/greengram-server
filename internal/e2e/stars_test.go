package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/admin"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/stars"
)

func starsBalance(ctx context.Context, c *tg.Client) (int64, error) {
	st, err := c.PaymentsGetStarsStatus(ctx, &tg.PaymentsGetStarsStatusRequest{Peer: &tg.InputPeerSelf{}})
	if err != nil {
		return 0, err
	}
	return st.Balance.(*tg.StarsAmount).Amount, nil
}

func selfPremium(ctx context.Context, c *tg.Client) (bool, error) {
	us, err := c.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
	if err != nil || len(us) != 1 {
		return false, fmt.Errorf("self: %v", err)
	}
	return us[0].(*tg.User).Premium, nil
}

func pay(ctx context.Context, c *tg.Client, inv tg.InputInvoiceClass) (int64, int64, error) {
	form, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
	if err != nil {
		return 0, 0, err
	}
	var id, price int64
	switch f := form.(type) {
	case *tg.PaymentsPaymentFormStars:
		id, price = f.FormID, f.Invoice.Prices[0].Amount
	case *tg.PaymentsPaymentFormStarGift:
		id, price = f.FormID, f.Invoice.Prices[0].Amount
	default:
		return 0, 0, fmt.Errorf("form %T", form)
	}
	_, err = c.PaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: id, Invoice: inv})
	return id, price, err
}

func TestStarsPremiumGiftsBoosts(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	ctx := context.Background()
	if _, _, err := s.stars.Adjust(ctx, alice.id, alice.id, 5000, "starter", "e2e-request-1"); err != nil {
		t.Fatal(err)
	}
	// a retried admin request doesn't pay twice, a reused id is refused
	if _, bal, err := s.stars.Adjust(ctx, alice.id, alice.id, 5000, "starter", "e2e-request-1"); err != nil || bal != 5000 {
		t.Fatalf("replayed adjust: %d %v", bal, err)
	}
	if _, _, err := s.stars.Adjust(ctx, alice.id, alice.id, 10, "other", "e2e-request-1"); err == nil {
		t.Fatal("reused request id accepted")
	}
	gift, err := s.stars.AddGift(ctx, stars.Gift{Title: "Rose", Emoji: "🌹", Stars: 100, ConvertStars: 80, Limited: true, Stock: 2})
	if err != nil {
		t.Fatal(err)
	}
	bobUser := &tg.InputUser{UserID: bob.id}

	var channel *tg.Channel
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if bal, err := starsBalance(ctx, c); err != nil || bal != 5000 {
			return fmt.Errorf("start balance %d %v", bal, err)
		}
		if p, err := selfPremium(ctx, c); err != nil || p {
			return fmt.Errorf("premium before buying: %v %v", p, err)
		}

		// a month for herself: 250 from settings
		sub := &tg.InputInvoiceStars{Purpose: &tg.InputStorePaymentPremiumSubscription{}}
		id, price, err := pay(ctx, c, sub)
		if err != nil || price != 250 {
			return fmt.Errorf("buy premium: %d %v", price, err)
		}
		if _, err := c.PaymentsSendStarsForm(ctx, &tg.PaymentsSendStarsFormRequest{FormID: id, Invoice: sub}); !tgerr.Is(err, "FORM_SUBMIT_DUPLICATE") {
			return fmt.Errorf("paid twice: %v", err)
		}
		if p, err := selfPremium(ctx, c); err != nil || !p {
			return fmt.Errorf("premium after buying: %v %v", p, err)
		}

		// three months for bob, then a plan she can't afford
		if _, price, err := pay(ctx, c, &tg.InputInvoicePremiumGiftStars{UserID: bobUser, Months: 3, Message: tg.TextWithEntities{Text: "enjoy"}}); err != nil || price != 675 {
			return fmt.Errorf("gift premium: %d %v", price, err)
		}
		if _, _, err := pay(ctx, c, &tg.InputInvoicePremiumGiftStars{UserID: bobUser, Months: 120}); !tgerr.Is(err, "BALANCE_TOO_LOW") {
			return fmt.Errorf("unaffordable plan: %v", err)
		}

		gifts, err := c.PaymentsGetStarGifts(ctx, 0)
		if err != nil {
			return err
		}
		// the seeded ones plus the Rose. Web A can't draw a gift without a
		// sticker; no packs are imported here, so these are the plain box.
		list := gifts.(*tg.PaymentsStarGifts).Gifts
		if len(list) != 39 {
			return fmt.Errorf("catalog %+v", list)
		}
		for _, g := range list {
			g := g.(*tg.StarGift)
			doc, ok := g.Sticker.(*tg.Document)
			if !ok || doc.MimeType != "application/x-tgsticker" {
				return fmt.Errorf("gift %d artwork %+v", g.ID, g.Sticker)
			}
			isSticker := false
			for _, a := range doc.Attributes {
				_, ok := a.(*tg.DocumentAttributeSticker)
				isSticker = isSticker || ok
			}
			if !isSticker {
				return fmt.Errorf("gift %d attributes %+v", g.ID, doc.Attributes)
			}
			if g.ID == gift.ID && g.AvailabilityRemains != 2 {
				return fmt.Errorf("rose stock %+v", g)
			}
		}
		send := &tg.InputInvoiceStarGift{Peer: &tg.InputPeerUser{UserID: bob.id}, GiftID: gift.ID, Message: tg.TextWithEntities{Text: "for you"}}
		if _, price, err := pay(ctx, c, send); err != nil || price != 100 {
			return fmt.Errorf("send gift: %d %v", price, err)
		}
		if bal, err := starsBalance(ctx, c); err != nil || bal != 5000-250-675-100 {
			return fmt.Errorf("balance after spending %d %v", bal, err)
		}
		// the gift's message and the catalog both count this one as taken
		if _, act, err := serviceAction[*tg.MessageActionStarGift](ctx, c, bob.id); err != nil || act.Gift.(*tg.StarGift).AvailabilityRemains != 1 {
			return fmt.Errorf("gift message stock %+v %v", act, err)
		}
		again, err := c.PaymentsGetStarGifts(ctx, gifts.(*tg.PaymentsStarGifts).Hash)
		if err != nil {
			return err
		}
		fresh, ok := again.(*tg.PaymentsStarGifts)
		if !ok || fresh.Hash == gifts.(*tg.PaymentsStarGifts).Hash {
			return fmt.Errorf("catalog after a purchase %+v", again)
		}
		for _, g := range fresh.Gifts {
			if g := g.(*tg.StarGift); g.ID == gift.ID && g.AvailabilityRemains != 1 {
				return fmt.Errorf("rose stock after a purchase %+v", g)
			}
		}
		// Web A's balance modal only ever shows what the status carries
		status, err := c.PaymentsGetStarsStatus(ctx, &tg.PaymentsGetStarsStatusRequest{Peer: &tg.InputPeerSelf{}})
		if err != nil {
			return err
		}
		if len(status.History) == 0 {
			return fmt.Errorf("status without history")
		}
		hist, err := c.PaymentsGetStarsTransactions(ctx, &tg.PaymentsGetStarsTransactionsRequest{Peer: &tg.InputPeerSelf{}, Outbound: true, Limit: 2})
		if err != nil {
			return err
		}
		if len(hist.History) != 2 || hist.NextOffset == "" || !hist.History[0].Gift || hist.History[1].PremiumGiftMonths != 3 {
			return fmt.Errorf("outbound history %+v", hist)
		}

		// boosts: Premium only, channels and supergroups alike
		res, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: true, Title: "Boosted"})
		if err != nil {
			return err
		}
		channel = channelOf(t, res)
		if _, err := c.ChannelsUpdateUsername(ctx, &tg.ChannelsUpdateUsernameRequest{Channel: inCh(channel), Username: "boosted"}); err != nil {
			return err
		}
		mine, err := c.PremiumApplyBoost(ctx, &tg.PremiumApplyBoostRequest{Peer: peerCh(channel), Slots: []int{1}})
		if err != nil {
			return err
		}
		if len(mine.MyBoosts) < 4 || mine.MyBoosts[0].Peer == nil {
			return fmt.Errorf("my boosts %+v", mine.MyBoosts)
		}
		group, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Megagroup: true, Title: "Chat"})
		if err != nil {
			return err
		}
		if _, err := c.PremiumApplyBoost(ctx, &tg.PremiumApplyBoostRequest{Peer: peerCh(channelOf(t, group)), Slots: []int{2}}); err != nil {
			return fmt.Errorf("boosting a group: %v", err)
		}
		gst, err := c.PremiumGetBoostsStatus(ctx, peerCh(channelOf(t, group)))
		if err != nil || gst.Boosts != 1 || !gst.MyBoost {
			return fmt.Errorf("group boost status %+v %v", gst, err)
		}
		// the slot is on another channel now: a day's cooldown
		if _, err := c.PremiumApplyBoost(ctx, &tg.PremiumApplyBoostRequest{Peer: peerCh(channel), Slots: []int{1}}); err != nil {
			return fmt.Errorf("same channel again: %v", err)
		}
		return nil
	})

	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		if p, err := selfPremium(ctx, c); err != nil || !p {
			return fmt.Errorf("bob's gifted premium: %v %v", p, err)
		}
		if _, act, err := serviceAction[*tg.MessageActionStarGift](ctx, c, alice.id); err != nil || act.Message.Text != "for you" {
			return fmt.Errorf("gift message %+v %v", act, err)
		}
		if _, act, err := serviceAction[*tg.MessageActionGiftPremium](ctx, c, alice.id); err != nil || act.Message.Text != "enjoy" {
			return fmt.Errorf("premium message %+v %v", act, err)
		}
		saved, err := c.PaymentsGetSavedStarGifts(ctx, &tg.PaymentsGetSavedStarGiftsRequest{Peer: &tg.InputPeerSelf{}, Limit: 10})
		if err != nil {
			return err
		}
		if saved.Count != 1 || saved.Gifts[0].ConvertStars != 80 || saved.Gifts[0].MsgID == 0 {
			return fmt.Errorf("bob's gifts %+v", saved)
		}
		in := &tg.InputSavedStarGiftUser{MsgID: saved.Gifts[0].MsgID}
		if _, err := c.PaymentsSaveStarGift(ctx, &tg.PaymentsSaveStarGiftRequest{Unsave: true, Stargift: in}); err != nil {
			return err
		}
		full, err := c.UsersGetFullUser(ctx, &tg.InputUserSelf{})
		if err != nil {
			return err
		}
		if full.FullUser.StargiftsCount != 0 {
			return fmt.Errorf("hidden gift still counted: %d", full.FullUser.StargiftsCount)
		}
		if _, err := c.PaymentsConvertStarGift(ctx, in); err != nil {
			return err
		}
		if _, err := c.PaymentsConvertStarGift(ctx, in); err == nil {
			return fmt.Errorf("converted twice")
		}
		if bal, err := starsBalance(ctx, c); err != nil || bal != 80 {
			return fmt.Errorf("bob after converting %d %v", bal, err)
		}

		if _, err := c.ChannelsJoinChannel(ctx, inCh(channel)); err != nil {
			return err
		}
		if _, err := c.PremiumApplyBoost(ctx, &tg.PremiumApplyBoostRequest{Peer: peerCh(channel), Slots: []int{1, 2}}); err != nil {
			return err
		}
		st, err := c.PremiumGetBoostsStatus(ctx, peerCh(channel))
		if err != nil {
			return err
		}
		// three boosts in a small channel: a level each
		if st.Boosts != 3 || st.Level != 3 || st.CurrentLevelBoosts != 3 || st.NextLevelBoosts != 4 || !st.MyBoost || len(st.MyBoostSlots) != 2 {
			return fmt.Errorf("boost status %+v", st)
		}
		return nil
	})

	// alice's rating counts what she spent on others' gifts and Premium,
	// less 85% of the 100 Star gift bob cashed in
	r, err := s.stars.ProfileRating(ctx, alice.id)
	if err != nil || r.Stars != 775-85 || r.Level != 1 {
		t.Fatalf("rating %+v %v", r, err)
	}
	// cashing in someone else's gift costs bob nothing
	if r, err := s.stars.ProfileRating(ctx, bob.id); err != nil || r.Stars != 0 || r.Level != 0 {
		t.Fatalf("bob's rating %+v %v", r, err)
	}
}

// defaultPremiumDays is the web's "Welcome Premium": every new account
// starts with that many days.
func TestWelcomePremium(t *testing.T) {
	s := start(t)
	if _, err := s.db.Pool.Exec(context.Background(), `INSERT INTO instance_settings(key, value) VALUES ('defaultPremiumDays', '30')`); err != nil {
		t.Fatal(err)
	}
	carol := s.signUp(t, "carol")
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		if p, err := selfPremium(ctx, c); err != nil || !p {
			return fmt.Errorf("welcome premium: %v %v", p, err)
		}
		promo, err := c.HelpGetPremiumPromo(ctx)
		if err != nil {
			return err
		}
		if len(promo.PeriodOptions) != 8 || promo.PeriodOptions[4].Amount != 4410 {
			return fmt.Errorf("plans %+v", promo.PeriodOptions)
		}
		return nil
	})
	u, _, err := s.accounts.User(context.Background(), carol.id)
	if err != nil || u.PremiumUntil < time.Now().Add(29*24*time.Hour).Unix() {
		t.Fatalf("premium until %d %v", u.PremiumUntil, err)
	}
}

// TestAdminStarTransactions is the panel's Stars dialog and journal
// (admin/dialogs/stars-dialog.jsx, views/stars.jsx).
func TestAdminStarTransactions(t *testing.T) {
	s := start(t)
	boss := s.browser(t)
	boss.must("/api/register", map[string]string{"username": "boss", "name": "Boss", "password": "correct horse boss"})
	bob := s.signUp(t, "bob")
	adm := admin.New(s.db.Pool, s.accounts, s.settings, channels.New(s.db.Pool))
	adm.StarsAdjust = s.stars.Adjust
	adm.Register(s.mux)
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

	tx := map[string]any{"recipientId": bob.id, "amount": 250, "note": "community reward", "requestId": "4b1c9a52-0e5f-4a6e-9a51-8f3c1d2e7b10"}
	code, out := call("POST", "/api/admin/star-transactions", tx)
	if code != 200 || out["balance"] != 250.0 || out["amount"] != 250.0 || out["recipientId"] != float64(bob.id) || out["transactionId"] == "" {
		t.Fatalf("credit: %d %v", code, out)
	}
	if code, again := call("POST", "/api/admin/star-transactions", tx); code != 200 || again["transactionId"] != out["transactionId"] || again["balance"] != 250.0 {
		t.Fatalf("retry: %d %v", code, again)
	}
	tx["amount"] = 1
	if code, bad := call("POST", "/api/admin/star-transactions", tx); code != 409 || bad["error"] != "This request ID belongs to another transaction." {
		t.Fatalf("reused request id: %d %v", code, bad)
	}
	if code, bad := call("POST", "/api/admin/star-transactions", map[string]any{"recipientId": bob.id, "amount": -1000, "note": "", "requestId": "debit-too-much"}); code != 400 || bad["error"] != "The resulting Stars balance is invalid." {
		t.Fatalf("overdraw: %d %v", code, bad)
	}
	// the old PATCH action goes the same way and says why it refused
	if code, bad := call("PATCH", "/api/admin", map[string]any{"action": "stars", "id": bob.id, "amount": 5, "reference": "x"}); code != 400 {
		t.Fatalf("PATCH stars with a bad reference: %d %v", code, bad)
	}

	_, list := call("GET", "/api/admin", nil)
	journal := list["starTransactions"].([]any)
	if len(journal) != 1 {
		t.Fatalf("journal %v", journal)
	}
	e := journal[0].(map[string]any)
	if e["transactionId"] != out["transactionId"] || e["amount"] != 250.0 || e["username"] != "bob" || e["note"] != "community reward" || e["created_at"].(float64) < 1 {
		t.Fatalf("journal entry %v", e)
	}
	var audited int
	s.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_log WHERE action='stars_transaction'").Scan(&audited)
	if audited != 1 {
		t.Fatalf("stars audit entries: %d", audited)
	}
}

// A rating is only sent once there is one, except on your own profile.
func TestRatingOnProfiles(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		other, err := c.UsersGetFullUser(ctx, &tg.InputUser{UserID: bob.id})
		if err != nil {
			return err
		}
		if _, ok := other.FullUser.GetStarsRating(); ok {
			return fmt.Errorf("rating on a profile without one: %+v", other.FullUser.StarsRating)
		}
		mine, err := c.UsersGetFullUser(ctx, &tg.InputUserSelf{})
		if err != nil {
			return err
		}
		if _, ok := mine.FullUser.GetStarsRating(); !ok {
			return fmt.Errorf("no rating on my own profile")
		}
		return nil
	})
}
