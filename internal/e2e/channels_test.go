package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

func channelOf(t *testing.T, u tg.UpdatesClass) *tg.Channel {
	t.Helper()
	ups, ok := u.(*tg.Updates)
	if !ok {
		t.Fatalf("updates %T", u)
	}
	for _, c := range ups.Chats {
		if ch, ok := c.(*tg.Channel); ok {
			return ch
		}
	}
	t.Fatalf("no channel in %+v", ups)
	return nil
}

func inCh(c *tg.Channel) *tg.InputChannel {
	return &tg.InputChannel{ChannelID: c.ID, AccessHash: c.AccessHash}
}
func peerCh(c *tg.Channel) *tg.InputPeerChannel {
	return &tg.InputPeerChannel{ChannelID: c.ID, AccessHash: c.AccessHash}
}

func waitChannelText(t *testing.T, got <-chan tg.UpdatesClass, text string) *tg.Message {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case u := <-got:
			if ups, ok := u.(*tg.Updates); ok {
				for _, x := range ups.Updates {
					if nm, ok := x.(*tg.UpdateNewChannelMessage); ok {
						if m, ok := nm.Message.(*tg.Message); ok && m.Message == text {
							return m
						}
					}
				}
			}
		case <-deadline:
			t.Fatalf("no channel push with %q", text)
			return nil
		}
	}
}

func channelHistory(ctx context.Context, c *tg.Client, ch *tg.Channel) ([]*tg.Message, int, error) {
	h, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peerCh(ch), Limit: 50})
	if err != nil {
		return nil, 0, err
	}
	cm := h.(*tg.MessagesChannelMessages)
	var out []*tg.Message
	for _, m := range cm.Messages {
		if msg, ok := m.(*tg.Message); ok {
			out = append(out, msg)
		}
	}
	return out, cm.Count, nil
}

