package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
)

func waitDraft(t *testing.T, got <-chan tg.UpdatesClass, what string, match func(*tg.UpdateDraftMessage) bool) {
	t.Helper()
	pushed(t, got, what, func(u tg.UpdateClass) bool {
		d, ok := u.(*tg.UpdateDraftMessage)
		return ok && match(d)
	})
}

func TestDrafts(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	// Alice's phone keeps listening while her other app types
	phone := make(chan tg.UpdatesClass, 128)
	aliceLive := s.live(t, alice, phone)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cr, err := aliceLive.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Team", Users: []tg.InputUserClass{&tg.InputUser{UserID: bob.id}}})
	if err != nil {
		t.Fatal(err)
	}
	group := channelOf(t, cr.Updates)

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.MessagesSaveDraft(ctx, &tg.MessagesSaveDraftRequest{Peer: userPeer(bob.id), Message: "half typed"}); err != nil {
			return err
		}
		_, err := c.MessagesSaveDraft(ctx, &tg.MessagesSaveDraftRequest{Peer: peerCh(group), Message: "group draft"})
		return err
	})
	waitDraft(t, phone, "private draft", func(d *tg.UpdateDraftMessage) bool {
		dm, ok := d.Draft.(*tg.DraftMessage)
		return ok && dm.Message == "half typed" && d.Peer.(*tg.PeerUser).UserID == bob.id
	})
	waitDraft(t, phone, "group draft", func(d *tg.UpdateDraftMessage) bool {
		dm, ok := d.Draft.(*tg.DraftMessage)
		return ok && dm.Message == "group draft"
	})

	pd, err := aliceLive.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: userPeer(bob.id)}, &tg.InputDialogPeer{Peer: peerCh(group)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range pd.Dialogs {
		if dm, ok := d.(*tg.Dialog).Draft.(*tg.DraftMessage); !ok || dm.Message == "" {
			t.Fatalf("dialog without its draft %+v", d)
		}
	}
	all, err := aliceLive.MessagesGetAllDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(all.(*tg.Updates).Updates); n != 2 {
		t.Fatalf("getAllDrafts: %d", n)
	}

	// sending with clear_draft takes it away everywhere
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "half typed, now sent", RandomID: 1, ClearDraft: true})
		return err
	})
	waitDraft(t, phone, "cleared draft", func(d *tg.UpdateDraftMessage) bool {
		_, ok := d.Draft.(*tg.DraftMessageEmpty)
		return ok && d.Peer.(*tg.PeerUser).UserID == bob.id
	})
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.MessagesClearAllDrafts(ctx); err != nil {
			return err
		}
		all, err := c.MessagesGetAllDrafts(ctx)
		if err != nil {
			return err
		}
		if n := len(all.(*tg.Updates).Updates); n != 0 {
			return fmt.Errorf("drafts after clearAllDrafts: %d", n)
		}
		return nil
	})
	waitDraft(t, phone, "group draft cleared", func(d *tg.UpdateDraftMessage) bool {
		_, ok := d.Draft.(*tg.DraftMessageEmpty)
		_, ch := d.Peer.(*tg.PeerChannel)
		return ok && ch
	})
}
