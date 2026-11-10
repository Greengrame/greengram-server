package media

import (
	"crypto/sha256"
	"encoding/binary"
	"net/url"
	"strings"

	"github.com/iamxvbaba/td/tg"
)

func WidgetRef(key, address string) Ref {
	var sum = sha256.Sum256([]byte(key))
	var page = &tg.WebPage{ID: int64(binary.LittleEndian.Uint64(sum[:8]) >> 1), URL: address, DisplayURL: "Widget"}
	page.SetType("greengram_widget")
	page.SetTitle("Widget")
	return Ref{Kind: KindWebPage, ID: page.ID, TL: &tg.MessageMediaWebPage{Webpage: page}}
}

func WidgetKey(ref Ref) string {
	var media *tg.MessageMediaWebPage
	var page *tg.WebPage
	var ok bool
	var address *url.URL
	var err error
	media, ok = ref.TL.(*tg.MessageMediaWebPage)
	if !ok {
		return ""
	}
	page, ok = media.Webpage.(*tg.WebPage)
	if !ok || page.Type != "greengram_widget" {
		return ""
	}
	address, err = url.Parse(page.URL)
	if err != nil || !strings.HasPrefix(address.Path, "/__greengram/widgets/") {
		return ""
	}
	return strings.TrimPrefix(address.Path, "/__greengram/widgets/")
}
