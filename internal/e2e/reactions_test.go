package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
)

func waitReactions(t *testing.T, got <-chan tg.UpdatesClass, msgID int) *tg.UpdateMessageReactions {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case u := <-got:
			if ups, ok := u.(*tg.Updates); ok {
				for _, x := range ups.Updates {
					if r, ok := x.(*tg.UpdateMessageReactions); ok && r.MsgID == msgID {
						return r
					}
				}
			}
		case <-deadline:
			t.Fatalf("no reactions push for %d", msgID)
			return nil
		}
	}
}

func emoji(e string) []tg.ReactionClass { return []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: e}} }

func sentMsg(t *testing.T, u tg.UpdatesClass) int {
	t.Helper()
	for _, x := range u.(*tg.Updates).Updates {
		if id, ok := x.(*tg.UpdateMessageID); ok {
			return id.ID
		}
	}
	t.Fatalf("no UpdateMessageID in %+v", u)
	return 0
}

func TestReactions(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	alicePushes := make(chan tg.UpdatesClass, 128)
	aliceLive := s.live(t, alice, alicePushes)
	bobPushes := make(chan tg.UpdatesClass, 128)
	bobLive := s.live(t, bob, bobPushes)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if _, err := aliceLive.MessagesGetAvailableReactions(ctx, 0); err != nil {
		t.Fatal(err)
	}

	// private: Bob reacts to Alice's message
	res, err := aliceLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "react to me", RandomID: 1})
	if err != nil {
		t.Fatal(err)
	}
	inAlice := sentMsg(t, res)
	inBob := waitNew(t, bobPushes, "react to me").ID

	// one reaction without Premium: the newest stays
	up, err := bobLive.MessagesSendReaction(ctx, &tg.MessagesSendReactionRequest{Peer: userPeer(alice.id), MsgID: inBob,
		Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "🔥"}, &tg.ReactionEmoji{Emoticon: "👍"}}})
	if err != nil {
		t.Fatal(err)
	}
	mine := up.(*tg.Updates).Updates[0].(*tg.UpdateMessageReactions)
	if len(mine.Reactions.Results) != 1 || mine.Reactions.Results[0].Reaction.(*tg.ReactionEmoji).Emoticon != "👍" || mine.Reactions.Results[0].ChosenOrder != 1 {
		t.Fatalf("bob's view %+v", mine.Reactions)
	}
	theirs := waitReactions(t, alicePushes, inAlice)
	rr := theirs.Reactions.RecentReactions
	if len(theirs.Reactions.Results) != 1 || theirs.Reactions.Results[0].ChosenOrder != 0 || len(rr) != 1 || !rr[0].Unread || rr[0].My ||
		rr[0].PeerID.(*tg.PeerUser).UserID != bob.id {
		t.Fatalf("alice's view %+v", theirs.Reactions)
	}

	dl, err := aliceLive.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: userPeer(bob.id)}})
	if err != nil {
		t.Fatal(err)
	}
	if n := dl.Dialogs[0].(*tg.Dialog).UnreadReactionsCount; n != 1 {
		t.Fatalf("unread reactions %d", n)
	}
	unread, err := aliceLive.MessagesGetUnreadReactions(ctx, &tg.MessagesGetUnreadReactionsRequest{Peer: userPeer(bob.id), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if ms := unread.(*tg.MessagesMessagesSlice).Messages; len(ms) != 1 || ms[0].(*tg.Message).ID != inAlice {
		t.Fatalf("unread reactions list %+v", ms)
	}
	if _, err := aliceLive.MessagesReadReactions(ctx, &tg.MessagesReadReactionsRequest{Peer: userPeer(bob.id)}); err != nil {
		t.Fatal(err)
	}
	dl, err = aliceLive.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: userPeer(bob.id)}})
	if err != nil {
		t.Fatal(err)
	}
	if n := dl.Dialogs[0].(*tg.Dialog).UnreadReactionsCount; n != 0 {
		t.Fatalf("unread reactions after read %d", n)
	}

	msgs, err := history(ctx, aliceLive, bob.id)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := msgs[0].GetReactions(); !ok || len(r.Results) != 1 || r.Results[0].Count != 1 {
		t.Fatalf("history reactions %+v", msgs[0])
	}
	list, err := aliceLive.MessagesGetMessageReactionsList(ctx, &tg.MessagesGetMessageReactionsListRequest{Peer: userPeer(bob.id), ID: inAlice, Limit: 10})
	if err != nil || list.Count != 1 || len(list.Reactions) != 1 {
		t.Fatalf("reactions list %v %+v", err, list)
	}

	// taking it back clears it for Alice too
	if _, err := bobLive.MessagesSendReaction(ctx, &tg.MessagesSendReactionRequest{Peer: userPeer(alice.id), MsgID: inBob}); err != nil {
		t.Fatal(err)
	}
	if cleared := waitReactions(t, alicePushes, inAlice); len(cleared.Reactions.Results) != 0 {
		t.Fatalf("after unreact %+v", cleared.Reactions)
	}
	got, err := aliceLive.MessagesGetMessagesReactions(ctx, &tg.MessagesGetMessagesReactionsRequest{Peer: userPeer(bob.id), ID: []int{inAlice}})
	if err != nil || len(got.(*tg.Updates).Updates) != 1 {
		t.Fatalf("getMessagesReactions %v %+v", err, got)
	}

	// Saved Messages: anyone may react
	res, err = aliceLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: &tg.InputPeerSelf{}, Message: "note", RandomID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aliceLive.MessagesSendReaction(ctx, &tg.MessagesSendReactionRequest{Peer: &tg.InputPeerSelf{}, MsgID: sentMsg(t, res), Reaction: emoji("❤")}); err != nil {
		t.Fatal(err)
	}

	// group: Bob reacts to Alice's message, Alice is told as the author
	cr, err := aliceLive.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Team", Users: []tg.InputUserClass{&tg.InputUser{UserID: bob.id}}})
	if err != nil {
		t.Fatal(err)
	}
	group := channelOf(t, cr.Updates)
	full, err := aliceLive.ChannelsGetFullChannel(ctx, inCh(group))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := full.FullChat.(*tg.ChannelFull).AvailableReactions.(*tg.ChatReactionsAll); !ok {
		t.Fatalf("group reactions %+v", full.FullChat)
	}
	if _, err := aliceLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: "group post", RandomID: 3}); err != nil {
		t.Fatal(err)
	}
	post := waitChannelText(t, bobPushes, "group post")
	if _, err := bobLive.MessagesSendReaction(ctx, &tg.MessagesSendReactionRequest{Peer: peerCh(group), MsgID: post.ID, Reaction: emoji("🎉")}); err != nil {
		t.Fatal(err)
	}
	author := waitReactions(t, alicePushes, post.ID)
	if len(author.Reactions.RecentReactions) != 1 || !author.Reactions.RecentReactions[0].Unread || author.Reactions.Min {
		t.Fatalf("author's group view %+v", author.Reactions)
	}
	unread, err = aliceLive.MessagesGetUnreadReactions(ctx, &tg.MessagesGetUnreadReactionsRequest{Peer: peerCh(group), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	cm := unread.(*tg.MessagesChannelMessages)
	if len(cm.Messages) != 1 {
		t.Fatalf("group unread reactions %+v", cm.Messages)
	}
	if r, ok := cm.Messages[0].(*tg.Message).GetReactions(); !ok || r.Results[0].Count != 1 {
		t.Fatalf("group message reactions %+v", cm.Messages[0])
	}
	if _, err := aliceLive.MessagesReadReactions(ctx, &tg.MessagesReadReactionsRequest{Peer: peerCh(group)}); err != nil {
		t.Fatal(err)
	}
	dg, err := aliceLive.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: peerCh(group)}})
	if err != nil {
		t.Fatal(err)
	}
	if n := dg.Dialogs[0].(*tg.Dialog).UnreadReactionsCount; n != 0 {
		t.Fatalf("group unread reactions after read %d", n)
	}
}

