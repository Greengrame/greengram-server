package media

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"

	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
)

// GiftArt names a catalog gift's artwork: its own sticker if it has one,
// else whatever animated sticker the imported packs have for its emoji.
type GiftArt struct {
	Gift    int64
	Sticker int64
	Emoji   string
}

// GiftStickers returns a document for every gift, keyed by gift id. An
// emoji no pack has gets the plain gift box.
func (s *Service) GiftStickers(ctx context.Context, gifts []GiftArt) (map[int64]Document, error) {
	var ids []int64
	var emoji []string
	for _, g := range gifts {
		if g.Sticker != 0 {
			ids = append(ids, g.Sticker)
		} else {
			emoji = append(emoji, g.Emoji)
		}
	}
	// the biggest set first, so a gift doesn't change pictures when some
	// small themed pack gets imported
	rows, err := s.db.Query(ctx, `SELECT DISTINCT ON (i.emoji) i.emoji, i.document_id FROM sticker_set_items i
		JOIN documents d ON d.id = i.document_id JOIN sticker_sets st ON st.id = i.set_id
		WHERE i.emoji = ANY($1) AND d.mime = 'application/x-tgsticker' AND st.owner_id IS NULL
		ORDER BY i.emoji, (SELECT count(*) FROM sticker_set_items n WHERE n.set_id = i.set_id) DESC, i.set_id, i.position`, emoji)
	if err != nil {
		return nil, err
	}
	picked := map[string]int64{}
	var e string
	var id int64
	if _, err := pgx.ForEachRow(rows, []any{&e, &id}, func() error {
		picked[e] = id
		ids = append(ids, id)
		return nil
	}); err != nil {
		return nil, err
	}
	docs, err := s.Documents(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]Document, len(docs))
	for _, d := range docs {
		byID[d.ID] = d
	}
	out := make(map[int64]Document, len(gifts))
	for _, g := range gifts {
		ref := g.Sticker
		if ref == 0 {
			ref = picked[g.Emoji]
		}
		d, ok := byID[ref]
		if !ok {
			if d, err = s.giftBox(ctx); err != nil {
				return nil, err
			}
		}
		out[g.Gift] = asGiftSticker(d, g.Emoji)
	}
	return out, nil
}

// premiumGiftEmoji is what the Premium and Stars gift pictures are drawn with.
// Clients ask inputStickerSetPremiumGifts for a sticker per keycap, 1️⃣ to 5️⃣
// going up with the size of the gift, the same five for plans and for Stars.
var premiumGiftEmoji = []struct{ keycap, emoji string }{
	{"1\ufe0f\u20e3", "🎁"}, {"2\ufe0f\u20e3", "⭐️"}, {"3\ufe0f\u20e3", "🌟"}, {"4\ufe0f\u20e3", "💎"}, {"5\ufe0f\u20e3", "👑"},
}

// PremiumGiftSet answers inputStickerSetPremiumGifts from the imported packs.
// Telegram's own gift stickers leave room around the box, and Web A lays
// the giveaway post out for that (the sticker rides up over the bubble's
// top edge); a pack emoji is drawn edge to edge and covers the message
// above. So these are the pack's art, smaller and low in the frame.
func (s *Service) PremiumGiftSet(ctx context.Context) (Set, []Item, error) {
	set, items, err := s.giftSet(ctx, "premium-gifts", "PremiumGifts", "Premium Gifts", "inputStickerSetPremiumGifts", premiumGiftEmoji)
	if err != nil {
		return set, items, err
	}
	sum := crc32.NewIEEE()
	for i := range items {
		d, err := s.paddedGift(ctx, items[i].Doc)
		if err != nil {
			return set, nil, err
		}
		items[i].Doc = d
		binary.Write(sum, binary.LittleEndian, d.ID)
	}
	set.Hash = int(int32(sum.Sum32()))
	return set, items, nil
}

// Where the art goes in a premium gift sticker, as parts of the frame: its
// size, and the height of its middle.
const (
	giftArtScale  = 0.68
	giftArtMiddle = 0.6
)

