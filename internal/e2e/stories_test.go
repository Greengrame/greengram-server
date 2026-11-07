package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/telegram/uploader"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

func postStory(ctx context.Context, c *tg.Client, random int64, caption string, rules ...tg.InputPrivacyRuleClass) (*tg.StoryItem, error) {
	in, err := uploader.NewUploader(c).FromBytes(ctx, "story.png", testPNG(720, 1280))
	if err != nil {
		return nil, err
	}
	res, err := c.StoriesSendStory(ctx, &tg.StoriesSendStoryRequest{Peer: &tg.InputPeerSelf{}, Media: &tg.InputMediaUploadedPhoto{File: in},
		Caption: caption, PrivacyRules: rules, RandomID: random})
	if err != nil {
		return nil, err
	}
	var (
		item  *tg.StoryItem
		idSet bool
	)
	for _, u := range res.(*tg.Updates).Updates {
		switch v := u.(type) {
		case *tg.UpdateStoryID:
			idSet = v.RandomID == random
		case *tg.UpdateStory:
			item, _ = v.Story.(*tg.StoryItem)
		}
	}
	if item == nil || !idSet || !item.Out {
		return nil, fmt.Errorf("sendStory reply %+v", res)
	}
	return item, nil
}

// waitStory is the next updateStory push about owner.
func waitStory(t *testing.T, got <-chan tg.UpdatesClass, owner int64) (tg.StoryItemClass, *tg.Updates) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case u := <-got:
			if ups, ok := u.(*tg.Updates); ok {
				for _, x := range ups.Updates {
					if s, ok := x.(*tg.UpdateStory); ok && s.Peer.(*tg.PeerUser).UserID == owner {
						return s.Story, ups
					}
				}
			}
		case <-deadline:
			t.Fatal("no updateStory push")
			return nil, nil
		}
	}
}

func peerIDs(all *tg.StoriesAllStories) map[int64][]int {
	out := map[int64][]int{}
	for _, ps := range all.PeerStories {
		for _, s := range ps.Stories {
			out[ps.Peer.(*tg.PeerUser).UserID] = append(out[ps.Peer.(*tg.PeerUser).UserID], s.GetID())
		}
	}
	return out
}

