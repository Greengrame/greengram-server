package e2e

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

// waitPush waits for a push holding an update match accepts.
func waitPush(t *testing.T, got <-chan tg.UpdatesClass, what string, match func(tg.UpdateClass) bool) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case u := <-got:
			if ups, ok := u.(*tg.Updates); ok {
				for _, x := range ups.Updates {
					if match(x) {
						return
					}
				}
			}
		case <-deadline:
			t.Fatalf("no push with %s", what)
		}
	}
}

func TestProfileSettings(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	// bob has a chat with alice, so her renames reach him
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(alice.id), Message: "hey", RandomID: 1})
		return err
	})
	bobPushes := make(chan tg.UpdatesClass, 64)
	s.live(t, bob, bobPushes)

	today := time.Now()
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		req := &tg.AccountUpdateProfileRequest{}
		req.SetFirstName("Alicia")
		req.SetLastName("Verde")
		req.SetAbout("grows things")
		u, err := c.AccountUpdateProfile(ctx, req)
		if err != nil {
			return err
		}
		if me := u.(*tg.User); me.FirstName != "Alicia" || me.LastName != "Verde" || !me.Self {
			return fmt.Errorf("updateProfile answered %+v", me)
		}
		long := &tg.AccountUpdateProfileRequest{}
		long.SetAbout(string(make([]rune, 71)))
		if _, err := c.AccountUpdateProfile(ctx, long); !tgerr.Is(err, "ABOUT_TOO_LONG") {
			return fmt.Errorf("71-char bio: %v", err)
		}

		if free, err := c.AccountCheckUsername(ctx, "bob"); err != nil || free {
			return fmt.Errorf("bob's name free: %v %v", free, err)
		}
		if free, err := c.AccountCheckUsername(ctx, "alicia_v"); err != nil || !free {
			return fmt.Errorf("alicia_v free: %v %v", free, err)
		}
		if _, err := c.AccountUpdateUsername(ctx, "bob"); !tgerr.Is(err, "USERNAME_OCCUPIED") {
			return fmt.Errorf("took bob's username: %v", err)
		}
		if _, err := c.AccountUpdateUsername(ctx, "alicia_v"); err != nil {
			return err
		}

		b := &tg.AccountUpdateBirthdayRequest{}
		b.SetBirthday(tg.Birthday{Day: today.Day(), Month: int(today.Month())})
		if _, err := c.AccountUpdateBirthday(ctx, b); err != nil {
			return err
		}
		if _, err := c.ContactsAddContact(ctx, &tg.ContactsAddContactRequest{ID: &tg.InputUser{UserID: bob.id}, FirstName: "Bob"}); err != nil {
			return err
		}

		gp := tg.GlobalPrivacySettings{ArchiveAndMuteNewNoncontactPeers: true, HideReadMarks: true}
		if _, err := c.AccountSetGlobalPrivacySettings(ctx, gp); err != nil {
			return err
		}
		got, err := c.AccountGetGlobalPrivacySettings(ctx)
		if err != nil || !got.ArchiveAndMuteNewNoncontactPeers || !got.HideReadMarks || got.KeepArchivedUnmuted {
			return fmt.Errorf("global privacy %+v %v", got, err)
		}
		if _, err := c.AccountSetContentSettings(ctx, &tg.AccountSetContentSettingsRequest{SensitiveEnabled: true}); err != nil {
			return err
		}
		if cs, err := c.AccountGetContentSettings(ctx); err != nil || !cs.SensitiveEnabled {
			return fmt.Errorf("content settings %+v %v", cs, err)
		}
		if _, err := c.AccountSetAccountTTL(ctx, tg.AccountDaysTTL{Days: 180}); err != nil {
			return err
		}
		if ttl, err := c.AccountGetAccountTTL(ctx); err != nil || ttl.Days != 180 {
			return fmt.Errorf("account ttl %+v %v", ttl, err)
		}
		if _, err := c.AccountSetAuthorizationTTL(ctx, 30); err != nil {
			return err
		}
		if a, err := c.AccountGetAuthorizations(ctx); err != nil || a.AuthorizationTTLDays != 30 {
			return fmt.Errorf("authorization ttl %+v %v", a, err)
		}
		if _, err := c.AccountSetContactSignUpNotification(ctx, true); err != nil {
			return err
		}
		if silent, err := c.AccountGetContactSignUpNotification(ctx); err != nil || !silent {
			return fmt.Errorf("sign-up notifications silent %v %v", silent, err)
		}
		return nil
	})

	waitPush(t, bobPushes, "alice's rename", func(u tg.UpdateClass) bool {
		v, ok := u.(*tg.UpdateUser)
		return ok && v.UserID == alice.id
	})

	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		full, err := c.UsersGetFullUser(ctx, &tg.InputUser{UserID: alice.id})
		if err != nil {
			return err
		}
		if full.FullUser.About != "grows things" {
			return fmt.Errorf("bio %q", full.FullUser.About)
		}
		u := full.Users[0].(*tg.User)
		if u.FirstName != "Alicia" || u.Username != "alicia_v" {
			return fmt.Errorf("bob sees %q %q", u.FirstName, u.Username)
		}
		// birthday privacy defaults to contacts, and alice has bob in hers
		if bd, ok := full.FullUser.GetBirthday(); !ok || bd.Day != today.Day() {
			return fmt.Errorf("birthday %+v %v", bd, ok)
		}
		if _, err := c.ContactsAddContact(ctx, &tg.ContactsAddContactRequest{ID: &tg.InputUser{UserID: alice.id}, FirstName: "Alice"}); err != nil {
			return err
		}
		bds, err := c.ContactsGetBirthdays(ctx)
		if err != nil {
			return err
		}
		if len(bds.Contacts) != 1 || bds.Contacts[0].ContactID != alice.id {
			return fmt.Errorf("birthdays %+v", bds.Contacts)
		}
		r, err := c.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: "alicia_v"})
		if err != nil || r.Peer.(*tg.PeerUser).UserID != alice.id {
			return fmt.Errorf("resolve new username: %v", err)
		}
		return nil
	})

	// the new username is also the sign-in name now
	b := s.browser(t)
	if code, _ := b.post("/api/login", map[string]string{"username": "alicia_v", "password": "correct horse alice"}); code != 200 {
		t.Fatalf("sign in with the new username: %d", code)
	}
}