// paddedGift is d redrawn smaller (see PremiumGiftSet), made once and kept
// under an id derived from d's.
func (s *Service) paddedGift(ctx context.Context, d Document) (Document, error) {
	id := seedID(fmt.Sprintf("padded-gift:%d", d.ID))
	if have, err := s.Documents(ctx, []int64{id}); err != nil {
		return d, err
	} else if len(have) == 1 {
		have[0].Attrs = d.Attrs
		return have[0], nil
	}
	raw, err := s.blobs.Read(ctx, d.Key, 0, int(d.Size))
	if err != nil {
		return d, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return d, err
	}
	anim, err := io.ReadAll(io.LimitReader(zr, 16<<20))
	if err != nil {
		return d, err
	}
	padded, err := padLottie(anim, giftArtScale, giftArtMiddle)
	if err != nil {
		// not a lottie we can read: the art as it is beats no art
		return d, nil
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(padded)
	if err := zw.Close(); err != nil {
		return d, err
	}
	key, size, err := s.blobs.Put(ctx, &buf)
	if err != nil {
		return d, err
	}
	out := Document{ID: id, AccessHash: seedID(fmt.Sprintf("doc-hash:padded-gift:%d", d.ID)), Date: s.unix(), Mime: "application/x-tgsticker",
		Kind: KindSticker, Key: key, Size: size, Attrs: d.Attrs}
	return out, s.insertDocument(ctx, s.db, out)
}

// padLottie puts an animation's layers in a precomp drawn at scale, its
// middle at middle of the height.
func padLottie(raw []byte, scale, middle float64) ([]byte, error) {
	var anim map[string]json.RawMessage
	if err := json.Unmarshal(raw, &anim); err != nil {
		return nil, err
	}
	var w, h, ip, op float64
	for k, v := range map[string]*float64{"w": &w, "h": &h, "ip": &ip, "op": &op} {
		if err := json.Unmarshal(anim[k], v); err != nil {
			return nil, fmt.Errorf("lottie %s: %w", k, err)
		}
	}
	if w <= 0 || h <= 0 || len(anim["layers"]) == 0 {
		return nil, errors.New("lottie: no frame")
	}
	var assets []json.RawMessage
	if a, ok := anim["assets"]; ok {
		if err := json.Unmarshal(a, &assets); err != nil {
			return nil, err
		}
	}
	const ref = "greengram-padded"
	comp, err := json.Marshal(map[string]any{"id": ref, "layers": anim["layers"]})
	if err != nil {
		return nil, err
	}
	assets = append(assets, comp)
	still := func(v any) map[string]any { return map[string]any{"a": 0, "k": v} }
	layer := map[string]any{"ddd": 0, "ind": 1, "ty": 0, "nm": "gift", "refId": ref, "sr": 1, "ao": 0, "bm": 0,
		"w": w, "h": h, "ip": ip, "op": op, "st": 0,
		"ks": map[string]any{"o": still(100), "r": still(0), "p": still([]float64{w / 2, h * middle, 0}),
			"a": still([]float64{w / 2, h / 2, 0}), "s": still([]float64{scale * 100, scale * 100, 100})}}
	if anim["assets"], err = json.Marshal(assets); err != nil {
		return nil, err
	}
	if anim["layers"], err = json.Marshal([]any{layer}); err != nil {
		return nil, err
	}
	return json.Marshal(anim)
}

// tonGiftEmoji is the picture on a wallet transfer's bubble; clients take the
// first sticker when they don't find the keycap they want for the amount.
var tonGiftEmoji = []struct{ keycap, emoji string }{{"1\ufe0f\u20e3", "💎"}, {"2\ufe0f\u20e3", "💰"}, {"3\ufe0f\u20e3", "👑"}}

// TonGiftSet answers inputStickerSetTonGifts the same way.
func (s *Service) TonGiftSet(ctx context.Context) (Set, []Item, error) {
	return s.giftSet(ctx, "ton-gifts", "TonGifts", "Gram Gifts", "inputStickerSetTonGifts", tonGiftEmoji)
}

func (s *Service) giftSet(ctx context.Context, key, name, title, builtin string, emoji []struct{ keycap, emoji string }) (Set, []Item, error) {
	in := make([]GiftArt, len(emoji))
	for i, e := range emoji {
		in[i] = GiftArt{Gift: int64(i), Emoji: e.emoji}
	}
	docs, err := s.GiftStickers(ctx, in)
	if err != nil {
		return Set{}, nil, err
	}
	items := make([]Item, len(in))
	sum := crc32.NewIEEE()
	for i, e := range emoji {
		items[i] = Item{Doc: asGiftSticker(docs[int64(i)], e.keycap), Emoji: e.keycap}
		binary.Write(sum, binary.LittleEndian, items[i].Doc.ID)
	}
	return Set{ID: seedID("set:" + key), AccessHash: seedID("set-hash:" + key), Name: name, Title: title,
		Kind: SetStickers, Builtin: builtin, Hash: int(int32(sum.Sum32())), Count: len(items)}, items, nil
}

// Pack items are mostly custom emoji, which clients won't draw at gift size
// and try to resolve back to their set. A gift's copy is a loose sticker.
func asGiftSticker(d Document, emoji string) Document {
	attrs := make([]tg.DocumentAttributeClass, 0, len(d.Attrs)+1)
	for _, a := range d.Attrs {
		switch a.(type) {
		case *tg.DocumentAttributeCustomEmoji, *tg.DocumentAttributeSticker:
		default:
			attrs = append(attrs, a)
		}
	}
	d.Attrs = append(attrs, &tg.DocumentAttributeSticker{Alt: emoji, Stickerset: &tg.InputStickerSetEmpty{}})
	return d
}

// giftBox is the fallback for an instance with no packs imported.
func (s *Service) giftBox(ctx context.Context) (Document, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(giftBoxLottie())); err != nil {
		return Document{}, err
	}
	if err := zw.Close(); err != nil {
		return Document{}, err
	}
	key, size, err := s.blobs.Put(ctx, &buf)
	if err != nil {
		return Document{}, err
	}
	d := Document{ID: seedID("gift-box"), AccessHash: seedID("doc-hash:gift-box"), Date: s.unix(), Mime: "application/x-tgsticker", Kind: KindSticker, Key: key, Size: size,
		Attrs: []tg.DocumentAttributeClass{&tg.DocumentAttributeImageSize{W: 512, H: 512}, &tg.DocumentAttributeFilename{FileName: "AnimatedSticker.tgs"}}}
	return d, s.insertDocument(ctx, s.db, d)
}

