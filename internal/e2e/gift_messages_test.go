package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/builtin"
	"github.com/Greengrame/greengram-server/internal/stars"
)

// serviceAction finds the service message with this action type in a history.
func serviceAction[T tg.MessageActionClass](ctx context.Context, c *tg.Client, peer int64) (*tg.MessageService, T, error) {
	var zero T
	h, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: userPeer(peer), Limit: 50})
	if err != nil {
		return nil, zero, err
	}
	for _, m := range h.(*tg.MessagesMessagesSlice).Messages {
		if s, ok := m.(*tg.MessageService); ok {
			if a, ok := s.Action.(T); ok {
				return s, a, nil
			}
		}
	}
	return nil, zero, fmt.Errorf("no %T in history with %d", zero, peer)
}

func TestGiftsAreServiceMessages(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, _, err := s.stars.Adjust(ctx, alice.id, alice.id, 5000, "starter", "gift-messages-1"); err != nil {
		t.Fatal(err)
	}
	gift, err := s.stars.AddGift(ctx, stars.Gift{Title: "Rose", Emoji: "🌹", Stars: 100, ConvertStars: 80})
	if err != nil {
		t.Fatal(err)
	}

	bobPushes := make(chan tg.UpdatesClass, 64)
	bobLive := s.live(t, bob, bobPushes)
	if _, err := bobLive.UpdatesGetState(ctx); err != nil {
		t.Fatal(err)
	}

	bobUser := &tg.InputUser{UserID: bob.id}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		send := &tg.InputInvoiceStarGift{Peer: &tg.InputPeerUser{UserID: bob.id}, GiftID: gift.ID, Message: tg.TextWithEntities{Text: "for you"}}
		if _, price, err := pay(ctx, c, send); err != nil || price != 100 {
			return fmt.Errorf("star gift: %d %v", price, err)
		}
		if _, price, err := pay(ctx, c, &tg.InputInvoicePremiumGiftStars{UserID: bobUser, Months: 3, Message: tg.TextWithEntities{Text: "enjoy"}}); err != nil || price != 675 {
			return fmt.Errorf("premium gift: %d %v", price, err)
		}
		starsGift := &tg.InputInvoiceStars{Purpose: &tg.InputStorePaymentStarsGift{UserID: bobUser, Stars: 500, Currency: "XTR", Amount: 500}}
		if _, price, err := pay(ctx, c, starsGift); err != nil || price != 500 {
			return fmt.Errorf("stars gift: %d %v", price, err)
		}
		if bal, err := starsBalance(ctx, c); err != nil || bal != 5000-100-675-500 {
			return fmt.Errorf("sender's balance %d %v", bal, err)
		}

		// the sender's copies, as a reload would read them
		m, act, err := serviceAction[*tg.MessageActionStarGift](ctx, c, bob.id)
		if err != nil {
			return err
		}
		if !m.Out || act.Saved || act.Message.Text != "for you" || act.ConvertStars != 80 {
			return fmt.Errorf("sender's gift %+v %+v", m, act)
		}
		if doc, ok := act.Gift.(*tg.StarGift).Sticker.(*tg.Document); !ok || doc.MimeType == "" {
			return fmt.Errorf("gift artwork %+v", act.Gift)
		}
		pm, pa, err := serviceAction[*tg.MessageActionGiftPremium](ctx, c, bob.id)
		if err != nil {
			return err
		}
		if !pm.Out || pa.Currency != "XTR" || pa.Amount != 675 || pa.Days == 0 || pa.Message.Text != "enjoy" {
			return fmt.Errorf("sender's premium %+v %+v", pm, pa)
		}
		sm, sa, err := serviceAction[*tg.MessageActionGiftStars](ctx, c, bob.id)
		if err != nil {
			return err
		}
		if !sm.Out || sa.Currency != "XTR" || sa.Stars != 500 || sa.TransactionID == "" {
			return fmt.Errorf("sender's stars %+v %+v", sm, sa)
		}
		return nil
	})

	// the recipient got all three pushed, with the sender to name
	seen := map[string]bool{}
	for len(seen) < 3 {
		select {
		case <-ctx.Done():
			t.Fatalf("pushes so far: %v", seen)
		case u := <-bobPushes:
			up, ok := u.(*tg.Updates)
			if !ok {
				continue
			}
			for _, one := range up.Updates {
				nm, ok := one.(*tg.UpdateNewMessage)
				if !ok {
					continue
				}
				svc, ok := nm.Message.(*tg.MessageService)
				if !ok {
					t.Fatalf("a gift came as %T", nm.Message)
				}
				if svc.Out {
					t.Fatalf("recipient's copy is outgoing: %+v", svc)
				}
				named := false
				for _, us := range up.Users {
					named = named || us.GetID() == alice.id
				}
				if !named {
					t.Fatalf("push doesn't carry the sender: %+v", up.Users)
				}
				seen[fmt.Sprintf("%T", svc.Action)] = true
			}
		}
	}

	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		m, act, err := serviceAction[*tg.MessageActionStarGift](ctx, c, alice.id)
		if err != nil {
			return err
		}
		if m.Out || !act.Saved || act.Converted || act.Message.Text != "for you" {
			return fmt.Errorf("recipient's gift %+v %+v", m, act)
		}
		if _, sa, err := serviceAction[*tg.MessageActionGiftStars](ctx, c, alice.id); err != nil || sa.Stars != 500 {
			return fmt.Errorf("recipient's stars %+v %v", sa, err)
		}
		if bal, err := starsBalance(ctx, c); err != nil || bal != 500 {
			return fmt.Errorf("recipient's balance %d %v", bal, err)
		}

		// the chat list's preview is the newest of them, still a service message
		d, err := c.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 10})
		if err != nil {
			return err
		}
		ds, ok := d.(*tg.MessagesDialogs)
		if !ok || len(ds.Messages) == 0 {
			return fmt.Errorf("dialogs %T %+v", d, d)
		}
		if _, ok := ds.Messages[0].(*tg.MessageService); !ok {
			return fmt.Errorf("preview is %T", ds.Messages[0])
		}

		// the buttons act on the message's own id
		in := &tg.InputSavedStarGiftUser{MsgID: m.ID}
		if _, err := c.PaymentsSaveStarGift(ctx, &tg.PaymentsSaveStarGiftRequest{Unsave: true, Stargift: in}); err != nil {
			return err
		}
		if _, act, err = serviceAction[*tg.MessageActionStarGift](ctx, c, alice.id); err != nil || act.Saved {
			return fmt.Errorf("hidden gift still saved: %+v %v", act, err)
		}
		if _, err := c.PaymentsConvertStarGift(ctx, in); err != nil {
			return err
		}
		if _, act, err = serviceAction[*tg.MessageActionStarGift](ctx, c, alice.id); err != nil || !act.Converted {
			return fmt.Errorf("converted gift %+v %v", act, err)
		}
		if bal, err := starsBalance(ctx, c); err != nil || bal != 580 {
			return fmt.Errorf("after converting %d %v", bal, err)
		}
		if _, err := c.PaymentsConvertStarGift(ctx, in); err == nil {
			return fmt.Errorf("converted twice")
		}
		// it stays a gift, not something you can edit into text
		if _, err := c.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{Peer: userPeer(alice.id), ID: m.ID, Message: "edited"}); !tgerr.Is(err, "MESSAGE_ID_INVALID", "MESSAGE_AUTHOR_REQUIRED") {
			return fmt.Errorf("editing a gift: %v", err)
		}
		return nil
	})

	// only the owner's copy changed
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		_, act, err := serviceAction[*tg.MessageActionStarGift](ctx, c, bob.id)
		if err != nil || act.Converted {
			return fmt.Errorf("sender's copy %+v %v", act, err)
		}
		// a gift to yourself is one message, the recipient's copy
		self := &tg.InputInvoiceStarGift{Peer: &tg.InputPeerSelf{}, GiftID: gift.ID}
		if _, _, err := pay(ctx, c, self); err != nil {
			return err
		}
		_, own, err := serviceAction[*tg.MessageActionStarGift](ctx, c, alice.id)
		if err != nil || !own.Saved {
			return fmt.Errorf("gift to self %+v %v", own, err)
		}
		return nil
	})
}

