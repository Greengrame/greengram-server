package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
)

// pushed returns the first pushed update match accepts.
func pushed(t *testing.T, got <-chan tg.UpdatesClass, what string, match func(tg.UpdateClass) bool) tg.UpdateClass {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case u := <-got:
			if ups, ok := u.(*tg.Updates); ok {
				for _, x := range ups.Updates {
					if match(x) {
						return x
					}
				}
			}
		case <-deadline:
			t.Fatalf("no push: %s", what)
			return nil
		}
	}
}

func TestPinnedMessages(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	alicePushes := make(chan tg.UpdatesClass, 128)
	aliceLive := s.live(t, alice, alicePushes)
	bobPushes := make(chan tg.UpdatesClass, 128)
	bobLive := s.live(t, bob, bobPushes)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	res, err := aliceLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "pin me", RandomID: 1})
	if err != nil {
		t.Fatal(err)
	}
	inAlice := sentID(t, res)
	inBob := waitNew(t, bobPushes, "pin me").ID
	before, err := bobLive.UpdatesGetState(ctx)
	if err != nil {
		t.Fatal(err)
	}

	up, err := aliceLive.MessagesUpdatePinnedMessage(ctx, &tg.MessagesUpdatePinnedMessageRequest{Peer: userPeer(bob.id), ID: inAlice, Silent: true})
	if err != nil {
		t.Fatal(err)
	}
	var sawPin, sawNote bool
	for _, u := range up.(*tg.Updates).Updates {
		switch v := u.(type) {
		case *tg.UpdatePinnedMessages:
			sawPin = v.Pinned && v.Messages[0] == inAlice
		case *tg.UpdateNewMessage:
			s, ok := v.Message.(*tg.MessageService)
			sawNote = ok && s.Silent && s.ReplyTo.(*tg.MessageReplyHeader).ReplyToMsgID == inAlice
		}
	}
	if !sawPin || !sawNote {
		t.Fatalf("pin answer %+v", up)
	}
	pushed(t, bobPushes, "bob's pin", func(u tg.UpdateClass) bool {
		p, ok := u.(*tg.UpdatePinnedMessages)
		return ok && p.Pinned && p.Messages[0] == inBob && p.Peer.(*tg.PeerUser).UserID == alice.id
	})
	pushed(t, bobPushes, "bob's pin note", func(u tg.UpdateClass) bool {
		n, ok := u.(*tg.UpdateNewMessage)
		if !ok {
			return false
		}
		s, ok := n.Message.(*tg.MessageService)
		return ok && s.ReplyTo.(*tg.MessageReplyHeader).ReplyToMsgID == inBob
	})
	// the pin and the note are in Bob's journal, two pts
	diff, err := bobLive.UpdatesGetDifference(ctx, &tg.UpdatesGetDifferenceRequest{Pts: before.Pts, Date: before.Date})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := diff.(*tg.UpdatesDifference)
	if !ok || len(d.NewMessages) != 1 || len(d.OtherUpdates) != 1 || d.State.Pts != before.Pts+2 {
		t.Fatalf("bob's difference %+v", diff)
	}

	full, err := bobLive.UsersGetFullUser(ctx, &tg.InputUser{UserID: alice.id})
	if err != nil {
		t.Fatal(err)
	}
	if full.FullUser.PinnedMsgID != inBob {
		t.Fatalf("pinned_msg_id %d, want %d", full.FullUser.PinnedMsgID, inBob)
	}
	found, err := bobLive.MessagesSearch(ctx, &tg.MessagesSearchRequest{Peer: userPeer(alice.id), Filter: &tg.InputMessagesFilterPinned{}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if ms := found.(*tg.MessagesMessagesSlice).Messages; len(ms) != 1 || !ms[0].(*tg.Message).Pinned {
		t.Fatalf("pinned search %+v", ms)
	}

	// unpin all, from the other side: both sides lose it
	aff, err := bobLive.MessagesUnpinAllMessages(ctx, &tg.MessagesUnpinAllMessagesRequest{Peer: userPeer(alice.id)})
	if err != nil || aff.PtsCount != 1 {
		t.Fatalf("unpinAll %v %+v", err, aff)
	}
	pushed(t, alicePushes, "alice's unpin", func(u tg.UpdateClass) bool {
		p, ok := u.(*tg.UpdatePinnedMessages)
		return ok && !p.Pinned && p.Messages[0] == inAlice
	})

	// one-sided: Bob hears nothing, his side stays unpinned
	if _, err := aliceLive.MessagesUpdatePinnedMessage(ctx, &tg.MessagesUpdatePinnedMessageRequest{Peer: userPeer(bob.id), ID: inAlice, PmOneside: true}); err != nil {
		t.Fatal(err)
	}
	full, err = bobLive.UsersGetFullUser(ctx, &tg.InputUser{UserID: alice.id})
	if err != nil || full.FullUser.PinnedMsgID != 0 {
		t.Fatalf("one-sided pin reached bob: %v %d", err, full.FullUser.PinnedMsgID)
	}
	afull, err := aliceLive.UsersGetFullUser(ctx, &tg.InputUser{UserID: bob.id})
	if err != nil || afull.FullUser.PinnedMsgID != inAlice {
		t.Fatalf("alice's own pin: %v %d", err, afull.FullUser.PinnedMsgID)
	}

	// group
	cr, err := aliceLive.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Team", Users: []tg.InputUserClass{&tg.InputUser{UserID: bob.id}}})
	if err != nil {
		t.Fatal(err)
	}
	group := channelOf(t, cr.Updates)
	if _, err := aliceLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "rules", RandomID: 2}); err != nil {
		t.Fatal(err)
	}
	post := waitChannelText(t, bobPushes, "rules")
	if _, err := aliceLive.MessagesUpdatePinnedMessage(ctx, &tg.MessagesUpdatePinnedMessageRequest{Peer: peerCh(group), ID: post.ID}); err != nil {
		t.Fatal(err)
	}
	pushed(t, bobPushes, "group pin", func(u tg.UpdateClass) bool {
		p, ok := u.(*tg.UpdatePinnedChannelMessages)
		return ok && p.Pinned && p.ChannelID == group.ID && p.Messages[0] == post.ID
	})
	cf, err := bobLive.ChannelsGetFullChannel(ctx, inCh(group))
	if err != nil {
		t.Fatal(err)
	}
	if id := cf.FullChat.(*tg.ChannelFull).PinnedMsgID; id != post.ID {
		t.Fatalf("channel pinned_msg_id %d", id)
	}
	if _, err := aliceLive.MessagesUnpinAllMessages(ctx, &tg.MessagesUnpinAllMessagesRequest{Peer: peerCh(group)}); err != nil {
		t.Fatal(err)
	}
	pushed(t, bobPushes, "group unpin", func(u tg.UpdateClass) bool {
		p, ok := u.(*tg.UpdatePinnedChannelMessages)
		return ok && !p.Pinned && p.Messages[0] == post.ID
	})
}