func TestSavedDialogs(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(alice.id), Message: "worth keeping", RandomID: 1})
		return err
	})
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: &tg.InputPeerSelf{}, Message: "my note", RandomID: 1}); err != nil {
			return err
		}
		h, err := history(ctx, c, bob.id)
		if err != nil || len(h) != 1 {
			return fmt.Errorf("bob's message: %v %d", err, len(h))
		}
		res, err := c.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{FromPeer: userPeer(bob.id), ID: []int{h[0].ID},
			RandomID: []int64{2}, ToPeer: &tg.InputPeerSelf{}})
		if err != nil {
			return err
		}
		if p, ok := sent(t, res).GetSavedPeerID(); !ok || p.(*tg.PeerUser).UserID != bob.id {
			return fmt.Errorf("forward into saved has saved_peer %v %v", p, ok)
		}

		list, err := c.MessagesGetSavedDialogs(ctx, &tg.MessagesGetSavedDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20})
		if err != nil {
			return err
		}
		ds := list.(*tg.MessagesSavedDialogs)
		if len(ds.Dialogs) != 2 || len(ds.Messages) != 2 {
			return fmt.Errorf("saved dialogs %+v", ds)
		}
		if top := ds.Dialogs[0].(*tg.SavedDialog).Peer.(*tg.PeerUser).UserID; top != bob.id {
			return fmt.Errorf("newest saved dialog is %d, want bob", top)
		}

		hist, err := c.MessagesGetSavedHistory(ctx, &tg.MessagesGetSavedHistoryRequest{Peer: userPeer(bob.id), Limit: 20})
		if err != nil {
			return err
		}
		if msgs := hist.(*tg.MessagesMessagesSlice).Messages; len(msgs) != 1 || msgs[0].(*tg.Message).Message != "worth keeping" {
			return fmt.Errorf("saved history from bob %+v", msgs)
		}

		if _, err := c.MessagesToggleSavedDialogPin(ctx, &tg.MessagesToggleSavedDialogPinRequest{Pinned: true,
			Peer: &tg.InputDialogPeer{Peer: &tg.InputPeerSelf{}}}); err != nil {
			return err
		}
		pinned, err := c.MessagesGetPinnedSavedDialogs(ctx)
		if err != nil {
			return err
		}
		if pd := pinned.(*tg.MessagesSavedDialogs).Dialogs; len(pd) != 1 || pd[0].(*tg.SavedDialog).Peer.(*tg.PeerUser).UserID != alice.id {
			return fmt.Errorf("pinned saved %+v", pd)
		}

		if _, err := c.MessagesDeleteSavedHistory(ctx, &tg.MessagesDeleteSavedHistoryRequest{Peer: userPeer(bob.id)}); err != nil {
			return err
		}
		list, err = c.MessagesGetSavedDialogs(ctx, &tg.MessagesGetSavedDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20})
		if err != nil {
			return err
		}
		if ds := list.(*tg.MessagesSavedDialogs).Dialogs; len(ds) != 1 || !ds[0].(*tg.SavedDialog).Pinned {
			return fmt.Errorf("after deleting bob's saved dialog %+v", ds)
		}
		// the original chat with bob is untouched
		h, err = history(ctx, c, bob.id)
		if err != nil || len(h) != 1 {
			return fmt.Errorf("chat with bob after: %v %d", err, len(h))
		}
		return nil
	})
}