// A reactions export (gramsrv's stickerfetch layout) replaces the list
// made from emoji packs.
func TestImportReactions(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "global_json"), 0o755)
	os.MkdirAll(filepath.Join(dir, "reactions"), 0o755)
	doc := func(id int64, mime, ext string) string {
		body := []byte(fmt.Sprintf("artwork %d", id))
		os.WriteFile(filepath.Join(dir, "reactions", fmt.Sprintf("%d.%s", id, ext)), body, 0o644)
		return fmt.Sprintf(`{"id": %d, "mime_type": %q, "size": %d, "attributes": [{"_": "DocumentAttributeImageSize", "w": 100, "h": 100}]}`, id, mime, len(body))
	}
	raw := fmt.Sprintf(`{"result": {"reactions": [{"reaction": "👍", "title": "Thumbs Up", "static_icon": %s, "appear_animation": %s,
		"select_animation": %s, "activate_animation": %s, "effect_animation": %s}]}}`,
		doc(11, "image/webp", "webp"), doc(12, "application/x-tgsticker", "tgs"), doc(13, "application/x-tgsticker", "tgs"),
		doc(14, "application/x-tgsticker", "tgs"), doc(15, "application/x-tgsticker", "tgs"))
	os.WriteFile(filepath.Join(dir, "global_json", "available_reactions_raw.json"), []byte(raw), 0o644)
	if n, err := s.media.ImportReactions(context.Background(), dir); err != nil || n != 1 {
		t.Fatalf("import: %d %v", n, err)
	}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.MessagesGetAvailableReactions(ctx, 0)
		if err != nil {
			return err
		}
		list := res.(*tg.MessagesAvailableReactions)
		if len(list.Reactions) != 1 || list.Reactions[0].Title != "Thumbs Up" || list.Reactions[0].StaticIcon.(*tg.Document).MimeType != "image/webp" {
			return fmt.Errorf("available reactions %+v", list.Reactions)
		}
		if again, err := c.MessagesGetAvailableReactions(ctx, list.Hash); err != nil {
			return err
		} else if _, ok := again.(*tg.MessagesAvailableReactionsNotModified); !ok {
			return fmt.Errorf("same hash: %T", again)
		}
		return nil
	})
}
