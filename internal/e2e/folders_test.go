package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

// waitUpdate returns the first pushed update match accepts.
func waitUpdate[T tg.UpdateClass](t *testing.T, got <-chan tg.UpdatesClass, match func(T) bool) T {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case u := <-got:
			if ups, ok := u.(*tg.Updates); ok {
				for _, x := range ups.Updates {
					if v, ok := x.(T); ok && match(v) {
						return v
					}
				}
			}
		case <-deadline:
			var zero T
			t.Fatalf("no %T pushed", zero)
			return zero
		}
	}
}

func TestChatFolders(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	pushes := make(chan tg.UpdatesClass, 128)
	other := s.live(t, alice, pushes)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var group *tg.Channel
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Work"})
		if err != nil {
			return err
		}
		group = channelOf(t, res.Updates)
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: &tg.InputPeerUser{UserID: bob.id}, Message: "hi bob", RandomID: 1}); err != nil {
			return err
		}

		f := &tg.DialogFilter{ID: 7, Title: tg.TextWithEntities{Text: "Work"}, Groups: true,
			IncludePeers: []tg.InputPeerClass{&tg.InputPeerUser{UserID: bob.id}}, PinnedPeers: []tg.InputPeerClass{peerCh(group)}}
		if _, err := c.MessagesUpdateDialogFilter(ctx, &tg.MessagesUpdateDialogFilterRequest{ID: 7, Filter: f}); err != nil {
			return err
		}
		empty := &tg.DialogFilter{ID: 8, Title: tg.TextWithEntities{Text: "Nothing"}}
		if _, err := c.MessagesUpdateDialogFilter(ctx, &tg.MessagesUpdateDialogFilterRequest{ID: 8, Filter: empty}); !tgerr.Is(err, "FILTER_INCLUDE_EMPTY") {
			return fmt.Errorf("empty folder: %v", err)
		}
		if _, err := c.MessagesUpdateDialogFilter(ctx, &tg.MessagesUpdateDialogFilterRequest{ID: 1, Filter: f}); !tgerr.Is(err, "FILTER_ID_INVALID") {
			return fmt.Errorf("folder id 1: %v", err)
		}
		g := &tg.DialogFilter{ID: 9, Title: tg.TextWithEntities{Text: "Bots"}, Bots: true}
		if _, err := c.MessagesUpdateDialogFilter(ctx, &tg.MessagesUpdateDialogFilterRequest{ID: 9, Filter: g}); err != nil {
			return err
		}
		// All chats moves between the two
		if _, err := c.MessagesUpdateDialogFiltersOrder(ctx, []int{9, 0, 7}); err != nil {
			return err
		}
		res2, err := c.MessagesGetDialogFilters(ctx)
		if err != nil {
			return err
		}
		var order []string
		for _, x := range res2.Filters {
			switch v := x.(type) {
			case *tg.DialogFilterDefault:
				order = append(order, "all")
			case *tg.DialogFilter:
				order = append(order, v.Title.Text)
				if v.ID == 7 && (len(v.IncludePeers) != 1 || len(v.PinnedPeers) != 1 || !v.Groups) {
					return fmt.Errorf("stored folder %+v", v)
				}
			}
		}
		if fmt.Sprint(order) != "[Bots all Work]" {
			return fmt.Errorf("folders in order %v", order)
		}
		if _, err := c.MessagesUpdateDialogFilter(ctx, &tg.MessagesUpdateDialogFilterRequest{ID: 9}); err != nil {
			return err
		}
		sug, err := c.MessagesGetSuggestedDialogFilters(ctx)
		if err != nil {
			return err
		}
		if len(sug) == 0 {
			return fmt.Errorf("no suggested folders")
		}
		return nil
	})

	// the other session heard about every change
	got := waitUpdate(t, pushes, func(u *tg.UpdateDialogFilter) bool { return u.ID == 7 })
	if f, ok := got.Filter.(*tg.DialogFilter); !ok || f.Title.Text != "Work" {
		t.Fatalf("pushed folder %+v", got.Filter)
	}
	waitUpdate(t, pushes, func(u *tg.UpdateDialogFilterOrder) bool { return len(u.Order) == 3 })
	waitUpdate(t, pushes, func(u *tg.UpdateDialogFilter) bool { return u.ID == 9 && u.Filter == nil })

	// archive the group: it leaves the main list and shows up in folder 1
	st, err := other.UpdatesGetState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		up, err := c.FoldersEditPeerFolders(ctx, []tg.InputFolderPeer{{Peer: peerCh(group), FolderID: 1}})
		if err != nil {
			return err
		}
		fp, ok := up.(*tg.Updates).Updates[0].(*tg.UpdateFolderPeers)
		if !ok || fp.Pts != st.Pts+1 || fp.PtsCount != 1 {
			return fmt.Errorf("editPeerFolders %+v (state pts %d)", up, st.Pts)
		}
		return nil
	})
	waitUpdate(t, pushes, func(u *tg.UpdateFolderPeers) bool { return len(u.FolderPeers) == 1 && u.FolderPeers[0].FolderID == 1 })

	// and it's in the journal for a session that was away
	d, err := other.UpdatesGetDifference(ctx, &tg.UpdatesGetDifferenceRequest{Pts: st.Pts, Date: st.Date})
	if err != nil {
		t.Fatal(err)
	}
	if diff, ok := d.(*tg.UpdatesDifference); !ok || len(diff.OtherUpdates) != 1 {
		t.Fatalf("difference after archiving %+v", d)
	}

	dialogsOf := func(folder int, exclude bool) []tg.DialogClass {
		req := &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20, ExcludePinned: exclude}
		if folder != 0 {
			req.SetFolderID(folder)
		}
		res, err := other.MessagesGetDialogs(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		switch v := res.(type) {
		case *tg.MessagesDialogs:
			return v.Dialogs
		case *tg.MessagesDialogsSlice:
			return v.Dialogs
		}
		t.Fatalf("getDialogs %T", res)
		return nil
	}
	main := dialogsOf(0, false)
	if _, ok := main[0].(*tg.DialogFolder); !ok {
		t.Fatalf("main list doesn't start with the archive: %+v", main)
	}
	for _, x := range main[1:] {
		if p, ok := x.(*tg.Dialog).Peer.(*tg.PeerChannel); ok && p.ChannelID == group.ID {
			t.Fatal("archived group still in the main list")
		}
	}
	for _, x := range dialogsOf(0, true) {
		if _, ok := x.(*tg.DialogFolder); ok {
			t.Fatal("archive row with exclude_pinned")
		}
	}
	arch := dialogsOf(1, false)
	if len(arch) != 1 || arch[0].(*tg.Dialog).FolderID != 1 {
		t.Fatalf("archive %+v", arch)
	}

	// back to the main list
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.FoldersEditPeerFolders(ctx, []tg.InputFolderPeer{{Peer: peerCh(group), FolderID: 0}})
		return err
	})
	if len(dialogsOf(1, false)) != 0 {
		t.Fatal("archive not empty after unarchiving")
	}
}