// TestSearchGlobalPaging: messages of one second in different chats all come
// through when the results span pages.
func TestSearchGlobalPaging(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")
	const sent = 12
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		one, err := newGroup(ctx, c, t, "One", bob.id)
		if err != nil {
			return err
		}
		two, err := newGroup(ctx, c, t, "Two", bob.id)
		if err != nil {
			return err
		}
		peers := []tg.InputPeerClass{userPeer(bob.id), peerCh(one), userPeer(carol.id), peerCh(two)}
		for i := 0; i < sent; i++ {
			if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peers[i%len(peers)], Message: "pagey " + strconv.Itoa(i), RandomID: int64(i + 1)}); err != nil {
				return err
			}
		}
		for _, q := range []string{"UPDATE messages SET date=1700000000 WHERE text LIKE 'pagey%'", "UPDATE channel_messages SET date=1700000000 WHERE text LIKE 'pagey%'"} {
			if _, err := s.db.Pool.Exec(ctx, q); err != nil {
				return err
			}
		}
		seen := map[string]bool{}
		req := &tg.MessagesSearchGlobalRequest{Q: "pagey", Filter: &tg.InputMessagesFilterEmpty{}, OffsetPeer: &tg.InputPeerEmpty{}, Limit: 5}
		for page := 0; page < sent; page++ {
			res, err := c.MessagesSearchGlobal(ctx, req)
			if err != nil {
				return err
			}
			slice := res.(*tg.MessagesMessagesSlice)
			if slice.Count != sent {
				return fmt.Errorf("count %d", slice.Count)
			}
			for _, m := range slice.Messages {
				text := m.(*tg.Message).Message
				if seen[text] {
					return fmt.Errorf("%q came twice", text)
				}
				seen[text] = true
			}
			rate, more := slice.GetNextRate()
			if !more {
				break
			}
			last := slice.Messages[len(slice.Messages)-1].(*tg.Message)
			req.OffsetRate, req.OffsetID = rate, last.ID
			switch p := last.PeerID.(type) {
			case *tg.PeerUser:
				req.OffsetPeer = userPeer(p.UserID)
			case *tg.PeerChannel:
				if p.ChannelID == one.ID {
					req.OffsetPeer = peerCh(one)
				} else {
					req.OffsetPeer = peerCh(two)
				}
			}
		}
		if len(seen) != sent {
			return fmt.Errorf("got %d of %d: %v", len(seen), sent, seen)
		}
		return nil
	})
}

