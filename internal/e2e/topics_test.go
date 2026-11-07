package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

func sentID(t *testing.T, u tg.UpdatesClass) int {
	t.Helper()
	for _, x := range u.(*tg.Updates).Updates {
		if v, ok := x.(*tg.UpdateMessageID); ok {
			return v.ID
		}
	}
	t.Fatalf("no UpdateMessageID in %+v", u)
	return 0
}

func repliesOf(ctx context.Context, c *tg.Client, ch *tg.Channel, top int) (*tg.MessagesChannelMessages, error) {
	res, err := c.MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{Peer: peerCh(ch), MsgID: top, Limit: 50})
	if err != nil {
		return nil, err
	}
	return res.(*tg.MessagesChannelMessages), nil
}

func header(m tg.MessageClass) *tg.MessageReplyHeader {
	var r tg.MessageReplyHeaderClass
	switch v := m.(type) {
	case *tg.Message:
		r = v.ReplyTo
	case *tg.MessageService:
		r = v.ReplyTo
	}
	h, _ := r.(*tg.MessageReplyHeader)
	return h
}

func TestForumTopics(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	bobPushes := make(chan tg.UpdatesClass, 256)
	b := s.live(t, bob, bobPushes)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var group *tg.Channel
	var topic, first int
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Studio", Users: []tg.InputUserClass{&tg.InputUser{UserID: bob.id}}})
		if err != nil {
			return err
		}
		group = channelOf(t, res.Updates)
		if _, err := c.MessagesCreateForumTopic(ctx, &tg.MessagesCreateForumTopicRequest{Peer: peerCh(group), Title: "Early", RandomID: 4}); !tgerr.Is(err, "CHANNEL_FORUM_MISSING") {
			return fmt.Errorf("topic before forum: %v", err)
		}
		up, err := c.ChannelsToggleForum(ctx, &tg.ChannelsToggleForumRequest{Channel: inCh(group), Enabled: true})
		if err != nil {
			return err
		}
		if ch := channelOf(t, up); !ch.Forum {
			return fmt.Errorf("toggleForum gave %+v", ch)
		}
		ft, err := c.MessagesGetForumTopics(ctx, &tg.MessagesGetForumTopicsRequest{Peer: peerCh(group), Limit: 20})
		if err != nil {
			return err
		}
		if len(ft.Topics) != 1 || ft.Topics[0].(*tg.ForumTopic).ID != 1 || ft.Count != 0 {
			return fmt.Errorf("new forum topics %+v", ft)
		}

		up, err = c.MessagesCreateForumTopic(ctx, &tg.MessagesCreateForumTopicRequest{Peer: peerCh(group), Title: "Design", IconColor: 0xFFD67E, RandomID: 5})
		if err != nil {
			return err
		}
		topic = sentID(t, up)
		// a message into the topic, the way Web A sends it
		up, err = c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "first sketch", RandomID: 6,
			ReplyTo: &tg.InputReplyToMessage{ReplyToMsgID: topic, TopMsgID: topic}})
		if err != nil {
			return err
		}
		first = sentID(t, up)
		return nil
	})

	created := waitUpdate(t, bobPushes, func(u *tg.UpdateNewChannelMessage) bool {
		m, ok := u.Message.(*tg.MessageService)
		_, isTopic := m.Action.(*tg.MessageActionTopicCreate)
		return ok && isTopic
	})
	if created.Message.(*tg.MessageService).ID != topic || header(created.Message) != nil {
		t.Fatalf("topic's opening message %+v", created.Message)
	}
	sketch := waitChannelText(t, bobPushes, "first sketch")
	if h := header(sketch); h == nil || !h.ForumTopic || h.ReplyToMsgID != topic || h.ReplyToTopID != 0 {
		t.Fatalf("message in topic has header %+v", h)
	}

	// bob answers inside the topic by replying, and writes in General
	up, err := b.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "looks good", RandomID: 7,
		ReplyTo: &tg.InputReplyToMessage{ReplyToMsgID: first}})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range up.(*tg.Updates).Updates {
		if nm, ok := x.(*tg.UpdateNewChannelMessage); ok {
			if h := header(nm.Message); h == nil || !h.ForumTopic || h.ReplyToMsgID != first || h.ReplyToTopID != topic {
				t.Fatalf("reply in topic has header %+v", h)
			}
		}
	}
	if _, err := b.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "general hi", RandomID: 8}); err != nil {
		t.Fatal(err)
	}

	texts := func(cm *tg.MessagesChannelMessages) map[string]bool {
		out := map[string]bool{}
		for _, m := range cm.Messages {
			switch v := m.(type) {
			case *tg.Message:
				out[v.Message] = true
			case *tg.MessageService:
				if _, ok := v.Action.(*tg.MessageActionTopicCreate); ok {
					out["<created>"] = true
				}
			}
		}
		return out
	}
	in, err := repliesOf(ctx, b, group, topic)
	if err != nil {
		t.Fatal(err)
	}
	got := texts(in)
	if !got["<created>"] || !got["first sketch"] || !got["looks good"] || got["general hi"] || len(in.Topics) != 1 {
		t.Fatalf("topic history %v, topics %+v", got, in.Topics)
	}
	gen, err := repliesOf(ctx, b, group, 1)
	if err != nil {
		t.Fatal(err)
	}
	got = texts(gen)
	if !got["general hi"] || got["first sketch"] || got["<created>"] {
		t.Fatalf("General history %v", got)
	}

	// bob has alice's sketch unread in Design until he reads it
	topicOf := func(c *tg.Client, id int) *tg.ForumTopic {
		res, err := c.MessagesGetForumTopicsByID(ctx, &tg.MessagesGetForumTopicsByIDRequest{Peer: peerCh(group), Topics: []int{id}})
		if err != nil {
			t.Fatal(err)
		}
		ft, _ := res.Topics[0].(*tg.ForumTopic)
		return ft
	}
	if ft := topicOf(b, topic); ft == nil || ft.UnreadCount != 1 || ft.Title != "Design" || ft.IconColor != 0xFFD67E || ft.TopMessage == 0 {
		t.Fatalf("bob's view of the topic %+v", ft)
	}
	if _, err := b.MessagesReadDiscussion(ctx, &tg.MessagesReadDiscussionRequest{Peer: peerCh(group), MsgID: topic, ReadMaxID: first}); err != nil {
		t.Fatal(err)
	}
	if ft := topicOf(b, topic); ft.UnreadCount != 0 || ft.ReadInboxMaxID != first {
		t.Fatalf("after readDiscussion %+v", ft)
	}

	// close, pin, list, delete
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		req := &tg.MessagesEditForumTopicRequest{Peer: peerCh(group), TopicID: topic}
		req.SetClosed(true)
		if _, err := c.MessagesEditForumTopic(ctx, req); err != nil {
			return err
		}
		if _, err := c.MessagesEditForumTopic(ctx, req); !tgerr.Is(err, "TOPIC_NOT_MODIFIED") {
			return fmt.Errorf("closing twice: %v", err)
		}
		if _, err := c.MessagesUpdatePinnedForumTopic(ctx, &tg.MessagesUpdatePinnedForumTopicRequest{Peer: peerCh(group), TopicID: topic, Pinned: true}); err != nil {
			return err
		}
		return nil
	})
	edit := waitUpdate(t, bobPushes, func(u *tg.UpdateNewChannelMessage) bool {
		m, ok := u.Message.(*tg.MessageService)
		_, isEdit := m.Action.(*tg.MessageActionTopicEdit)
		return ok && isEdit
	})
	if h := header(edit.Message); h == nil || !h.ForumTopic || h.ReplyToMsgID != topic {
		t.Fatalf("topic edit message header %+v", h)
	}
	waitUpdate(t, bobPushes, func(u *tg.UpdatePinnedForumTopic) bool { return u.TopicID == topic && u.Pinned })
	if _, err := b.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "late", RandomID: 9,
		ReplyTo: &tg.InputReplyToMessage{ReplyToMsgID: topic, TopMsgID: topic}}); !tgerr.Is(err, "TOPIC_CLOSED") {
		t.Fatalf("writing in a closed topic: %v", err)
	}
	req := &tg.MessagesEditForumTopicRequest{Peer: peerCh(group), TopicID: topic}
	req.SetClosed(false)
	if _, err := b.MessagesEditForumTopic(ctx, req); !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("bob reopening alice's topic: %v", err)
	}
	ft, err := b.MessagesGetForumTopics(ctx, &tg.MessagesGetForumTopicsRequest{Peer: peerCh(group), Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if lead := ft.Topics[0].(*tg.ForumTopic); lead.ID != topic || !lead.Pinned || !lead.Closed || ft.Count != 1 || len(ft.Topics) != 2 {
		t.Fatalf("topic list %+v", ft)
	}

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.MessagesDeleteTopicHistory(ctx, &tg.MessagesDeleteTopicHistoryRequest{Peer: peerCh(group), TopMsgID: 1}); !tgerr.Is(err, "TOPIC_ID_INVALID") {
			return fmt.Errorf("deleting General: %v", err)
		}
		res, err := c.MessagesDeleteTopicHistory(ctx, &tg.MessagesDeleteTopicHistoryRequest{Peer: peerCh(group), TopMsgID: topic})
		if err != nil {
			return err
		}
		// opening message, sketch, reply and the edit message
		if res.Offset != 0 || res.PtsCount != 4 {
			return fmt.Errorf("deleteTopicHistory %+v", res)
		}
		return nil
	})
	byID, err := b.MessagesGetForumTopicsByID(ctx, &tg.MessagesGetForumTopicsByIDRequest{Peer: peerCh(group), Topics: []int{topic}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := byID.Topics[0].(*tg.ForumTopicDeleted); !ok {
		t.Fatalf("deleted topic still there: %+v", byID.Topics[0])
	}
	// the channel journal still adds up for a client catching up
	d, err := b.UpdatesGetChannelDifference(ctx, &tg.UpdatesGetChannelDifferenceRequest{Channel: inCh(group), Filter: &tg.ChannelMessagesFilterEmpty{}, Pts: 1, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.(*tg.UpdatesChannelDifference); !ok {
		t.Fatalf("channel difference %T", d)
	}
}

// who may turn topics on: the creator, an admin with change_info, nobody else
func TestForumToggleRights(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")
	dave := s.signUp(t, "dave")

	var group *tg.Channel
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		var err error
		if group, err = newGroup(ctx, c, t, "Rights", bob.id, carol.id, dave.id); err != nil {
			return err
		}
		if _, err := c.ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{Channel: inCh(group), UserID: &tg.InputUser{UserID: bob.id},
			AdminRights: tg.ChatAdminRights{ChangeInfo: true}}); err != nil {
			return err
		}
		_, err = c.ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{Channel: inCh(group), UserID: &tg.InputUser{UserID: carol.id},
			AdminRights: tg.ChatAdminRights{DeleteMessages: true}})
		return err
	})
	toggle := func(p person, on bool) error {
		var out error
		run(t, s.client(p.storage), func(ctx context.Context, c *tg.Client) error {
			_, out = c.ChannelsToggleForum(ctx, &tg.ChannelsToggleForumRequest{Channel: inCh(group), Enabled: on})
			return nil
		})
		return out
	}
	if err := toggle(dave, true); !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("member toggled: %v", err)
	}
	if err := toggle(carol, true); !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("admin without change_info toggled: %v", err)
	}
	if err := toggle(bob, true); err != nil {
		t.Fatalf("admin with change_info: %v", err)
	}
	if err := toggle(alice, false); err != nil {
		t.Fatalf("creator: %v", err)
	}
}

