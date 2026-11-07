package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
)

func unreadMentions(ctx context.Context, t *testing.T, c *tg.Client, ch *tg.Channel) int {
	t.Helper()
	pd, err := c.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: peerCh(ch)}})
	if err != nil {
		t.Fatal(err)
	}
	return pd.Dialogs[0].(*tg.Dialog).UnreadMentionsCount
}

func TestMentions(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	bobPushes := make(chan tg.UpdatesClass, 128)
	bobLive := s.live(t, bob, bobPushes)
	aliceLive := s.live(t, alice, make(chan tg.UpdatesClass, 128))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cr, err := aliceLive.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Team", Users: []tg.InputUserClass{&tg.InputUser{UserID: bob.id}}})
	if err != nil {
		t.Fatal(err)
	}
	group := channelOf(t, cr.Updates)

	// by @username: "hey " is 4 UTF-16 units
	if _, err := aliceLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "hey @bob", RandomID: 1,
		Entities: []tg.MessageEntityClass{&tg.MessageEntityMention{Offset: 4, Length: 4}}}); err != nil {
		t.Fatal(err)
	}
	first := waitChannelText(t, bobPushes, "hey @bob")
	if !first.Mentioned || !first.MediaUnread {
		t.Fatalf("mention push %+v", first)
	}
	// by id, the way apps name people without a username
	if _, err := aliceLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "Bob, look", RandomID: 2,
		Entities: []tg.MessageEntityClass{&tg.InputMessageEntityMentionName{Offset: 0, Length: 3, UserID: &tg.InputUser{UserID: bob.id}}}}); err != nil {
		t.Fatal(err)
	}
	second := waitChannelText(t, bobPushes, "Bob, look")
	if mn, ok := second.Entities[0].(*tg.MessageEntityMentionName); !ok || mn.UserID != bob.id || !second.Mentioned {
		t.Fatalf("mention by id %+v", second)
	}
	// a reply to Bob mentions him too; a plain message doesn't
	res, err := bobLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "mine", RandomID: 3})
	if err != nil {
		t.Fatal(err)
	}
	mineID := sentID(t, res)
	if _, err := aliceLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "re", RandomID: 4,
		ReplyTo: &tg.InputReplyToMessage{ReplyToMsgID: mineID}}); err != nil {
		t.Fatal(err)
	}
	waitChannelText(t, bobPushes, "re")
	if _, err := aliceLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "plain", RandomID: 5}); err != nil {
		t.Fatal(err)
	}
	if plain := waitChannelText(t, bobPushes, "plain"); plain.Mentioned {
		t.Fatalf("plain message marked %+v", plain)
	}

	if n := unreadMentions(ctx, t, bobLive, group); n != 3 {
		t.Fatalf("unread mentions %d, want 3", n)
	}
	got, err := bobLive.MessagesGetUnreadMentions(ctx, &tg.MessagesGetUnreadMentionsRequest{Peer: peerCh(group), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if ms := got.(*tg.MessagesChannelMessages).Messages; len(ms) != 3 {
		t.Fatalf("getUnreadMentions %+v", ms)
	}
	if ok, err := bobLive.ChannelsReadMessageContents(ctx, &tg.ChannelsReadMessageContentsRequest{Channel: inCh(group), ID: []int{first.ID}}); err != nil || !ok {
		t.Fatalf("readMessageContents %v", err)
	}
	if n := unreadMentions(ctx, t, bobLive, group); n != 2 {
		t.Fatalf("after reading one %d", n)
	}
	h, _, err := channelHistory(ctx, bobLive, group)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range h {
		if m.ID == first.ID && (!m.Mentioned || m.MediaUnread) {
			t.Fatalf("read mention in history %+v", m)
		}
	}
	if _, err := bobLive.MessagesReadMentions(ctx, &tg.MessagesReadMentionsRequest{Peer: peerCh(group)}); err != nil {
		t.Fatal(err)
	}
	if n := unreadMentions(ctx, t, bobLive, group); n != 0 {
		t.Fatalf("after readMentions %d", n)
	}
}
