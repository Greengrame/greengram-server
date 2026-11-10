package media

import (
	"context"
	"net/url"
	"reflect"
	"strings"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

// Articles are Telegram's rich messages: a tree of page blocks. Clients render
// what they get, so what one person sends has to be safe for everyone else's
// app. The limits are the app config defaults Web A checks against
// (rich_message_*); depth has slack because the two count it a little apart.
const (
	maxRichBytes = 512 << 10 // of the encoded tree
	maxRichText  = 32768
	maxRichBlock = 500
	maxRichDepth = 32
	maxRichMedia = 50
	maxRichCols  = 20
)

// Blocks that show other sites or stand in for whole pages. Web A's composer
// never makes them; an embed in a chat message is a stranger's HTML.
var richRefused = map[string]bool{
	"PageBlockEmbed": true, "PageBlockEmbedPost": true, "PageBlockChannel": true, "PageBlockCover": true,
	"PageBlockRelatedArticles": true, "PageBlockAuthorDate": true, "PageBlockUnsupported": true,
}

var richSchemes = map[string]bool{"": true, "http": true, "https": true, "tg": true, "ton": true, "mailto": true, "tel": true}

var (
	richBlockType = reflect.TypeFor[tg.PageBlockClass]()
	richTextType  = reflect.TypeFor[tg.RichTextClass]()
)

type richWalk struct {
	text    strings.Builder
	chars   int
	blocks  int
	depth   int
	columns int
	bad     bool
	mention []int64
}

func (w *richWalk) visit(v reflect.Value, depth int) {
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			w.visit(v.Elem(), depth)
		}
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return
		}
		for i := range v.Len() {
			w.visit(v.Index(i), depth)
		}
	case reflect.Struct:
		w.visitStruct(v, depth)
	}
}

func (w *richWalk) visitStruct(v reflect.Value, depth int) {
	t := v.Type()
	ptr := reflect.PointerTo(t)
	isBlock := ptr.Implements(richBlockType)
	if isBlock || ptr.Implements(richTextType) {
		depth++
		w.depth = max(w.depth, depth)
	}
	if isBlock {
		w.blocks++
		if richRefused[t.Name()] {
			w.bad = true
		}
	}
	switch t.Name() {
	case "TextPlain":
		s := v.FieldByName("Text").String()
		w.chars += len(s)
		w.text.WriteString(s)
	case "TextMentionName":
		w.mention = append(w.mention, v.FieldByName("UserID").Int())
	case "PageTableRow":
		w.columns = max(w.columns, v.FieldByName("Cells").Len())
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Name == "URL" && f.Type.Kind() == reflect.String {
			u, err := url.Parse(v.Field(i).String())
			if err != nil || !richSchemes[strings.ToLower(u.Scheme)] {
				w.bad = true
			}
			continue
		}
		w.visit(v.Field(i), depth)
	}
	// words of two blocks shouldn't run together in search and previews
	if isBlock && w.text.Len() > 0 && !strings.HasSuffix(w.text.String(), "\n") {
		w.text.WriteByte('\n')
	}
}

func walkRich(blocks []tg.PageBlockClass) *richWalk {
	w := &richWalk{}
	for _, b := range blocks {
		w.visit(reflect.ValueOf(b), 0)
	}
	return w
}

// RichFromInput checks an article and resolves its files. It returns the
// encoded richMessage and the words in it. Web A sends blocks; the HTML and
// Markdown forms are for bots, and nothing here parses those yet.
func (s *Service) RichFromInput(ctx context.Context, in tg.InputRichMessageClass) ([]byte, string, error) {
	v, ok := in.(*tg.InputRichMessage)
	if !ok {
		return nil, "", tgerr.New(400, "RICH_MESSAGE_FORMAT_UNSUPPORTED")
	}
	if len(v.Blocks) == 0 {
		return nil, "", tgerr.New(400, "MESSAGE_EMPTY")
	}
	if len(v.Photos)+len(v.Documents) > maxRichMedia {
		return nil, "", tgerr.New(400, "RICH_MESSAGE_TOO_MUCH_MEDIA")
	}
	w := walkRich(v.Blocks)
	switch {
	case w.bad:
		return nil, "", tgerr.New(400, "RICH_MESSAGE_INVALID")
	case w.chars > maxRichText:
		return nil, "", tgerr.New(400, "MESSAGE_TOO_LONG")
	case w.blocks > maxRichBlock, w.depth > maxRichDepth, w.columns > maxRichCols:
		return nil, "", tgerr.New(400, "RICH_MESSAGE_TOO_COMPLEX")
	}
	out := tg.RichMessage{Blocks: v.Blocks, Photos: []tg.PhotoClass{}, Documents: []tg.DocumentClass{}}
	out.SetRtl(v.Rtl)
	for _, in := range v.Photos {
		p, err := s.InputPhoto(ctx, in)
		if err != nil {
			return nil, "", err
		}
		out.Photos = append(out.Photos, s.PhotoTL(p))
	}
	for _, in := range v.Documents {
		d, err := s.InputDocument(ctx, in)
		if err != nil {
			return nil, "", err
		}
		out.Documents = append(out.Documents, s.DocumentTL(d))
	}
	var b bin.Buffer
	if err := out.Encode(&b); err != nil {
		return nil, "", err
	}
	if len(b.Buf) > maxRichBytes {
		return nil, "", tgerr.New(400, "RICH_MESSAGE_TOO_COMPLEX")
	}
	return b.Buf, strings.TrimSpace(w.text.String()), nil
}

// DecodeRich is the stored article as a fresh object. It's never shared: td's
// Encode writes flags into the tree as it goes, and one message goes out on
// several goroutines.
func DecodeRich(raw []byte) (tg.RichMessage, bool) {
	var r tg.RichMessage
	if len(raw) == 0 || r.Decode(&bin.Buffer{Buf: raw}) != nil {
		return tg.RichMessage{}, false
	}
	return r, true
}

// RichHasMedia says whether an article shows files, for chats that restrict them.
func RichHasMedia(raw []byte) bool {
	r, ok := DecodeRich(raw)
	return ok && len(r.Photos)+len(r.Documents) > 0
}

// RichUsers are the client ids of people a stored article mentions.
func RichUsers(raw []byte) []int64 {
	r, ok := DecodeRich(raw)
	if !ok {
		return nil
	}
	return RichMentions(r)
}

// RichMentions are the client ids of people an article mentions.
func RichMentions(r tg.RichMessage) []int64 {
	return walkRich(r.Blocks).mention
}
