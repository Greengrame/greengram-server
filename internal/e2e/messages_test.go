package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/session"
	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/telegram/dcs"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/transport"
)

// person is a signed-in app: an auth key bound to an account.
type person struct {
	id      int64
	storage *session.StorageMemory
}

func (s *stack) signUp(t *testing.T, username string) person {
	t.Helper()
	b := s.browser(t)
	u := b.must("/api/register", map[string]string{"username": username, "name": username + " Test", "password": "correct horse " + username})
	p := person{id: int64(u["id"].(float64)), storage: &session.StorageMemory{}}
	token := b.session()
	run(t, s.client(p.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.AuthImportAuthorization(ctx, &tg.AuthImportAuthorizationRequest{ID: p.id, Bytes: []byte(token)})
		return err
	})
	return p
}

// live runs a client that keeps receiving pushes until the test ends.
func (s *stack) live(t *testing.T, p person, got chan<- tg.UpdatesClass) *tg.Client {
	t.Helper()
	c := telegram.NewClient(1, "test", telegram.Options{
		PublicKeys:     []telegram.PublicKey{{RSA: &s.key.PublicKey}},
		DC:             dc,
		DCList:         dcs.List{Options: []tg.DCOption{{ID: dc, IPAddress: "127.0.0.1", Port: s.tcp}}},
		Resolver:       dcs.Plain(dcs.PlainOptions{Protocol: transport.Intermediate}),
		SessionStorage: p.storage,
		UpdateHandler: telegram.UpdateHandlerFunc(func(_ context.Context, u tg.UpdatesClass) error {
			got <- u
			return nil
		}),
	})
	ready := make(chan *tg.Client)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx, func(ctx context.Context) error {
			api := c.API()
			// any signed-in call makes the session join the update stream
			if _, err := api.UpdatesGetState(ctx); err != nil {
				t.Errorf("live getState: %v", err)
			}
			ready <- api
			<-ctx.Done()
			return nil
		})
	}()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case api := <-ready:
		return api
	case <-time.After(60 * time.Second):
		t.Fatal("live client never connected")
		return nil
	}
}

func waitNew(t *testing.T, got <-chan tg.UpdatesClass, text string) *tg.Message {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case u := <-got:
			if ups, ok := u.(*tg.Updates); ok {
				for _, x := range ups.Updates {
					if nm, ok := x.(*tg.UpdateNewMessage); ok {
						if m, ok := nm.Message.(*tg.Message); ok && m.Message == text {
							return m
						}
					}
				}
			}
		case <-deadline:
			t.Fatalf("no push with %q", text)
			return nil
		}
	}
}

func userPeer(id int64) *tg.InputPeerUser { return &tg.InputPeerUser{UserID: id} }

func history(ctx context.Context, c *tg.Client, peer int64) ([]*tg.Message, error) {
	h, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: userPeer(peer), Limit: 50})
	if err != nil {
		return nil, err
	}
	var out []*tg.Message
	for _, m := range h.(*tg.MessagesMessagesSlice).Messages {
		out = append(out, m.(*tg.Message))
	}
	return out, nil
}