func TestStories(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// bob and carol have alice in their books (her feed audience); alice
	// has bob, so "contacts" means bob
	for _, p := range []person{bob, carol} {
		run(t, s.client(p.storage), func(ctx context.Context, c *tg.Client) error {
			_, err := c.ContactsAddContact(ctx, &tg.ContactsAddContactRequest{ID: &tg.InputUser{UserID: alice.id}, FirstName: "Alice"})
			return err
		})
	}
	bobPushes := make(chan tg.UpdatesClass, 64)
	bobLive := s.live(t, bob, bobPushes)
	carolLive := s.live(t, carol, make(chan tg.UpdatesClass, 64))

	var first, friends, mine *tg.StoryItem
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.ContactsAddContact(ctx, &tg.ContactsAddContactRequest{ID: &tg.InputUser{UserID: bob.id}, FirstName: "Bob"}); err != nil {
			return err
		}
		if _, err := c.ContactsEditCloseFriends(ctx, []int64{bob.id}); err != nil {
			return err
		}
		var err error
		if first, err = postStory(ctx, c, 1, "hello world", &tg.InputPrivacyValueAllowAll{}); err != nil {
			return err
		}
		if !first.Public || first.ExpireDate-first.Date != 86400 || first.Caption != "hello world" {
			return fmt.Errorf("first story %+v", first)
		}
		if _, err := postStory(ctx, c, 1, "again", &tg.InputPrivacyValueAllowAll{}); !tgerr.Is(err, "RANDOM_ID_DUPLICATE") {
			return fmt.Errorf("same random_id: %v", err)
		}
		_, err = c.StoriesSendStory(ctx, &tg.StoriesSendStoryRequest{Peer: &tg.InputPeerSelf{}, Media: &tg.InputMediaEmpty{}, RandomID: 9, Period: 172800})
		if !tgerr.Is(err, "PREMIUM_ACCOUNT_REQUIRED") {
			return fmt.Errorf("48h without premium: %v", err)
		}
		if friends, err = postStory(ctx, c, 2, "", &tg.InputPrivacyValueAllowCloseFriends{}); err != nil {
			return err
		}
		mine, err = postStory(ctx, c, 3, "", &tg.InputPrivacyValueAllowUsers{Users: []tg.InputUserClass{&tg.InputUser{UserID: carol.id}}})
		return err
	})

	// the ring shows up live: the push and the user in it
	item, ups := waitStory(t, bobPushes, alice.id)
	if item.GetID() != first.ID {
		t.Fatalf("first push %+v", item)
	}
	for _, u := range ups.Users {
		if v := u.(*tg.User); v.ID == alice.id && v.StoriesMaxID.MaxID == 0 {
			t.Fatalf("alice without stories_max_id in the push: %+v", v)
		}
	}
	if it := item.(*tg.StoryItem); it.Out || it.Privacy != nil || it.Media == nil {
		t.Fatalf("bob's copy %+v", it)
	}

	// bob: first and close friends, not the one for carol only
	all, err := bobLive.StoriesGetAllStories(ctx, &tg.StoriesGetAllStoriesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	feed := all.(*tg.StoriesAllStories)
	if got := peerIDs(feed)[alice.id]; fmt.Sprint(got) != fmt.Sprint([]int{first.ID, friends.ID}) {
		t.Fatalf("bob's feed %v", got)
	}
	if again, err := bobLive.StoriesGetAllStories(ctx, &tg.StoriesGetAllStoriesRequest{State: feed.State}); err != nil {
		t.Fatal(err)
	} else if _, ok := again.(*tg.StoriesAllStoriesNotModified); !ok {
		t.Fatalf("same state, got %T", again)
	}
	max, err := bobLive.StoriesGetPeerMaxIDs(ctx, []tg.InputPeerClass{userPeer(alice.id), userPeer(carol.id)})
	if err != nil || len(max) != 2 || max[0].MaxID != friends.ID || max[1].MaxID != 0 {
		t.Fatalf("max ids %v %+v", err, max)
	}
	// carol: first and hers
	cps, err := carolLive.StoriesGetPeerStories(ctx, userPeer(alice.id))
	if err != nil {
		t.Fatal(err)
	}
	if len(cps.Stories.Stories) != 2 || cps.Stories.Stories[1].GetID() != mine.ID {
		t.Fatalf("carol sees %+v", cps.Stories.Stories)
	}

	// bob watches the first one and likes it
	read, err := bobLive.StoriesReadStories(ctx, &tg.StoriesReadStoriesRequest{Peer: userPeer(alice.id), MaxID: first.ID})
	if err != nil || len(read) != 1 || read[0] != first.ID {
		t.Fatalf("read %v %v", err, read)
	}
	if _, err := bobLive.StoriesSendReaction(ctx, &tg.StoriesSendReactionRequest{Peer: userPeer(alice.id), StoryID: first.ID,
		Reaction: &tg.ReactionEmoji{Emoticon: "❤"}}); err != nil {
		t.Fatal(err)
	}
	ps, err := bobLive.StoriesGetPeerStories(ctx, userPeer(alice.id))
	if err != nil || ps.Stories.MaxReadID != first.ID {
		t.Fatalf("read mark %v %+v", err, ps)
	}
	if r, ok := ps.Stories.Stories[0].(*tg.StoryItem).SentReaction.(*tg.ReactionEmoji); !ok || r.Emoticon != "❤" {
		t.Fatalf("sent reaction %+v", ps.Stories.Stories[0])
	}

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		views, err := c.StoriesGetStoriesViews(ctx, &tg.StoriesGetStoriesViewsRequest{Peer: &tg.InputPeerSelf{}, ID: []int{first.ID, friends.ID}})
		if err != nil {
			return err
		}
		if v := views.Views[0]; v.ViewsCount != 1 || v.ReactionsCount != 1 || len(v.RecentViewers) != 1 || v.RecentViewers[0] != bob.id {
			return fmt.Errorf("views %+v", views.Views)
		}
		// with viewers on the story, getFullUser(self) still names self first
		full, err := c.UsersGetFullUser(ctx, &tg.InputUserSelf{})
		if err != nil {
			return err
		}
		if u, ok := full.Users[0].(*tg.User); !ok || !u.Self {
			return fmt.Errorf("first user of getFullUser(self): %+v", full.Users[0])
		}
		list, err := c.StoriesGetStoryViewsList(ctx, &tg.StoriesGetStoryViewsListRequest{Peer: &tg.InputPeerSelf{}, ID: first.ID, Limit: 10})
		if err != nil {
			return err
		}
		if len(list.Views) != 1 || list.ViewsCount != 1 || list.ReactionsCount != 1 {
			return fmt.Errorf("viewers %+v", list)
		}
		v := list.Views[0].(*tg.StoryView)
		if r, ok := v.Reaction.(*tg.ReactionEmoji); v.UserID != bob.id || !ok || r.Emoticon != "❤" {
			return fmt.Errorf("viewer %+v", v)
		}
		// bob is her close friend in the user objects she gets
		users, err := c.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUser{UserID: bob.id}})
		if err != nil || !users[0].(*tg.User).CloseFriend {
			return fmt.Errorf("close friend flag %v %+v", err, users)
		}
		// her own stories carry the rules
		own, err := c.StoriesGetPeerStories(ctx, &tg.InputPeerSelf{})
		if err != nil || len(own.Stories.Stories) != 3 {
			return fmt.Errorf("own %v %+v", err, own)
		}
		if _, ok := own.Stories.Stories[1].(*tg.StoryItem).Privacy[0].(*tg.PrivacyValueAllowCloseFriends); !ok {
			return fmt.Errorf("close friends rules %+v", own.Stories.Stories[1])
		}
		return nil
	})

	// hidden: out of carol's main feed, into the hidden one
	if _, err := carolLive.StoriesTogglePeerStoriesHidden(ctx, &tg.StoriesTogglePeerStoriesHiddenRequest{Peer: userPeer(alice.id), Hidden: true}); err != nil {
		t.Fatal(err)
	}
	for hidden, want := range map[bool]int{false: 0, true: 2} {
		all, err := carolLive.StoriesGetAllStories(ctx, &tg.StoriesGetAllStoriesRequest{Hidden: hidden})
		if err != nil {
			t.Fatal(err)
		}
		if n := len(peerIDs(all.(*tg.StoriesAllStories))[alice.id]); n != want {
			t.Fatalf("hidden=%v: %d stories", hidden, n)
		}
	}

	// "hide my stories from" carol: even the public one goes
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.ContactsBlock(ctx, &tg.ContactsBlockRequest{ID: userPeer(carol.id), MyStoriesFrom: true})
		return err
	})
	if cps, err := carolLive.StoriesGetPeerStories(ctx, userPeer(alice.id)); err != nil || len(cps.Stories.Stories) != 0 {
		t.Fatalf("carol after hide-from %v %+v", err, cps)
	}

	// expiry: the first story leaves the feed, stays in the archive, and
	// pinned it's on the profile
	if _, err := s.db.Pool.Exec(ctx, "UPDATE stories SET expire_date = date - 1 WHERE owner_id=$1 AND id=$2", alice.id, first.ID); err != nil {
		t.Fatal(err)
	}
	all, err = bobLive.StoriesGetAllStories(ctx, &tg.StoriesGetAllStoriesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := peerIDs(all.(*tg.StoriesAllStories))[alice.id]; fmt.Sprint(got) != fmt.Sprint([]int{friends.ID}) {
		t.Fatalf("feed after expiry %v", got)
	}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		arch, err := c.StoriesGetStoriesArchive(ctx, &tg.StoriesGetStoriesArchiveRequest{Peer: &tg.InputPeerSelf{}, Limit: 10})
		if err != nil || arch.Count != 3 || arch.Stories[2].GetID() != first.ID {
			return fmt.Errorf("archive %v %+v", err, arch)
		}
		if ids, err := c.StoriesTogglePinned(ctx, &tg.StoriesTogglePinnedRequest{Peer: &tg.InputPeerSelf{}, ID: []int{first.ID}, Pinned: true}); err != nil || len(ids) != 1 {
			return fmt.Errorf("pin %v %v", err, ids)
		}
		return nil
	})
	pinned, err := bobLive.StoriesGetPinnedStories(ctx, &tg.StoriesGetPinnedStoriesRequest{Peer: userPeer(alice.id), Limit: 10})
	if err != nil || pinned.Count != 1 || pinned.Stories[0].GetID() != first.ID {
		t.Fatalf("profile stories %v %+v", err, pinned)
	}

	// delete: bob's ring loses the story live
	drain(bobPushes)
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		ids, err := c.StoriesDeleteStories(ctx, &tg.StoriesDeleteStoriesRequest{Peer: &tg.InputPeerSelf{}, ID: []int{friends.ID}})
		if err != nil || len(ids) != 1 {
			return fmt.Errorf("delete %v %v", err, ids)
		}
		return nil
	})
	if item, _ := waitStory(t, bobPushes, alice.id); item.TypeID() != tg.StoryItemDeletedTypeID || item.GetID() != friends.ID {
		t.Fatalf("delete push %+v", item)
	}
	if max, err := bobLive.StoriesGetPeerMaxIDs(ctx, []tg.InputPeerClass{userPeer(alice.id)}); err != nil || max[0].MaxID != 0 {
		t.Fatalf("max after delete %v %+v", err, max)
	}
}

func drain(ch <-chan tg.UpdatesClass) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