func TestForumTyping(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	bobPushes := make(chan tg.UpdatesClass, 256)
	s.live(t, bob, bobPushes)

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		group, err := newGroup(ctx, c, t, "Typing", bob.id)
		if err != nil {
			return err
		}
		if _, err := c.ChannelsToggleForum(ctx, &tg.ChannelsToggleForumRequest{Channel: inCh(group), Enabled: true}); err != nil {
			return err
		}
		up, err := c.MessagesCreateForumTopic(ctx, &tg.MessagesCreateForumTopicRequest{Peer: peerCh(group), Title: "Design", RandomID: 5})
		if err != nil {
			return err
		}
		req := &tg.MessagesSetTypingRequest{Peer: peerCh(group), Action: &tg.SendMessageTypingAction{}}
		req.SetTopMsgID(sentID(t, up))
		_, err = c.MessagesSetTyping(ctx, req)
		return err
	})
	typing := waitUpdate(t, bobPushes, func(u *tg.UpdateChannelUserTyping) bool { return true })
	if top, ok := typing.GetTopMsgID(); !ok || top == 0 {
		t.Fatalf("typing lost its topic: %+v", typing)
	}
}

func TestForumSearch(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		group, err := newGroup(ctx, c, t, "Search")
		if err != nil {
			return err
		}
		if _, err := c.ChannelsToggleForum(ctx, &tg.ChannelsToggleForumRequest{Channel: inCh(group), Enabled: true}); err != nil {
			return err
		}
		up, err := c.MessagesCreateForumTopic(ctx, &tg.MessagesCreateForumTopicRequest{Peer: peerCh(group), Title: "Design", RandomID: 5})
		if err != nil {
			return err
		}
		topic := sentID(t, up)
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "needle in topic", RandomID: 6,
			ReplyTo: &tg.InputReplyToMessage{ReplyToMsgID: topic, TopMsgID: topic}}); err != nil {
			return err
		}
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "needle in general", RandomID: 7}); err != nil {
			return err
		}
		find := func(top int) (string, int, error) {
			req := &tg.MessagesSearchRequest{Peer: peerCh(group), Q: "needle", Filter: &tg.InputMessagesFilterEmpty{}, Limit: 10}
			if top != 0 {
				req.SetTopMsgID(top)
			}
			res, err := c.MessagesSearch(ctx, req)
			if err != nil {
				return "", 0, err
			}
			cm := res.(*tg.MessagesChannelMessages)
			if len(cm.Messages) == 0 {
				return "", cm.Count, nil
			}
			return cm.Messages[0].(*tg.Message).Message, cm.Count, nil
		}
		if text, n, err := find(topic); err != nil || n != 1 || text != "needle in topic" {
			return fmt.Errorf("search in topic: %q %d %v", text, n, err)
		}
		if text, n, err := find(1); err != nil || n != 1 || text != "needle in general" {
			return fmt.Errorf("search in General: %q %d %v", text, n, err)
		}
		if _, n, err := find(0); err != nil || n != 2 {
			return fmt.Errorf("search in the group: %d %v", n, err)
		}
		return nil
	})
}

