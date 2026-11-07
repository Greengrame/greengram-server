package e2e

import (
	"context"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/telegram/uploader"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

func TestBlocking(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		// in each other's books, so "contacts" rules alone would let bob in
		if _, err := c.ContactsAddContact(ctx, &tg.ContactsAddContactRequest{ID: &tg.InputUser{UserID: bob.id}, FirstName: "Bob"}); err != nil {
			return err
		}
		if ok, err := c.ContactsBlock(ctx, &tg.ContactsBlockRequest{ID: userPeer(bob.id)}); err != nil || !ok {
			return fmt.Errorf("block: %v %v", ok, err)
		}
		res, err := c.ContactsGetBlocked(ctx, &tg.ContactsGetBlockedRequest{Limit: 10})
		if err != nil {
			return err
		}
		list, ok := res.(*tg.ContactsBlocked)
		if !ok || len(list.Blocked) != 1 || list.Blocked[0].PeerID.(*tg.PeerUser).UserID != bob.id || len(list.Users) != 1 {
			return fmt.Errorf("blocked list %+v", res)
		}
		full, err := c.UsersGetFullUser(ctx, &tg.InputUser{UserID: bob.id})
		if err != nil {
			return err
		}
		if !full.FullUser.Blocked {
			return fmt.Errorf("full user of bob isn't marked blocked")
		}
		_, err = c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "hi", RandomID: 1})
		if !tgerr.Is(err, "YOU_BLOCKED_USER") {
			return fmt.Errorf("alice wrote to bob she blocked: %v", err)
		}
		return nil
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.ContactsAddContact(ctx, &tg.ContactsAddContactRequest{ID: &tg.InputUser{UserID: alice.id}, FirstName: "Alice"}); err != nil {
			return err
		}
		// being blocked looks like nothing: the sends work, on bob's side only
		st, err := c.UpdatesGetState(ctx)
		if err != nil {
			return err
		}
		res, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(alice.id), Message: "hi", RandomID: 1})
		if err != nil {
			return fmt.Errorf("bob's text to alice, who blocked him: %v", err)
		}
		if m := sent(t, res); !m.Out || m.Message != "hi" {
			return fmt.Errorf("bob's copy %+v", m)
		}
		after, err := c.UpdatesGetState(ctx)
		if err != nil {
			return err
		}
		if after.Pts != st.Pts+1 {
			return fmt.Errorf("pts %d -> %d", st.Pts, after.Pts)
		}
		in, err := uploader.NewUploader(c).FromBytes(ctx, "x.txt", []byte("hello"))
		if err != nil {
			return err
		}
		if _, err = c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: userPeer(alice.id), RandomID: 2, Media: &tg.InputMediaUploadedDocument{File: in, MimeType: "text/plain"}}); err != nil {
			return fmt.Errorf("bob's file to alice: %v", err)
		}
		saved, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: &tg.InputPeerSelf{}, Message: "pass it on", RandomID: 3})
		if err != nil {
			return err
		}
		if _, err = c.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{FromPeer: &tg.InputPeerSelf{}, ID: []int{sent(t, saved).ID}, RandomID: []int64{4},
			ToPeer: userPeer(alice.id)}); err != nil {
			return fmt.Errorf("bob's forward to alice: %v", err)
		}
		if _, err := c.MessagesSetTyping(ctx, &tg.MessagesSetTypingRequest{Peer: userPeer(alice.id), Action: &tg.SendMessageTypingAction{}}); err != nil {
			return fmt.Errorf("typing to alice: %v", err)
		}
		h, err := history(ctx, c, alice.id)
		if err != nil {
			return err
		}
		if len(h) != 3 {
			return fmt.Errorf("bob sees %d of his 3 messages", len(h))
		}
		users, err := c.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUser{UserID: alice.id}})
		if err != nil {
			return err
		}
		if st := users[0].(*tg.User).Status; func() bool { _, ok := st.(*tg.UserStatusEmpty); return !ok }() {
			return fmt.Errorf("bob sees alice's last seen: %T", st)
		}
		trap, err := c.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "trap", Users: []tg.InputUserClass{&tg.InputUser{UserID: alice.id}}})
		if err != nil {
			return err
		}
		if len(trap.MissingInvitees) != 1 || trap.MissingInvitees[0].UserID != alice.id {
			return fmt.Errorf("bob added alice to a group: %+v", trap.MissingInvitees)
		}
		return nil
	})
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		// nothing reached alice: no message, no dialog
		h, err := history(ctx, c, bob.id)
		if err != nil || len(h) != 0 {
			return fmt.Errorf("alice got bob's messages: %d %v", len(h), err)
		}
		d, err := c.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20})
		if err != nil {
			return err
		}
		list, ok := d.(*tg.MessagesDialogs)
		if !ok {
			return fmt.Errorf("dialogs %T", d)
		}
		for _, x := range list.Dialogs {
			if p, ok := x.(*tg.Dialog).Peer.(*tg.PeerUser); ok && p.UserID == bob.id {
				return fmt.Errorf("alice has a dialog with bob")
			}
		}
		// refused before the upload is taken in
		in, err := uploader.NewUploader(c).FromBytes(ctx, "x.txt", []byte("hello"))
		if err != nil {
			return err
		}
		_, err = c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: userPeer(bob.id), RandomID: 3, Media: &tg.InputMediaUploadedDocument{File: in, MimeType: "text/plain"}})
		if !tgerr.Is(err, "YOU_BLOCKED_USER") {
			return fmt.Errorf("alice sent bob she blocked a file: %v", err)
		}
		if _, err := c.ContactsUnblock(ctx, &tg.ContactsUnblockRequest{ID: userPeer(bob.id)}); err != nil {
			return err
		}
		_, err = c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "sorry", RandomID: 2})
		if err != nil {
			return err
		}
		// what bob sent while blocked stays lost
		if h, err = history(ctx, c, bob.id); err != nil || len(h) != 1 {
			return fmt.Errorf("after unblocking alice has %d messages: %v", len(h), err)
		}
		return nil
	})
	var n int
	s.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM uploads WHERE owner_id=$1", alice.id).Scan(&n)
	if n != 0 {
		t.Fatalf("alice's refused file was stored (%d uploads)", n)
	}
}

