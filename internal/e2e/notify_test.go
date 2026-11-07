package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
)

func TestNotifySettings(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	phone := make(chan tg.UpdatesClass, 128)
	aliceLive := s.live(t, alice, phone)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if _, err := aliceLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "hi", RandomID: 1}); err != nil {
		t.Fatal(err)
	}
	cr, err := aliceLive.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Team", Users: []tg.InputUserClass{&tg.InputUser{UserID: bob.id}}})
	if err != nil {
		t.Fatal(err)
	}
	group := channelOf(t, cr.Updates)

	// scope defaults before anyone touched them
	def, err := aliceLive.AccountGetNotifySettings(ctx, &tg.InputNotifyUsers{})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := def.GetShowPreviews(); !ok || !v {
		t.Fatalf("users default %+v", def)
	}

	const until = 2147483647
	var mute tg.InputPeerNotifySettings
	mute.SetMuteUntil(until)
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.AccountUpdateNotifySettings(ctx, &tg.AccountUpdateNotifySettingsRequest{Peer: &tg.InputNotifyPeer{Peer: userPeer(bob.id)}, Settings: mute}); err != nil {
			return err
		}
		_, err := c.AccountUpdateNotifySettings(ctx, &tg.AccountUpdateNotifySettingsRequest{Peer: &tg.InputNotifyPeer{Peer: peerCh(group)}, Settings: mute})
		return err
	})
	pushed(t, phone, "mute push", func(u tg.UpdateClass) bool {
		n, ok := u.(*tg.UpdateNotifySettings)
		return ok && n.NotifySettings.MuteUntil == until
	})

	got, err := aliceLive.AccountGetNotifySettings(ctx, &tg.InputNotifyPeer{Peer: userPeer(bob.id)})
	if err != nil || got.MuteUntil != until {
		t.Fatalf("getNotifySettings %v %+v", err, got)
	}
	pd, err := aliceLive.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: userPeer(bob.id)}, &tg.InputDialogPeer{Peer: peerCh(group)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range pd.Dialogs {
		if d.(*tg.Dialog).NotifySettings.MuteUntil != until {
			t.Fatalf("dialog not muted %+v", d)
		}
	}
	full, err := aliceLive.UsersGetFullUser(ctx, &tg.InputUser{UserID: bob.id})
	if err != nil || full.FullUser.NotifySettings.MuteUntil != until {
		t.Fatalf("full user notify %v %+v", err, full.FullUser.NotifySettings)
	}

	ex, err := aliceLive.AccountGetNotifyExceptions(ctx, &tg.AccountGetNotifyExceptionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(ex.(*tg.Updates).Updates); n != 2 {
		t.Fatalf("exceptions %d", n)
	}
	ex, err = aliceLive.AccountGetNotifyExceptions(ctx, &tg.AccountGetNotifyExceptionsRequest{Peer: &tg.InputNotifyChats{}})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(ex.(*tg.Updates).Updates); n != 1 {
		t.Fatalf("group exceptions %d", n)
	}

	if _, err := aliceLive.AccountResetNotifySettings(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = aliceLive.AccountGetNotifySettings(ctx, &tg.InputNotifyPeer{Peer: userPeer(bob.id)})
	if err != nil || got.MuteUntil != 0 {
		t.Fatalf("after reset %v %+v", err, got)
	}
}