// TestSearchPosts: public channels only, members or not, paged.
func TestSearchPosts(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	var news *tg.Channel
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		pub, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: true, Title: "News"})
		if err != nil {
			return err
		}
		news = channelOf(t, pub)
		if _, err := c.ChannelsUpdateUsername(ctx, &tg.ChannelsUpdateUsernameRequest{Channel: inCh(news), Username: "newsy"}); err != nil {
			return err
		}
		closed, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: true, Title: "Closed"})
		if err != nil {
			return err
		}
		for i := 0; i < 3; i++ {
			if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(news), Message: "posty #tagged " + strconv.Itoa(i), RandomID: int64(i + 1)}); err != nil {
				return err
			}
		}
		_, err = c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(channelOf(t, closed)), Message: "posty secret", RandomID: 9})
		return err
	})
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		flood, err := c.ChannelsCheckSearchPostsFlood(ctx, &tg.ChannelsCheckSearchPostsFloodRequest{Query: "posty"})
		if err != nil || !flood.QueryIsFree {
			return fmt.Errorf("flood %+v %v", flood, err)
		}
		req := &tg.ChannelsSearchPostsRequest{Query: "posty", OffsetPeer: &tg.InputPeerEmpty{}, Limit: 2}
		res, err := c.ChannelsSearchPosts(ctx, req)
		if err != nil {
			return err
		}
		page := res.(*tg.MessagesMessagesSlice)
		rate, more := page.GetNextRate()
		if page.Count != 3 || len(page.Messages) != 2 || !more || len(page.Chats) != 1 {
			return fmt.Errorf("first page %+v", page)
		}
		req.OffsetRate, req.OffsetID, req.OffsetPeer = rate, page.Messages[1].(*tg.Message).ID, peerCh(news)
		if res, err = c.ChannelsSearchPosts(ctx, req); err != nil {
			return err
		}
		if n := len(res.(*tg.MessagesMessagesSlice).Messages); n != 1 {
			return fmt.Errorf("second page %d", n)
		}
		res, err = c.ChannelsSearchPosts(ctx, &tg.ChannelsSearchPostsRequest{Hashtag: "tagged", OffsetPeer: &tg.InputPeerEmpty{}, Limit: 10})
		if err != nil || len(res.(*tg.MessagesMessagesSlice).Messages) != 3 {
			return fmt.Errorf("hashtag %+v %v", res, err)
		}
		return nil
	})
}

// TestPasskeysHidden: Web A shows "Loading..." forever without account.getPasskeys
// and offers a passkey it can't register unless the app config turns the entry off.
func TestPasskeysHidden(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		keys, err := c.AccountGetPasskeys(ctx)
		if err != nil || len(keys.Passkeys) != 0 {
			return fmt.Errorf("passkeys %+v %v", keys, err)
		}
		res, err := c.HelpGetAppConfig(ctx, 0)
		if err != nil {
			return err
		}
		for _, v := range res.(*tg.HelpAppConfig).Config.(*tg.JSONObject).Value {
			if b, ok := v.Value.(*tg.JSONBool); ok && v.Key == "settings_display_passkeys" && !b.Value {
				return nil
			}
		}
		return fmt.Errorf("settings_display_passkeys not off: %+v", res)
	})
}

// Android's settings row for Stars stays hidden without an explicit false,
// and it reads the key the way it comes off layer 231.
func TestAppConfigUnlocksStarsOnAndroid(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.HelpGetAppConfig(ctx, 0)
		if err != nil {
			return err
		}
		for _, v := range res.(*tg.HelpAppConfig).Config.(*tg.JSONObject).Value {
			if v.Key != "stars_purchase_blocked" {
				continue
			}
			if b, ok := v.Value.(*tg.JSONBool); ok && !b.Value {
				return nil
			}
			return fmt.Errorf("stars_purchase_blocked is %+v", v.Value)
		}
		return fmt.Errorf("no stars_purchase_blocked in %+v", res)
	})
}