func TestForwardOrigins(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")

	var ch *tg.Channel
	var post int
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: true, Title: "Wire"})
		if err != nil {
			return err
		}
		ch = channelOf(t, res)
		if _, err := c.ChannelsInviteToChannel(ctx, &tg.ChannelsInviteToChannelRequest{Channel: inCh(ch), Users: []tg.InputUserClass{&tg.InputUser{UserID: bob.id}}}); err != nil {
			return err
		}
		sent, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(ch), Message: "breaking", RandomID: 1})
		if err != nil {
			return err
		}
		post = sent.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID
		return nil
	})
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.AccountSetPrivacy(ctx, &tg.AccountSetPrivacyRequest{Key: &tg.InputPrivacyKeyForwards{},
			Rules: []tg.InputPrivacyRuleClass{&tg.InputPrivacyValueDisallowAll{}}}); err != nil {
			return err
		}
		_, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "don't link me", RandomID: 1})
		return err
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{FromPeer: peerCh(ch), ID: []int{post}, RandomID: []int64{10},
			ToPeer: userPeer(alice.id)})
		if err != nil {
			return err
		}
		m := sent(t, res)
		from, ok := m.FwdFrom.FromID.(*tg.PeerChannel)
		if !ok || from.ChannelID != ch.ID || m.FwdFrom.ChannelPost != post {
			return fmt.Errorf("post forward header %+v", m.FwdFrom)
		}
		var named bool
		for _, c := range res.(*tg.Updates).Chats {
			named = named || c.GetID() == ch.ID
		}
		if !named {
			return fmt.Errorf("post forward came without the channel")
		}

		h, err := history(ctx, c, carol.id)
		if err != nil || len(h) != 1 {
			return fmt.Errorf("carol's message: %v %d", err, len(h))
		}
		res, err = c.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{FromPeer: userPeer(carol.id), ID: []int{h[0].ID}, RandomID: []int64{11},
			ToPeer: userPeer(alice.id)})
		if err != nil {
			return err
		}
		m = sent(t, res)
		if m.FwdFrom.FromID != nil || m.FwdFrom.FromName != "carol Test" {
			return fmt.Errorf("carol hides her forwards, header %+v", m.FwdFrom)
		}
		return nil
	})
}

func TestCreatorComesBack(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	var group *tg.Channel
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		var err error
		if group, err = newGroup(ctx, c, t, "Mine", bob.id); err != nil {
			return err
		}
		if _, err := c.ChannelsLeaveChannel(ctx, inCh(group)); err != nil {
			return err
		}
		res, err := c.ChannelsJoinChannel(ctx, inCh(group))
		if err != nil {
			return fmt.Errorf("creator rejoining: %v", err)
		}
		back := channelOf(t, res.(*tg.MessagesChatInviteJoinResultOk).Updates)
		if !back.Creator || back.Left {
			return fmt.Errorf("back as %+v", back)
		}
		p, err := c.ChannelsGetParticipant(ctx, &tg.ChannelsGetParticipantRequest{Channel: inCh(group), Participant: &tg.InputPeerSelf{}})
		if err != nil {
			return err
		}
		if _, ok := p.Participant.(*tg.ChannelParticipantCreator); !ok {
			return fmt.Errorf("participant %T", p.Participant)
		}
		return nil
	})
	// anyone else needs a link back into a private group
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.ChannelsLeaveChannel(ctx, inCh(group)); err != nil {
			return err
		}
		if _, err := c.ChannelsJoinChannel(ctx, inCh(group)); !tgerr.Is(err, "CHANNEL_PRIVATE") {
			return fmt.Errorf("bob rejoined a private group: %v", err)
		}
		return nil
	})
}

