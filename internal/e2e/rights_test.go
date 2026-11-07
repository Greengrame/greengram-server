package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

// newGroup makes a group owned by the first client with the given members.
func newGroup(ctx context.Context, c *tg.Client, t *testing.T, title string, members ...int64) (*tg.Channel, error) {
	var users []tg.InputUserClass
	for _, id := range members {
		users = append(users, &tg.InputUser{UserID: id})
	}
	res, err := c.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: title, Users: users})
	if err != nil {
		return nil, err
	}
	return channelOf(t, res.Updates), nil
}

func TestAdminCantGrantMoreThanItHas(t *testing.T) {
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
			AdminRights: tg.ChatAdminRights{AddAdmins: true, InviteUsers: true}}); err != nil {
			return err
		}
		_, err = c.ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{Channel: inCh(group), UserID: &tg.InputUser{UserID: dave.id},
			AdminRights: tg.ChatAdminRights{DeleteMessages: true}})
		return err
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{Channel: inCh(group), UserID: &tg.InputUserSelf{},
			AdminRights: tg.ChatAdminRights{AddAdmins: true, InviteUsers: true, BanUsers: true, ChangeInfo: true}}); err == nil {
			return fmt.Errorf("admin gave itself more rights")
		}
		if _, err := c.ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{Channel: inCh(group), UserID: &tg.InputUser{UserID: carol.id},
			AdminRights: tg.ChatAdminRights{BanUsers: true}}); err == nil {
			return fmt.Errorf("admin granted a right it doesn't have")
		}
		if _, err := c.ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{Channel: inCh(group), UserID: &tg.InputUser{UserID: dave.id},
			AdminRights: tg.ChatAdminRights{}}); err == nil {
			return fmt.Errorf("admin demoted an admin the creator promoted")
		}
		// what it has itself it may hand on
		_, err := c.ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{Channel: inCh(group), UserID: &tg.InputUser{UserID: carol.id},
			AdminRights: tg.ChatAdminRights{InviteUsers: true}})
		return err
	})
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.ChannelsEditTitle(ctx, &tg.ChannelsEditTitleRequest{Channel: inCh(group), Title: "Carol's"})
		if !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
			return fmt.Errorf("carol renamed the group: %v", err)
		}
		return nil
	})
}

func TestKickedMemberCantTouchMessages(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	var group *tg.Channel
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		var err error
		group, err = newGroup(ctx, c, t, "Kick", bob.id)
		return err
	})
	var mine int
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		sent, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "hi all", RandomID: 1})
		if err != nil {
			return err
		}
		mine = sent.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID
		return nil
	})
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{Channel: inCh(group), Participant: &tg.InputPeerUser{UserID: bob.id},
			BannedRights: tg.ChatBannedRights{ViewMessages: true}})
		return err
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{Peer: peerCh(group), ID: mine, Message: "spam spam"}); err == nil {
			return fmt.Errorf("kicked member edited a message")
		}
		if _, err := c.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{Channel: inCh(group), ID: []int{mine}}); err == nil {
			return fmt.Errorf("kicked member deleted a message")
		}
		return nil
	})
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		msgs, _, err := channelHistory(ctx, c, group)
		if err != nil {
			return err
		}
		for _, m := range msgs {
			if m.ID == mine && m.Message == "hi all" {
				return nil
			}
		}
		return fmt.Errorf("bob's message changed or gone: %+v", msgs)
	})
}

func TestPrivacyChatExceptions(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")
	if _, err := s.db.Pool.Exec(context.Background(), "UPDATE users SET bio='secret bio' WHERE id=$1", bob.id); err != nil {
		t.Fatal(err)
	}

	var group *tg.Channel
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		var err error
		if group, err = newGroup(ctx, c, t, "Work", alice.id); err != nil {
			return err
		}
		// everybody but the people from work
		_, err = c.AccountSetPrivacy(ctx, &tg.AccountSetPrivacyRequest{Key: &tg.InputPrivacyKeyAbout{}, Rules: []tg.InputPrivacyRuleClass{
			&tg.InputPrivacyValueDisallowChatParticipants{Chats: []int64{group.ID}}, &tg.InputPrivacyValueAllowAll{}}})
		return err
	})
	about := func(p person) string {
		var got string
		run(t, s.client(p.storage), func(ctx context.Context, c *tg.Client) error {
			full, err := c.UsersGetFullUser(ctx, &tg.InputUser{UserID: bob.id})
			if err != nil {
				return err
			}
			got = full.FullUser.About
			return nil
		})
		return got
	}
	if got := about(alice); got != "" {
		t.Fatalf("group member sees bob's bio %q", got)
	}
	if got := about(carol); got != "secret bio" {
		t.Fatalf("outsider bio %q", got)
	}
}