func TestFindingPeople(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")

	var group, news *tg.Channel
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: true, Title: "Plant News"})
		if err != nil {
			return err
		}
		news = channelOf(t, res)
		if _, err := c.ChannelsUpdateUsername(ctx, &tg.ChannelsUpdateUsernameRequest{Channel: inCh(news), Username: "plantnews"}); err != nil {
			return err
		}
		// carol writes to alice first: alice gets the stranger bar
		_, err = c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(alice.id), Message: "hello stranger", RandomID: 1})
		return err
	})
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		var err error
		if group, err = newGroup(ctx, c, t, "Gardeners", bob.id); err != nil {
			return err
		}
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "seed swap see https://example.com/seeds", RandomID: 1}); err != nil {
			return err
		}
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "seed for you", RandomID: 2}); err != nil {
			return err
		}

		found, err := c.ContactsSearch(ctx, &tg.ContactsSearchRequest{Q: "garden", Limit: 10})
		if err != nil {
			return err
		}
		if len(found.MyResults) != 1 || found.MyResults[0].(*tg.PeerChannel).ChannelID != group.ID {
			return fmt.Errorf("search garden: %+v", found.MyResults)
		}
		found, err = c.ContactsSearch(ctx, &tg.ContactsSearchRequest{Q: "plant", Limit: 10})
		if err != nil {
			return err
		}
		if len(found.Results) != 1 || found.Results[0].(*tg.PeerChannel).ChannelID != news.ID {
			return fmt.Errorf("search plant: %+v", found.Results)
		}
		// bob by his display name, which only people who know him can search
		found, err = c.ContactsSearch(ctx, &tg.ContactsSearchRequest{Q: "bob te", Limit: 10})
		if err != nil {
			return err
		}
		if len(found.MyResults) != 1 || found.MyResults[0].(*tg.PeerUser).UserID != bob.id {
			return fmt.Errorf("search bob by name: %+v", found.MyResults)
		}

		all, err := c.MessagesSearchGlobal(ctx, &tg.MessagesSearchGlobalRequest{Q: "seed", Filter: &tg.InputMessagesFilterEmpty{}, OffsetPeer: &tg.InputPeerEmpty{}, Limit: 10})
		if err != nil {
			return err
		}
		if n := len(all.(*tg.MessagesMessagesSlice).Messages); n != 2 {
			return fmt.Errorf("global search for seed: %d results", n)
		}
		links, err := c.MessagesSearchGlobal(ctx, &tg.MessagesSearchGlobalRequest{Filter: &tg.InputMessagesFilterURL{}, OffsetPeer: &tg.InputPeerEmpty{}, Limit: 10})
		if err != nil {
			return err
		}
		if msgs := links.(*tg.MessagesMessagesSlice).Messages; len(msgs) != 1 {
			return fmt.Errorf("links: %d", len(msgs))
		}

		common, err := c.MessagesGetCommonChats(ctx, &tg.MessagesGetCommonChatsRequest{UserID: &tg.InputUser{UserID: bob.id}, Limit: 10})
		if err != nil || len(common.GetChats()) != 1 {
			return fmt.Errorf("common chats with bob: %v %+v", err, common)
		}
		full, err := c.UsersGetFullUser(ctx, &tg.InputUser{UserID: bob.id})
		if err != nil || full.FullUser.CommonChatsCount != 1 {
			return fmt.Errorf("common chats count %v", err)
		}

		top, err := c.ContactsGetTopPeers(ctx, &tg.ContactsGetTopPeersRequest{Correspondents: true, Groups: true, Limit: 10})
		if err != nil {
			return err
		}
		tp := top.(*tg.ContactsTopPeers)
		if len(tp.Categories) != 2 || len(tp.Categories[0].Peers) != 1 || len(tp.Categories[1].Peers) != 1 {
			return fmt.Errorf("top peers %+v", tp.Categories)
		}
		if _, err := c.ContactsResetTopPeerRating(ctx, &tg.ContactsResetTopPeerRatingRequest{Category: &tg.TopPeerCategoryCorrespondents{}, Peer: userPeer(bob.id)}); err != nil {
			return err
		}
		top, err = c.ContactsGetTopPeers(ctx, &tg.ContactsGetTopPeersRequest{Correspondents: true, Limit: 10})
		if err != nil || len(top.(*tg.ContactsTopPeers).Categories[0].Peers) != 0 {
			return fmt.Errorf("after reset %v %+v", err, top)
		}

		ps, err := c.MessagesGetPeerSettings(ctx, userPeer(carol.id))
		if err != nil {
			return err
		}
		if !ps.Settings.AddContact || !ps.Settings.BlockContact || !ps.Settings.ReportSpam {
			return fmt.Errorf("stranger bar %+v", ps.Settings)
		}
		if _, err := c.MessagesHidePeerSettingsBar(ctx, userPeer(carol.id)); err != nil {
			return err
		}
		if ps, err = c.MessagesGetPeerSettings(ctx, userPeer(carol.id)); err != nil || ps.Settings.AddContact {
			return fmt.Errorf("bar after hiding %+v %v", ps, err)
		}
		// bob is someone alice wrote to herself: no bar
		if ps, err = c.MessagesGetPeerSettings(ctx, userPeer(bob.id)); err != nil || ps.Settings.AddContact {
			return fmt.Errorf("bar for bob %+v %v", ps, err)
		}
		return nil
	})
}

func TestStatusPush(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(alice.id), Message: "hi", RandomID: 1})
		return err
	})
	got := make(chan tg.UpdatesClass, 64)
	s.live(t, bob, got)
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.AccountUpdateStatus(ctx, false)
		return err
	})
	waitPush(t, got, "alice online", func(u tg.UpdateClass) bool {
		v, ok := u.(*tg.UpdateUserStatus)
		if !ok {
			return false
		}
		_, online := v.Status.(*tg.UserStatusOnline)
		return v.UserID == alice.id && online
	})
}