// each member's own; a client finds it again in the full chat and the dialog
func TestForumViewAsMessages(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	var group *tg.Channel
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		var err error
		if group, err = newGroup(ctx, c, t, "Flat", bob.id); err != nil {
			return err
		}
		if _, err := c.ChannelsToggleViewForumAsMessages(ctx, &tg.ChannelsToggleViewForumAsMessagesRequest{Channel: inCh(group), Enabled: true}); !tgerr.Is(err, "CHANNEL_FORUM_MISSING") {
			return fmt.Errorf("not a forum yet: %v", err)
		}
		if _, err := c.ChannelsToggleForum(ctx, &tg.ChannelsToggleForumRequest{Channel: inCh(group), Enabled: true}); err != nil {
			return err
		}
		_, err = c.ChannelsToggleViewForumAsMessages(ctx, &tg.ChannelsToggleViewForumAsMessagesRequest{Channel: inCh(group), Enabled: true})
		return err
	})
	check := func(p person, want bool) {
		t.Helper()
		run(t, s.client(p.storage), func(ctx context.Context, c *tg.Client) error {
			full, err := c.ChannelsGetFullChannel(ctx, inCh(group))
			if err != nil {
				return err
			}
			if got := full.FullChat.(*tg.ChannelFull).ViewForumAsMessages; got != want {
				return fmt.Errorf("full chat says %v", got)
			}
			ds, err := c.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: peerCh(group)}})
			if err != nil {
				return err
			}
			if got := ds.Dialogs[0].(*tg.Dialog).ViewForumAsMessages; got != want {
				return fmt.Errorf("dialog says %v", got)
			}
			return nil
		})
	}
	check(alice, true)
	check(bob, false)
}
