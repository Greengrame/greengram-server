package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
)

// Reading your own message moves your inbox mark and nothing else; someone
// else reading it moves the outbox mark and shows in "seen by".
func TestSeenBy(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	aliceLive := s.live(t, alice, make(chan tg.UpdatesClass, 128))
	bobPushes := make(chan tg.UpdatesClass, 128)
	bobLive := s.live(t, bob, bobPushes)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	g, err := newGroup(ctx, aliceLive, t, "seen", bob.id)
	if err != nil {
		t.Fatal(err)
	}
	res, err := aliceLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(g), Message: "anyone?", RandomID: 1})
	if err != nil {
		t.Fatal(err)
	}
	id := sentMsg(t, res)
	waitChannelText(t, bobPushes, "anyone?")

	outbox := func() int {
		t.Helper()
		full, err := aliceLive.ChannelsGetFullChannel(ctx, inCh(g))
		if err != nil {
			t.Fatal(err)
		}
		return full.FullChat.(*tg.ChannelFull).ReadOutboxMaxID
	}
	if _, err := aliceLive.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{Channel: inCh(g), MaxID: id}); err != nil {
		t.Fatal(err)
	}
	if n := outbox(); n >= id {
		t.Fatalf("own read moved the outbox mark to %d, message %d", n, id)
	}
	seen, err := aliceLive.MessagesGetMessageReadParticipants(ctx, &tg.MessagesGetMessageReadParticipantsRequest{Peer: peerCh(g), MsgID: id})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("seen by %+v before anyone read", seen)
	}
	// a service message from bob (title change) isn't bob seeing anything
	if _, err := bobLive.ChannelsEditTitle(ctx, &tg.ChannelsEditTitleRequest{Channel: inCh(g), Title: "seen 2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := aliceLive.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{Channel: inCh(g)}); err != nil {
		t.Fatal(err)
	}
	if n := outbox(); n >= id {
		t.Fatalf("reading bob's service message moved the outbox mark to %d, message %d", n, id)
	}
	if _, err := bobLive.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{Channel: inCh(g), MaxID: id}); err != nil {
		t.Fatal(err)
	}
	if n := outbox(); n != id {
		t.Fatalf("outbox mark %d after bob read %d", n, id)
	}
	seen, err = aliceLive.MessagesGetMessageReadParticipants(ctx, &tg.MessagesGetMessageReadParticipantsRequest{Peer: peerCh(g), MsgID: id})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].UserID != bob.id {
		t.Fatalf("seen by %+v", seen)
	}
}

// A post's eye counter is how many members have read up to it.
func TestPostViews(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	c := s.live(t, alice, make(chan tg.UpdatesClass, 128))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: true, Title: "views"})
	if err != nil {
		t.Fatal(err)
	}
	var ch *tg.Channel
	for _, x := range res.(*tg.Updates).Chats {
		if v, ok := x.(*tg.Channel); ok {
			ch = v
		}
	}
	sent, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(ch), Message: "post", RandomID: 1})
	if err != nil {
		t.Fatal(err)
	}
	id := sentMsg(t, sent)
	dlg, err := c.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: peerCh(ch)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range dlg.Messages {
		if msg, ok := m.(*tg.Message); ok && msg.ID == id && msg.Views == 0 {
			t.Fatal("the dialog's top post has no views field, Web A would never ask for them")
		}
	}
	if _, err := c.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{Channel: inCh(ch), MaxID: id}); err != nil {
		t.Fatal(err)
	}
	hist, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peerCh(ch), Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range hist.(*tg.MessagesChannelMessages).Messages {
		if msg, ok := m.(*tg.Message); ok && msg.ID == id {
			found = true
			if msg.Views != 1 {
				t.Fatalf("history shows %d views, flags %v", msg.Views, msg.Flags)
			}
		}
	}
	if !found {
		t.Fatal("post not in history")
	}
	views, err := c.MessagesGetMessagesViews(ctx, &tg.MessagesGetMessagesViewsRequest{Peer: peerCh(ch), ID: []int{id}})
	if err != nil {
		t.Fatal(err)
	}
	if views.Views[0].Views != 1 {
		t.Fatalf("getMessagesViews says %d", views.Views[0].Views)
	}
}