func TestPremiumGiftStickerSet(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.MessagesGetStickerSet(ctx, &tg.MessagesGetStickerSetRequest{Stickerset: &tg.InputStickerSetPremiumGifts{}})
		if err != nil {
			return err
		}
		set, ok := res.(*tg.MessagesStickerSet)
		if !ok || len(set.Documents) != 5 {
			return fmt.Errorf("set %T %+v", res, res)
		}
		// Web A finds each picture by its alt text
		for i, d := range set.Documents {
			want := fmt.Sprintf("%d️⃣", i+1)
			found := false
			for _, a := range d.(*tg.Document).Attributes {
				if st, ok := a.(*tg.DocumentAttributeSticker); ok {
					found = st.Alt == want
				}
			}
			if !found {
				return fmt.Errorf("document %d doesn't say %s: %+v", i, want, d)
			}
		}
		return nil
	})
}

// The payment accounts have pictures, so Confirm Your Purchase has more than
// a letter to show.
func TestPaymentAccountPictures(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	if err := builtin.SeedSystemAvatars(context.Background(), s.stars, s.media); err != nil {
		t.Fatal(err)
	}
	// a second start leaves them alone
	if err := builtin.SeedSystemAvatars(context.Background(), s.stars, s.media); err != nil {
		t.Fatal(err)
	}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		bobUser := &tg.InputUser{UserID: bob.id}
		for name, inv := range map[string]tg.InputInvoiceClass{
			"premium": &tg.InputInvoicePremiumGiftStars{UserID: bobUser, Months: 1},
			"stars":   &tg.InputInvoiceStars{Purpose: &tg.InputStorePaymentStarsGift{UserID: bobUser, Stars: 100, Currency: "XTR", Amount: 100}},
		} {
			form, err := c.PaymentsGetPaymentForm(ctx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
			if err != nil {
				return fmt.Errorf("%s form: %w", name, err)
			}
			f := form.(*tg.PaymentsPaymentFormStars)
			if len(f.Users) != 1 {
				return fmt.Errorf("%s form users %+v", name, f.Users)
			}
			if _, ok := f.Users[0].(*tg.User).Photo.(*tg.UserProfilePhoto); !ok {
				return fmt.Errorf("%s bot has no picture: %+v", name, f.Users[0])
			}
		}
		return nil
	})
}
