package messages

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/ids"
)

func TestOriginTL(t *testing.T) {
	if _, ok := (Origin{Date: 5}).TL(); ok {
		t.Fatal("a date alone is no forward")
	}
	h, ok := Origin{Chan: 1001, Post: 7, Date: 5}.TL()
	if from, isChan := h.FromID.(*tg.PeerChannel); !ok || !isChan || from.ChannelID != 1001 || h.ChannelPost != 7 || h.Date != 5 {
		t.Fatalf("post %+v", h)
	}
	h, _ = Origin{Chan: 1001, Date: 5}.TL()
	if _, set := h.GetChannelPost(); set {
		t.Fatalf("group origin has a post %+v", h)
	}
	h, _ = Origin{User: 777000, Date: 5}.TL()
	if from, ok := h.FromID.(*tg.PeerUser); !ok || from.UserID != ids.Client(777000) {
		t.Fatalf("user %+v", h)
	}
	h, _ = Origin{Name: "Someone", Date: 5}.TL()
	if h.FromID != nil || h.FromName != "Someone" {
		t.Fatalf("hidden %+v", h)
	}
	m := Msg{From: 1, Peer: 2, Fwd: Origin{Name: "Someone", Date: 5}}
	if fwd, ok := m.TL().(*tg.Message).GetFwdFrom(); !ok || fwd.FromName != "Someone" {
		t.Fatalf("message header %+v", fwd)
	}
}
