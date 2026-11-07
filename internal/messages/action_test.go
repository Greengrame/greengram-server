package messages

import (
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/layer"
)

// A message that can't be written in a client's layer would take its whole
// getHistory down, so every layer has to be able to carry a gift.
func TestGiftActionsOnEveryLayer(t *testing.T) {
	sticker := &tg.Document{ID: 1, AccessHash: 2, FileReference: []byte{1}, MimeType: "application/x-tgsticker", DCID: 2,
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeSticker{Alt: "🎁", Stickerset: &tg.InputStickerSetEmpty{}}}}
	gift := &tg.StarGift{ID: 7, Sticker: sticker, Stars: 25, ConvertStars: 21}
	gift.SetTitle("Present")
	note := tg.TextWithEntities{Text: "for you", Entities: []tg.MessageEntityClass{}}
	star := &tg.MessageActionStarGift{Gift: gift, Saved: true}
	star.SetMessage(note)
	star.SetConvertStars(21)
	premium := &tg.MessageActionGiftPremium{Currency: "XTR", Amount: 250, Days: 30}
	premium.SetMessage(note)
	stars := &tg.MessageActionGiftStars{Currency: "XTR", Amount: 500, Stars: 500}
	stars.SetTransactionID("tx")

	for n := layer.Oldest; n <= layer.Newest; n++ {
		p, ok := layer.Resolve(n)
		if !ok {
			t.Fatalf("layer %d doesn't resolve", n)
		}
		for _, act := range []tg.MessageActionClass{star, premium, stars} {
			m := Msg{Owner: 1, ID: 1, Peer: 2, From: 2, Date: 1, Action: act}
			up := &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: m.TL(), Pts: 1, PtsCount: 1}},
				Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: 1}
			var b bin.Buffer
			if err := tlprofile.EncodeObject(p, up, &b); err != nil {
				t.Errorf("layer %d, %T: %v", n, act, err)
			}
		}
	}
}