func TestForwardedPostHidesAdmin(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	var channel *tg.Channel
	var post int
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: true, Title: "Anon news"})
		if err != nil {
			return err
		}
		channel = channelOf(t, res)
		if _, err := c.ChannelsInviteToChannel(ctx, &tg.ChannelsInviteToChannelRequest{Channel: inCh(channel), Users: []tg.InputUserClass{&tg.InputUser{UserID: bob.id}}}); err != nil {
			return err
		}
		sent, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(channel), Message: "who wrote this?", RandomID: 1})
		if err != nil {
			return err
		}
		post = sent.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID
		return nil
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{FromPeer: peerCh(channel), ID: []int{post}, RandomID: []int64{2},
			ToPeer: &tg.InputPeerSelf{}})
		if err != nil {
			return err
		}
		m := sent(t, res)
		if fwd, ok := m.GetFwdFrom(); ok {
			if p, ok := fwd.FromID.(*tg.PeerUser); ok && p.UserID == alice.id {
				return fmt.Errorf("forward names the channel's admin: %+v", fwd)
			}
		}
		for _, u := range res.(*tg.Updates).Users {
			if u.(*tg.User).ID == alice.id {
				return fmt.Errorf("forward ships the admin's user object")
			}
		}
		return nil
	})
}

func TestPrivateGroupHiddenFromOutsiders(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	carol := s.signUp(t, "carol")

	var group *tg.Channel
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		var err error
		group, err = newGroup(ctx, c, t, "Surprise party for carol")
		return err
	})
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.ChannelsGetChannels(ctx, []tg.InputChannelClass{&tg.InputChannel{ChannelID: group.ID}})
		if err != nil {
			return err
		}
		for _, ch := range res.GetChats() {
			if v, ok := ch.(*tg.Channel); ok {
				return fmt.Errorf("outsider got the private group: %q, %d members", v.Title, v.ParticipantsCount)
			}
		}
		return nil
	})
}

func TestKickedAdminsLinkStopsWorking(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")

	var group *tg.Channel
	var link string
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		var err error
		if group, err = newGroup(ctx, c, t, "Links", bob.id); err != nil {
			return err
		}
		_, err = c.ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{Channel: inCh(group), UserID: &tg.InputUser{UserID: bob.id},
			AdminRights: tg.ChatAdminRights{InviteUsers: true}})
		return err
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		inv, err := c.MessagesExportChatInvite(ctx, &tg.MessagesExportChatInviteRequest{Peer: peerCh(group)})
		if err != nil {
			return err
		}
		link = inv.(*tg.ChatInviteExported).Link
		return nil
	})
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{Channel: inCh(group), Participant: &tg.InputPeerUser{UserID: bob.id},
			BannedRights: tg.ChatBannedRights{ViewMessages: true}})
		return err
	})
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.MessagesImportChatInvite(ctx, link); !tgerr.Is(err, "INVITE_HASH_EXPIRED") {
			return fmt.Errorf("kicked admin's link let carol in: %v", err)
		}
		return nil
	})
}

func TestBanListIsForAdmins(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")

	var group *tg.Channel
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		var err error
		if group, err = newGroup(ctx, c, t, "Bans", bob.id, carol.id); err != nil {
			return err
		}
		_, err = c.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{Channel: inCh(group), Participant: &tg.InputPeerUser{UserID: carol.id},
			BannedRights: tg.ChatBannedRights{ViewMessages: true}})
		return err
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		for _, f := range []tg.ChannelParticipantsFilterClass{&tg.ChannelParticipantsKicked{}, &tg.ChannelParticipantsBanned{}} {
			if _, err := c.ChannelsGetParticipants(ctx, &tg.ChannelsGetParticipantsRequest{Channel: inCh(group), Filter: f, Limit: 10}); !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
				return fmt.Errorf("member read %T: %v", f, err)
			}
		}
		return nil
	})
}

func dialogOrder(ctx context.Context, c *tg.Client) ([]int64, error) {
	res, err := c.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20})
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, d := range res.(*tg.MessagesDialogs).Dialogs {
		if p, ok := d.(*tg.Dialog).Peer.(*tg.PeerUser); ok {
			out = append(out, p.UserID)
		}
	}
	return out, nil
}

func TestDeletingTopMessageReordersDialogs(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")
	db := s.db.Pool
	bg := context.Background()

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "long ago", RandomID: 1}); err != nil {
			return err
		}
		// pretend it was a while back
		if _, err := db.Exec(bg, "UPDATE messages SET date = date - 1000 WHERE text='long ago'"); err != nil {
			return err
		}
		if _, err := db.Exec(bg, "UPDATE dialogs SET top_date = top_date - 1000"); err != nil {
			return err
		}
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(carol.id), Message: "a bit ago", RandomID: 2}); err != nil {
			return err
		}
		if _, err := db.Exec(bg, "UPDATE dialogs SET top_date = top_date - 500 WHERE peer_id=$1 OR owner_id=$1", carol.id); err != nil {
			return err
		}
		sent, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "oops", RandomID: 3})
		if err != nil {
			return err
		}
		oops := sent.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID
		if _, err := c.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{ID: []int{oops}, Revoke: true}); err != nil {
			return err
		}
		order, err := dialogOrder(ctx, c)
		if err != nil {
			return err
		}
		if len(order) != 2 || order[0] != carol.id {
			return fmt.Errorf("bob's chat stayed on top after its newest message went: %v", order)
		}
		return nil
	})
}