func TestGroupsAndChannels(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")

	bobPushes := make(chan tg.UpdatesClass, 128)
	bobLive := s.live(t, bob, bobPushes)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var group *tg.Channel
	var link string
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Team", Users: []tg.InputUserClass{&tg.InputUser{UserID: bob.id}}})
		if err != nil {
			return err
		}
		group = channelOf(t, res.Updates)
		if !group.Megagroup || !group.Creator {
			return fmt.Errorf("createChat made %+v", group)
		}
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "hello team", RandomID: 10}); err != nil {
			return err
		}
		// like Telegram, members may edit group info until the admins say no
		if _, err := c.MessagesEditChatDefaultBannedRights(ctx, &tg.MessagesEditChatDefaultBannedRightsRequest{Peer: peerCh(group),
			BannedRights: tg.ChatBannedRights{ChangeInfo: true}}); err != nil {
			return err
		}
		inv, err := c.MessagesExportChatInvite(ctx, &tg.MessagesExportChatInviteRequest{Peer: peerCh(group)})
		if err != nil {
			return err
		}
		link = inv.(*tg.ChatInviteExported).Link
		return nil
	})

	// Bob was added and gets the group live
	waitChannelText(t, bobPushes, "hello team")
	dl, err := bobLive.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var found *tg.Dialog
	for _, d := range dl.(*tg.MessagesDialogs).Dialogs {
		if p, ok := d.(*tg.Dialog).Peer.(*tg.PeerChannel); ok && p.ChannelID == group.ID {
			found = d.(*tg.Dialog)
		}
	}
	if found == nil || found.UnreadCount < 1 || found.Pts == 0 {
		t.Fatalf("bob's group dialog %+v", found)
	}
	if _, err := bobLive.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{Channel: inCh(group), MaxID: found.TopMessage}); err != nil {
		t.Fatal(err)
	}

	// Carol joins with the link, can't edit the title until promoted
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		chk, err := c.MessagesCheckChatInvite(ctx, link)
		if err != nil {
			return err
		}
		if ci, ok := chk.(*tg.ChatInvite); !ok || ci.Title != "Team" {
			return fmt.Errorf("checkChatInvite %+v", chk)
		}
		if _, err := c.MessagesImportChatInvite(ctx, link); err != nil {
			return err
		}
		if _, err := c.ChannelsEditTitle(ctx, &tg.ChannelsEditTitleRequest{Channel: inCh(group), Title: "Carol's"}); !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
			return fmt.Errorf("member renamed the group: %v", err)
		}
		return nil
	})

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		parts, err := c.ChannelsGetParticipants(ctx, &tg.ChannelsGetParticipantsRequest{Channel: inCh(group), Filter: &tg.ChannelParticipantsRecent{}, Limit: 50})
		if err != nil {
			return err
		}
		if n := parts.(*tg.ChannelsChannelParticipants).Count; n != 3 {
			return fmt.Errorf("participants %d", n)
		}
		if _, err := c.ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{Channel: inCh(group), UserID: &tg.InputUser{UserID: carol.id},
			AdminRights: tg.ChatAdminRights{ChangeInfo: true}, Rank: "editor"}); err != nil {
			return err
		}
		_, err = c.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{Channel: inCh(group), Participant: &tg.InputPeerUser{UserID: bob.id},
			BannedRights: tg.ChatBannedRights{SendMessages: true}})
		return err
	})
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.ChannelsEditTitle(ctx, &tg.ChannelsEditTitleRequest{Channel: inCh(group), Title: "Team (edited)"})
		return err
	})
	if _, err := bobLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "can I talk?", RandomID: 20}); !tgerr.Is(err, "CHAT_WRITE_FORBIDDEN") {
		t.Fatalf("restricted bob sent: %v", err)
	}

	// a broadcast channel with the group as its discussion
	var channel *tg.Channel
	var post int
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: true, Title: "News"})
		if err != nil {
			return err
		}
		channel = channelOf(t, res)
		if _, err := c.ChannelsUpdateUsername(ctx, &tg.ChannelsUpdateUsernameRequest{Channel: inCh(channel), Username: "greennews"}); err != nil {
			return err
		}
		if _, err := c.ChannelsSetDiscussionGroup(ctx, &tg.ChannelsSetDiscussionGroupRequest{Broadcast: inCh(channel), Group: inCh(group)}); err != nil {
			return err
		}
		sent, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(channel), Message: "big news", RandomID: 30})
		if err != nil {
			return err
		}
		for _, u := range sent.(*tg.Updates).Updates {
			if id, ok := u.(*tg.UpdateMessageID); ok {
				post = id.ID
			}
		}
		return nil
	})

	// Carol finds it by name, joins, can't post, comments in the thread
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: "greennews"})
		if err != nil {
			return err
		}
		ch := res.Chats[0].(*tg.Channel)
		if !ch.Left || !ch.Broadcast {
			return fmt.Errorf("resolved %+v", ch)
		}
		if _, err := c.ChannelsJoinChannel(ctx, inCh(ch)); err != nil {
			return err
		}
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(ch), Message: "me too", RandomID: 40}); !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
			return fmt.Errorf("member posted in a channel: %v", err)
		}
		disc, err := c.MessagesGetDiscussionMessage(ctx, &tg.MessagesGetDiscussionMessageRequest{Peer: peerCh(ch), MsgID: post})
		if err != nil {
			return err
		}
		top := disc.Messages[0].(*tg.Message)
		if top.Message != "big news" {
			return fmt.Errorf("discussion top %+v", top)
		}
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "first comment", RandomID: 41,
			ReplyTo: &tg.InputReplyToMessage{ReplyToMsgID: top.ID}}); err != nil {
			return err
		}
		replies, err := c.MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{Peer: peerCh(ch), MsgID: post, Limit: 10})
		if err != nil {
			return err
		}
		if n := len(replies.(*tg.MessagesChannelMessages).Messages); n != 1 {
			return fmt.Errorf("replies %d", n)
		}
		msgs, _, err := channelHistory(ctx, c, ch)
		if err != nil {
			return err
		}
		if r, ok := msgs[0].GetReplies(); !ok || r.Replies != 1 {
			return fmt.Errorf("post replies %+v", msgs[0].Replies)
		}
		return nil
	})

	// Bob catches up on the group from the journal
	diff, err := bobLive.UpdatesGetChannelDifference(ctx, &tg.UpdatesGetChannelDifferenceRequest{Channel: inCh(group), Filter: &tg.ChannelMessagesFilterEmpty{}, Pts: found.Pts, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := diff.(*tg.UpdatesChannelDifference)
	if !ok || len(d.NewMessages) < 3 { // carol joined, title edit, post copy, comment
		t.Fatalf("channel difference %T %+v", diff, diff)
	}
}