func TestPrivateMessaging(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	bobPushes := make(chan tg.UpdatesClass, 64)
	bobLive := s.live(t, bob, bobPushes)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	start, err := bobLive.UpdatesGetState(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var hiInAlice int
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		found, err := c.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: "bob"})
		if err != nil {
			return err
		}
		if found.Peer.(*tg.PeerUser).UserID != bob.id {
			return fmt.Errorf("resolved %v", found.Peer)
		}
		res, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "hi bob", RandomID: 1})
		if err != nil {
			return err
		}
		for _, u := range res.(*tg.Updates).Updates {
			if id, ok := u.(*tg.UpdateMessageID); ok && id.RandomID == 1 {
				hiInAlice = id.ID
			}
		}
		if hiInAlice == 0 {
			return fmt.Errorf("no UpdateMessageID in %+v", res)
		}
		// the same random_id again is refused, not sent twice
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "hi bob", RandomID: 1}); err == nil {
			return fmt.Errorf("duplicate random_id accepted")
		}
		_, err = c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: &tg.InputPeerSelf{}, Message: "note to self", RandomID: 2})
		return err
	})

	pushed := waitNew(t, bobPushes, "hi bob")
	if pushed.Out || pushed.PeerID.(*tg.PeerUser).UserID != alice.id {
		t.Fatalf("bob's copy: %+v", pushed)
	}

	// Bob: dialog list, history, reply, read
	dialogs, err := bobLive.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	dl := dialogs.(*tg.MessagesDialogs)
	if len(dl.Dialogs) != 1 || dl.Dialogs[0].(*tg.Dialog).UnreadCount != 1 || len(dl.Users) < 2 {
		t.Fatalf("bob's dialogs %+v", dl)
	}
	if _, err := bobLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
		Peer: userPeer(alice.id), Message: "hey alice", RandomID: 3, ReplyTo: &tg.InputReplyToMessage{ReplyToMsgID: pushed.ID},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := bobLive.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{Peer: userPeer(alice.id), MaxID: pushed.ID}); err != nil {
		t.Fatal(err)
	}
	found, err := bobLive.MessagesSearch(ctx, &tg.MessagesSearchRequest{Peer: userPeer(alice.id), Q: "BOB", Filter: &tg.InputMessagesFilterEmpty{}, Limit: 10})
	if err != nil || len(found.(*tg.MessagesMessagesSlice).Messages) != 1 {
		t.Fatalf("search: %v %+v", err, found)
	}

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		msgs, err := history(ctx, c, bob.id)
		if err != nil {
			return err
		}
		if len(msgs) != 2 || msgs[0].Message != "hey alice" || msgs[0].ReplyTo.(*tg.MessageReplyHeader).ReplyToMsgID != hiInAlice {
			return fmt.Errorf("alice's history %+v", msgs)
		}
		pd, err := c.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: userPeer(bob.id)}})
		if err != nil {
			return err
		}
		if d := pd.Dialogs[0].(*tg.Dialog); d.ReadOutboxMaxID != hiInAlice {
			return fmt.Errorf("bob read it, alice's read_outbox_max = %d", d.ReadOutboxMaxID)
		}
		saved, err := history(ctx, c, alice.id)
		if err != nil || len(saved) != 1 || saved[0].Message != "note to self" {
			return fmt.Errorf("saved messages %v %+v", err, saved)
		}
		if _, err := c.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{Peer: userPeer(bob.id), ID: hiInAlice, Message: "hi bob (edited)"}); err != nil {
			return err
		}
		return nil
	})

	msgs, err := history(ctx, bobLive, alice.id)
	if err != nil || len(msgs) != 2 || msgs[1].Message != "hi bob (edited)" || msgs[1].EditDate == 0 {
		t.Fatalf("bob after edit: %v %+v", err, msgs)
	}

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{ID: []int{hiInAlice}, Revoke: true})
		return err
	})
	msgs, err = history(ctx, bobLive, alice.id)
	if err != nil || len(msgs) != 1 || msgs[0].Message != "hey alice" {
		t.Fatalf("bob after revoke: %v %+v", err, msgs)
	}

	// a client that was away replays exactly what happened
	diff, err := bobLive.UpdatesGetDifference(ctx, &tg.UpdatesGetDifferenceRequest{Pts: start.Pts, Date: start.Date})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := diff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference %T", diff)
	}
	// new: hi bob, hey alice; other: read inbox, edit, delete
	if len(d.NewMessages) != 2 || len(d.OtherUpdates) != 3 {
		t.Fatalf("difference new=%d other=%d", len(d.NewMessages), len(d.OtherUpdates))
	}
	if again, err := bobLive.UpdatesGetDifference(ctx, &tg.UpdatesGetDifferenceRequest{Pts: d.State.Pts, Date: d.State.Date}); err != nil {
		t.Fatal(err)
	} else if _, ok := again.(*tg.UpdatesDifferenceEmpty); !ok {
		t.Fatalf("caught up but got %T", again)
	}
}

// An app that imports another account on the same auth key stops getting
// the first account's pushes, even while that account's sign-in lives on
// (here: the browser it came from).
func TestReimportMovesPushes(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	carol := s.signUp(t, "carol")
	b := s.browser(t)
	u := b.must("/api/register", map[string]string{"username": "bob", "name": "Bob", "password": "correct horse bob"})
	bobID, bobToken := int64(u["id"].(float64)), b.session()

	pushes := make(chan tg.UpdatesClass, 64)
	dev := s.live(t, alice, pushes)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := dev.AuthImportAuthorization(ctx, &tg.AuthImportAuthorizationRequest{ID: bobID, Bytes: []byte(bobToken)}); err != nil {
		t.Fatal(err)
	}
	if _, err := dev.UpdatesGetState(ctx); err != nil {
		t.Fatal(err)
	}
	run(t, s.client(carol.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(alice.id), Message: "for alice", RandomID: 1}); err != nil {
			return err
		}
		_, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bobID), Message: "for bob", RandomID: 2})
		return err
	})
	deadline := time.After(20 * time.Second)
	for {
		select {
		case up := <-pushes:
			ups, ok := up.(*tg.Updates)
			if !ok {
				continue
			}
			for _, x := range ups.Updates {
				nm, ok := x.(*tg.UpdateNewMessage)
				if !ok {
					continue
				}
				switch nm.Message.(*tg.Message).Message {
				case "for alice":
					t.Fatal("bob's device got alice's message")
				case "for bob":
					return
				}
			}
		case <-deadline:
			t.Fatal("no push for bob")
		}
	}
}

// Web A keeps a chat's message list only after seeing its dialog, so an
// empty chat needs one or it never leaves the spinner.
func TestPeerDialogsForNewChats(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		pd, err := c.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: userPeer(bob.id)}, &tg.InputDialogPeer{Peer: userPeer(alice.id)}})
		if err != nil {
			return err
		}
		if len(pd.Dialogs) != 2 || pd.Dialogs[0].(*tg.Dialog).TopMessage != 0 || len(pd.Messages) != 0 {
			return fmt.Errorf("new chats %+v", pd)
		}
		dialogs, err := c.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 10})
		if err != nil {
			return err
		}
		if len(dialogs.(*tg.MessagesDialogs).Dialogs) != 0 {
			return fmt.Errorf("looking at a chat put it in the list: %+v", dialogs)
		}
		return nil
	})
}
