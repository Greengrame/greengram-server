package e2e

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
)

func previewSite(t *testing.T) *httptest.Server {
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var pic bytes.Buffer
	png.Encode(&pic, img)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pic.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(pic.Bytes())
		default:
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<html><head><meta property="og:title" content="Page ` + r.URL.Path + `">
				<meta property="og:description" content="About it"><meta property="og:image" content="/pic.png"></head></html>`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func pageOf(m tg.MessageClass) *tg.WebPage {
	msg, ok := m.(*tg.Message)
	if !ok {
		return nil
	}
	wm, ok := msg.Media.(*tg.MessageMediaWebPage)
	if !ok {
		return nil
	}
	w, _ := wm.Webpage.(*tg.WebPage)
	return w
}

func TestLinkPreviews(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	site := previewSite(t)

	bobPushes := make(chan tg.UpdatesClass, 128)
	s.live(t, bob, bobPushes)
	aliceLive := s.live(t, alice, make(chan tg.UpdatesClass, 128))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// typing a link: the app asks for the preview
	pv, err := aliceLive.MessagesGetWebPagePreview(ctx, &tg.MessagesGetWebPagePreviewRequest{Message: "look " + site.URL + "/one"})
	if err != nil {
		t.Fatal(err)
	}
	wm, ok := pv.Media.(*tg.MessageMediaWebPage)
	if !ok {
		t.Fatalf("preview %+v", pv.Media)
	}
	w := wm.Webpage.(*tg.WebPage)
	if w.Title != "Page /one" || w.Description != "About it" || w.Photo == nil {
		t.Fatalf("webpage %+v", w)
	}

	// sent after the preview was fetched: it goes out with it
	if _, err := aliceLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "look " + site.URL + "/one", RandomID: 1}); err != nil {
		t.Fatal(err)
	}
	if p := pageOf(waitNew(t, bobPushes, "look "+site.URL+"/one")); p == nil || p.ID != w.ID {
		t.Fatalf("message without its preview")
	}

	// a link nobody previewed: the message goes first, the preview comes as an edit
	text := "and " + site.URL + "/two"
	if _, err := aliceLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: text, RandomID: 2}); err != nil {
		t.Fatal(err)
	}
	if p := pageOf(waitNew(t, bobPushes, text)); p != nil {
		t.Fatalf("preview before the fetch %+v", p)
	}
	edit := pushed(t, bobPushes, "preview edit", func(u tg.UpdateClass) bool {
		e, ok := u.(*tg.UpdateEditMessage)
		return ok && pageOf(e.Message) != nil
	}).(*tg.UpdateEditMessage)
	if p := pageOf(edit.Message); p.Title != "Page /two" || edit.Message.(*tg.Message).EditDate != 0 {
		t.Fatalf("preview edit %+v", edit.Message)
	}

	// no_webpage keeps it off
	text = "quiet " + site.URL + "/one"
	if _, err := aliceLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: text, RandomID: 3, NoWebpage: true}); err != nil {
		t.Fatal(err)
	}
	if p := pageOf(waitNew(t, bobPushes, text)); p != nil {
		t.Fatalf("no_webpage got %+v", p)
	}

	// the same in a group
	cr, err := aliceLive.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Team", Users: []tg.InputUserClass{&tg.InputUser{UserID: bob.id}}})
	if err != nil {
		t.Fatal(err)
	}
	group := channelOf(t, cr.Updates)
	text = "group " + site.URL + "/three"
	if _, err := aliceLive.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group), Message: text, RandomID: 4}); err != nil {
		t.Fatal(err)
	}
	pushed(t, bobPushes, "group preview edit", func(u tg.UpdateClass) bool {
		e, ok := u.(*tg.UpdateEditChannelMessage)
		return ok && pageOf(e.Message) != nil && pageOf(e.Message).Title == "Page /three"
	})
	wp, err := aliceLive.MessagesGetWebPage(ctx, &tg.MessagesGetWebPageRequest{URL: site.URL + "/three"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := wp.Webpage.(*tg.WebPage); !ok {
		t.Fatalf("getWebPage %+v", wp.Webpage)
	}
}