func TestReadEchoToOtherSessions(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	var first, second int
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		for i, text := range []string{"one", "two"} {
			if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(alice.id), Message: text, RandomID: int64(i + 1)}); err != nil {
				return err
			}
		}
		return nil
	})
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		msgs, err := history(ctx, c, bob.id)
		if err != nil || len(msgs) != 2 {
			return fmt.Errorf("history %v %d", err, len(msgs))
		}
		second, first = msgs[0].ID, msgs[1].ID
		return nil
	})

	pushes := make(chan tg.UpdatesClass, 16)
	s.live(t, alice, pushes)
	wait := func() *tg.UpdateReadHistoryInbox {
		deadline := time.After(20 * time.Second)
		for {
			select {
			case u := <-pushes:
				if ups, ok := u.(*tg.Updates); ok {
					for _, x := range ups.Updates {
						if r, ok := x.(*tg.UpdateReadHistoryInbox); ok {
							return r
						}
					}
				}
			case <-deadline:
				t.Fatal("no read push")
				return nil
			}
		}
	}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{Peer: userPeer(bob.id), MaxID: first})
		return err
	})
	if r := wait(); r.MaxID != first || r.StillUnreadCount != 1 {
		t.Fatalf("partial read echo: max %d unread %d", r.MaxID, r.StillUnreadCount)
	}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{Peer: userPeer(bob.id), MaxID: 0})
		return err
	})
	if r := wait(); r.MaxID != second {
		t.Fatalf("read-all echo: max %d, want %d", r.MaxID, second)
	}
}

func TestNewMemberStartsRead(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	var group *tg.Channel
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		var err error
		if group, err = newGroup(ctx, c, t, "Old chat"); err != nil {
			return err
		}
		for i := range 20 {
			if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: fmt.Sprint("backlog ", i), RandomID: int64(i + 1)}); err != nil {
				return err
			}
		}
		_, err = c.ChannelsInviteToChannel(ctx, &tg.ChannelsInviteToChannelRequest{Channel: inCh(group), Users: []tg.InputUserClass{&tg.InputUser{UserID: bob.id}}})
		return err
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		pd, err := c.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: peerCh(group)}})
		if err != nil {
			return err
		}
		// just "alice added bob"
		if d := pd.Dialogs[0].(*tg.Dialog); d.UnreadCount > 1 {
			return fmt.Errorf("new member has %d unread from before joining", d.UnreadCount)
		}
		return nil
	})
}

func TestPosterEditsOwnPost(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	var channel *tg.Channel
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: true, Title: "Desk"})
		if err != nil {
			return err
		}
		channel = channelOf(t, res)
		if _, err := c.ChannelsInviteToChannel(ctx, &tg.ChannelsInviteToChannelRequest{Channel: inCh(channel), Users: []tg.InputUserClass{&tg.InputUser{UserID: bob.id}}}); err != nil {
			return err
		}
		_, err = c.ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{Channel: inCh(channel), UserID: &tg.InputUser{UserID: bob.id},
			AdminRights: tg.ChatAdminRights{PostMessages: true}})
		return err
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		sent, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(channel), Message: "tpyo", RandomID: 1})
		if err != nil {
			return err
		}
		id := sent.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID
		_, err = c.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{Peer: peerCh(channel), ID: id, Message: "typo"})
		return err
	})
}

func TestInvitePrivacy(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")

	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.AccountSetPrivacy(ctx, &tg.AccountSetPrivacyRequest{Key: &tg.InputPrivacyKeyChatInvite{},
			Rules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueDisallowAll{}}})
		return err
	})
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Private?",
			Users: []tg.InputUserClass{&tg.InputUser{UserID: bob.id}, &tg.InputUser{UserID: carol.id}}})
		if err != nil {
			return err
		}
		if len(res.MissingInvitees) != 1 || res.MissingInvitees[0].UserID != bob.id {
			return fmt.Errorf("missing invitees %+v", res.MissingInvitees)
		}
		group := channelOf(t, res.Updates)
		inv, err := c.ChannelsInviteToChannel(ctx, &tg.ChannelsInviteToChannelRequest{Channel: inCh(group), Users: []tg.InputUserClass{&tg.InputUser{UserID: bob.id}}})
		if err != nil {
			return err
		}
		if len(inv.MissingInvitees) != 1 {
			return fmt.Errorf("invite missing %+v", inv.MissingInvitees)
		}
		p, err := c.ChannelsGetParticipant(ctx, &tg.ChannelsGetParticipantRequest{Channel: inCh(group), Participant: &tg.InputPeerUser{UserID: bob.id}})
		if err == nil {
			return fmt.Errorf("bob was added anyway: %+v", p.Participant)
		}
		return nil
	})
}