// A star reaction takes Stars from the reactor, gives them to the author and
// counts on the message; a retry with the same random id changes nothing.
func TestStarReactions(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	alicePushes := make(chan tg.UpdatesClass, 128)
	aliceLive := s.live(t, alice, alicePushes)
	bobPushes := make(chan tg.UpdatesClass, 128)
	bobLive := s.live(t, bob, bobPushes)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if _, err := s.db.Pool.Exec(ctx, `INSERT INTO star_ledger(user_id, amount, reason, reference, created_by, created_at, transaction_id)
		VALUES ($1, 100, 'admin', 'seed:star', $1, 1, 'seed-star')`, bob.id); err != nil {
		t.Fatal(err)
	}
	balance := func(u int64) int64 {
		t.Helper()
		var n int64
		if err := s.db.Pool.QueryRow(ctx, "SELECT coalesce(sum(amount), 0) FROM star_ledger WHERE user_id=$1", u).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	g, err := newGroup(ctx, aliceLive, t, "stars", bob.id)
	if err != nil {
		t.Fatal(err)
	}
	res, err := aliceLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(g), Message: "tip me", RandomID: 1})
	if err != nil {
		t.Fatal(err)
	}
	id := sentMsg(t, res)
	waitChannelText(t, bobPushes, "tip me")

	send := func(count int, random int64) {
		t.Helper()
		if _, err := bobLive.MessagesSendPaidReaction(ctx, &tg.MessagesSendPaidReactionRequest{Peer: peerCh(g), MsgID: id, Count: count, RandomID: random}); err != nil {
			t.Fatal(err)
		}
	}
	send(1, 11)
	send(7, 12)
	send(7, 12) // the same request again
	if a, b := balance(alice.id), balance(bob.id); a != 8 || b != 92 {
		t.Fatalf("balances alice %d bob %d", a, b)
	}
	var seen *tg.UpdateMessageReactions
	for seen == nil || seen.Reactions.Results[0].Count != 8 {
		seen = waitReactions(t, alicePushes, id)
	}
	if _, ok := seen.Reactions.Results[0].Reaction.(*tg.ReactionPaid); !ok {
		t.Fatalf("first result %+v", seen.Reactions.Results[0])
	}
	if len(seen.Reactions.TopReactors) != 1 || seen.Reactions.TopReactors[0].Count != 8 {
		t.Fatalf("top reactors %+v", seen.Reactions.TopReactors)
	}
	// history carries it too
	h, err := aliceLive.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peerCh(g), Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range h.(*tg.MessagesChannelMessages).Messages {
		if msg, ok := m.(*tg.Message); ok && msg.ID == id {
			r, ok := msg.GetReactions()
			found = ok && len(r.Results) == 1 && r.Results[0].Count == 8
		}
	}
	if !found {
		t.Fatal("no star count in history")
	}
	// too many, and you can't tip yourself
	if _, err := bobLive.MessagesSendPaidReaction(ctx, &tg.MessagesSendPaidReactionRequest{Peer: peerCh(g), MsgID: id, Count: 93, RandomID: 13}); err == nil {
		t.Fatal("tipped more than the balance")
	}
	if _, err := aliceLive.MessagesSendPaidReaction(ctx, &tg.MessagesSendPaidReactionRequest{Peer: peerCh(g), MsgID: id, Count: 1, RandomID: 14}); err == nil {
		t.Fatal("tipped own message")
	}
	if a, b := balance(alice.id), balance(bob.id); a != 8 || b != 92 {
		t.Fatalf("balances after refusals alice %d bob %d", a, b)
	}
}

func TestChecklists(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	aliceLive := s.live(t, alice, make(chan tg.UpdatesClass, 128))
	bobPushes := make(chan tg.UpdatesClass, 128)
	bobLive := s.live(t, bob, bobPushes)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	item := func(id int, text string) tg.TodoItem {
		return tg.TodoItem{ID: id, Title: tg.TextWithEntities{Text: text, Entities: []tg.MessageEntityClass{}}}
	}
	todo := func(random int64, othersComplete bool) *tg.MessagesSendMediaRequest {
		return &tg.MessagesSendMediaRequest{Peer: userPeer(bob.id), RandomID: random, Media: &tg.InputMediaTodo{Todo: tg.TodoList{
			OthersCanComplete: othersComplete, Title: tg.TextWithEntities{Text: "shop", Entities: []tg.MessageEntityClass{}},
			List: []tg.TodoItem{item(1, "milk"), item(2, "eggs")}}}}
	}
	if _, err := aliceLive.MessagesSendMedia(ctx, todo(1, true)); err == nil {
		t.Fatal("checklist without Premium")
	}
	if _, err := s.db.Pool.Exec(ctx, "UPDATE users SET premium_until=$2 WHERE id=$1", alice.id, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	aliceLive = s.live(t, alice, make(chan tg.UpdatesClass, 128))
	res, err := aliceLive.MessagesSendMedia(ctx, todo(2, true))
	if err != nil {
		t.Fatal(err)
	}
	mine := sentMsg(t, res)
	theirs := waitNew(t, bobPushes, "").ID

	if _, err := bobLive.MessagesToggleTodoCompleted(ctx, &tg.MessagesToggleTodoCompletedRequest{Peer: userPeer(alice.id), MsgID: theirs, Completed: []int{2}}); err != nil {
		t.Fatal(err)
	}
	h, err := aliceLive.MessagesGetMessages(ctx, []tg.InputMessageClass{&tg.InputMessageID{ID: mine}})
	if err != nil {
		t.Fatal(err)
	}
	var m *tg.MessageMediaToDo
	switch v := h.(type) {
	case *tg.MessagesMessages:
		m, _ = v.Messages[0].(*tg.Message).Media.(*tg.MessageMediaToDo)
	case *tg.MessagesMessagesSlice:
		m, _ = v.Messages[0].(*tg.Message).Media.(*tg.MessageMediaToDo)
	}
	if m == nil || len(m.Completions) != 1 || m.Completions[0].ID != 2 {
		t.Fatalf("completions %+v", m)
	}
	// the other side can't add without others_can_append
	if _, err := bobLive.MessagesAppendTodoList(ctx, &tg.MessagesAppendTodoListRequest{Peer: userPeer(alice.id), MsgID: theirs, List: []tg.TodoItem{item(3, "tea")}}); err == nil {
		t.Fatal("appended without the right")
	}
	if _, err := aliceLive.MessagesAppendTodoList(ctx, &tg.MessagesAppendTodoListRequest{Peer: userPeer(bob.id), MsgID: mine, List: []tg.TodoItem{item(3, "tea")}}); err != nil {
		t.Fatal(err)
	}
}