func TestBanCountsForAdmins(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")

	var group *tg.Channel
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		var err error
		if group, err = newGroup(ctx, c, t, "Club", bob.id, carol.id); err != nil {
			return err
		}
		if _, err := c.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{Channel: inCh(group), Participant: userPeer(carol.id),
			BannedRights: tg.ChatBannedRights{ViewMessages: true}}); err != nil {
			return err
		}
		full, err := c.ChannelsGetFullChannel(ctx, inCh(group))
		if err != nil {
			return err
		}
		if n, ok := full.FullChat.(*tg.ChannelFull).GetKickedCount(); !ok || n != 1 {
			return fmt.Errorf("creator sees kicked=%d (%v)", n, ok)
		}
		return nil
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		full, err := c.ChannelsGetFullChannel(ctx, inCh(group))
		if err != nil {
			return err
		}
		f := full.FullChat.(*tg.ChannelFull)
		if _, ok := f.GetKickedCount(); ok {
			return fmt.Errorf("member sees the kicked count")
		}
		if _, ok := f.GetBannedCount(); ok {
			return fmt.Errorf("member sees the banned count")
		}
		return nil
	})
}

func TestUploadNeedsRights(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	var ch *tg.Channel
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: true, Title: "Read only"})
		if err != nil {
			return err
		}
		ch = channelOf(t, res)
		_, err = c.ChannelsInviteToChannel(ctx, &tg.ChannelsInviteToChannelRequest{Channel: inCh(ch), Users: []tg.InputUserClass{&tg.InputUser{UserID: bob.id}}})
		return err
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		in, err := uploader.NewUploader(c).FromBytes(ctx, "x.txt", []byte("not yours to post"))
		if err != nil {
			return err
		}
		_, err = c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: peerCh(ch), RandomID: 1, Media: &tg.InputMediaUploadedDocument{File: in, MimeType: "text/plain"}})
		if !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
			return fmt.Errorf("member posted a file to a channel: %v", err)
		}
		_, err = c.MessagesUploadMedia(ctx, &tg.MessagesUploadMediaRequest{Peer: peerCh(ch), Media: &tg.InputMediaUploadedDocument{File: in, MimeType: "text/plain"}})
		if !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
			return fmt.Errorf("member uploaded media for a channel: %v", err)
		}
		return nil
	})
	var n int
	s.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM uploads WHERE owner_id=$1", bob.id).Scan(&n)
	if n != 0 {
		t.Fatalf("refused upload was stored (%d)", n)
	}
}

func TestUserPhotosPaging(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		for i := range 3 {
			in, err := uploader.NewUploader(c).FromBytes(ctx, "me.png", testPNG(200+i, 200))
			if err != nil {
				return err
			}
			if _, err := c.PhotosUploadProfilePhoto(ctx, &tg.PhotosUploadProfilePhotoRequest{File: in}); err != nil {
				return err
			}
		}
		return nil
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		page := func(offset int, maxID int64, limit int) ([]int64, int, error) {
			res, err := c.PhotosGetUserPhotos(ctx, &tg.PhotosGetUserPhotosRequest{UserID: &tg.InputUser{UserID: alice.id}, Offset: offset, MaxID: maxID, Limit: limit})
			if err != nil {
				return nil, 0, err
			}
			var ps []tg.PhotoClass
			count := -1
			switch v := res.(type) {
			case *tg.PhotosPhotos:
				ps = v.Photos
			case *tg.PhotosPhotosSlice:
				ps, count = v.Photos, v.Count
			}
			var out []int64
			for _, p := range ps {
				out = append(out, p.GetID())
			}
			return out, count, nil
		}
		all, _, err := page(0, 0, 10)
		if err != nil || len(all) != 3 {
			return fmt.Errorf("all photos %v %v", all, err)
		}
		first, count, err := page(0, 0, 1)
		if err != nil || count != 3 || len(first) != 1 || first[0] != all[0] {
			return fmt.Errorf("first page %v of %d: %v", first, count, err)
		}
		// max_id is a place in the list, not an id order
		next, count, err := page(0, all[0], 1)
		if err != nil || count != 3 || len(next) != 1 || next[0] != all[1] {
			return fmt.Errorf("after %d: %v (%d) %v", all[0], next, count, err)
		}
		// offset -1 with max_id refetches that photo
		same, _, err := page(-1, all[1], 1)
		if err != nil || len(same) != 1 || same[0] != all[1] {
			return fmt.Errorf("refetch %d: %v %v", all[1], same, err)
		}
		return nil
	})
}