func giftBoxLottie() string {
	rect := func(x, y, w, h int, r, g, b float64) string {
		return fmt.Sprintf(`{"ty":"gr","it":[{"ty":"rc","d":1,"p":{"a":0,"k":[%d,%d]},"s":{"a":0,"k":[%d,%d]},"r":{"a":0,"k":16}},`+
			`{"ty":"fl","o":{"a":0,"k":100},"r":1,"c":{"a":0,"k":[%g,%g,%g,1]}},`+
			`{"ty":"tr","p":{"a":0,"k":[0,0]},"a":{"a":0,"k":[0,0]},"s":{"a":0,"k":[100,100]},"r":{"a":0,"k":0},"o":{"a":0,"k":100}}]}`, x, y, w, h, r, g, b)
	}
	// lottie paints the last shape first: ribbon, lid, box
	return `{"tgs":1,"v":"5.5.2","fr":60,"ip":0,"op":60,"w":512,"h":512,"nm":"gift","ddd":0,"assets":[],"layers":[{"ddd":0,"ind":1,"ty":4,"nm":"box","sr":1,` +
		`"ks":{"o":{"a":0,"k":100},"r":{"a":0,"k":0},"p":{"a":0,"k":[0,0,0]},"a":{"a":0,"k":[0,0,0]},"s":{"a":0,"k":[100,100,100]}},"ao":0,"shapes":[` +
		rect(256, 290, 56, 300, 0.98, 0.84, 0.3) + `,` +
		rect(256, 180, 380, 90, 0.27, 0.72, 0.5) + `,` +
		rect(256, 310, 320, 200, 0.2, 0.62, 0.42) +
		`],"ip":0,"op":60,"st":0,"bm":0}]}`
}
