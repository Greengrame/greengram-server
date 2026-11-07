package media

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
	"golang.org/x/image/webp"

	"github.com/Greengrame/greengram-server/internal/account"
)

// Packs people make in the Stickers mini app, with greengram-web's
// stickers.js rules: their own uploads or stickers from packs they have,
// 1-120 per pack (emoji packs 1-200, like Telegram), emoji up to 100x100.

func refuse(status int, msg string) error { return &account.Error{Status: status, Message: msg} }

// MaxStickerBytes is the upload limit for one sticker file.
const MaxStickerBytes = 1 << 20

var shortNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{4,63}$`)

// CheckPackName says whether name can be a new pack's short name.
func (s *Service) CheckPackName(ctx context.Context, name string) error {
	if !shortNameRe.MatchString(strings.ToLower(name)) {
		return refuse(400, "Choose an available pack name of 5–64 letters, numbers or underscores.")
	}
	var taken bool
	if err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM sticker_sets WHERE lower(short_name)=lower($1))", name).Scan(&taken); err != nil {
		return err
	}
	if taken {
		return refuse(409, "That pack name is taken.")
	}
	return nil
}

// OwnedPacks are the packs owner made, newest first.
func (s *Service) OwnedPacks(ctx context.Context, owner int64) ([]Set, error) {
	rows, err := s.db.Query(ctx, "SELECT "+setCols+" FROM "+setFrom+" WHERE s.owner_id=$1 ORDER BY s.created_at DESC, s.id DESC", owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Set
	for rows.Next() {
		v, err := scanSet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Pack finds a pack by id or short name for the Stickers app's pages.
func (s *Service) Pack(ctx context.Context, user int64, ref string) (Set, []Item, bool, error) {
	where, arg := "lower(s.short_name)=lower($2)", any(ref)
	if id, ok := digits(ref); ok {
		where, arg = "s.id=$2", id
	}
	set, err := scanSet(s.db.QueryRow(ctx, "SELECT "+setCols+" FROM "+setFrom+" WHERE "+where+" AND s.builtin IS NULL AND ((s.enabled AND NOT s.draft) OR s.owner_id = $1)", user, arg))
	if errors.Is(err, pgx.ErrNoRows) {
		return Set{}, nil, false, nil
	}
	if err != nil {
		return Set{}, nil, false, err
	}
	items, err := s.items(ctx, set.ID)
	return set, items, true, err
}

func digits(v string) (int64, bool) {
	if v == "" || len(v) > 19 {
		return 0, false
	}
	var n int64
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int64(r-'0')
	}
	return n, n > 0
}

// InPack refuses a document that isn't in any sticker pack.
func (s *Service) InPack(ctx context.Context, doc int64) error {
	var ok bool
	if err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM sticker_set_items WHERE document_id=$1)", doc).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return refuse(404, "Sticker not found.")
	}
	return nil
}

// PackInstalls counts who has the pack in their panel.
func (s *Service) PackInstalls(ctx context.Context, set int64) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, "SELECT count(*) FROM user_sticker_sets WHERE set_id=$1 AND installed", set).Scan(&n)
	return n, err
}

// StickerSize reads a sticker document's pixel size from its attributes.
func StickerSize(d Document) (w, h int) {
	for _, a := range d.Attrs {
		switch v := a.(type) {
		case *tg.DocumentAttributeImageSize:
			return v.W, v.H
		case *tg.DocumentAttributeVideo:
			return v.W, v.H
		}
	}
	return 512, 512
}

func packAttrs(mime string, w, h int, emoji bool, alt string, set tg.InputStickerSetClass, textColor bool) []tg.DocumentAttributeClass {
	var kind tg.DocumentAttributeClass = &tg.DocumentAttributeSticker{Alt: alt, Stickerset: set}
	if emoji {
		kind = &tg.DocumentAttributeCustomEmoji{Alt: alt, Stickerset: set, Free: true, TextColor: textColor}
	}
	switch mime {
	case "application/x-tgsticker":
		return []tg.DocumentAttributeClass{&tg.DocumentAttributeImageSize{W: w, H: h}, kind, &tg.DocumentAttributeFilename{FileName: "AnimatedSticker.tgs"}}
	case "video/webm":
		return []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{W: w, H: h}, kind, &tg.DocumentAttributeFilename{FileName: "sticker.webm"}}
	}
	return []tg.DocumentAttributeClass{&tg.DocumentAttributeImageSize{W: w, H: h}, kind, &tg.DocumentAttributeFilename{FileName: "sticker." + stickerExt[mime]}}
}

// stickerInfo works out what an uploaded sticker file is.
func stickerInfo(data []byte, emoji bool) (mime string, w, h int, err error) {
	bad := refuse(400, "Use PNG, WEBP, animated TGS or VP9 WEBM.")
	limit := 512
	if emoji {
		limit = 100
	}
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) && len(data) >= 24 && string(data[12:16]) == "IHDR":
		mime, w, h = "image/png", int(binary.BigEndian.Uint32(data[16:20])), int(binary.BigEndian.Uint32(data[20:24]))
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		cfg, err := webp.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return "", 0, 0, bad
		}
		mime, w, h = "image/webp", cfg.Width, cfg.Height
	case bytes.HasPrefix(data, []byte{0x1f, 0x8b}):
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return "", 0, 0, bad
		}
		raw, err := io.ReadAll(io.LimitReader(zr, 4<<20))
		var lottie struct {
			V string `json:"v"`

			Layers []json.RawMessage `json:"layers"`
		}
		if err != nil || json.Unmarshal(raw, &lottie) != nil || lottie.V == "" || lottie.Layers == nil {
			return "", 0, 0, bad
		}
		var size struct {
			W float64 `json:"w"`
			H float64 `json:"h"`
		}
		json.Unmarshal(raw, &size)
		return "application/x-tgsticker", int(size.W), int(size.H), nil
	case bytes.HasPrefix(data, []byte{0x1a, 0x45, 0xdf, 0xa3}):
		// the WebM header's size fields aren't worth a parser here
		return "video/webm", limit, limit, nil
	default:
		return "", 0, 0, bad
	}
	if w < 1 || h < 1 || w > limit || h > limit {
		return "", 0, 0, refuse(400, "Use up to 512×512 pixels for stickers, or 100×100 for emoji.")
	}
	return mime, w, h, nil
}

// UploadSticker stores one file for a pack that isn't saved yet.
func (s *Service) UploadSticker(ctx context.Context, owner int64, emoji bool, data []byte) (Document, error) {
	if len(data) == 0 || len(data) > MaxStickerBytes {
		return Document{}, refuse(413, "Upload a file between 1 byte and 1 MB.")
	}
	mime, w, h, err := stickerInfo(data, emoji)
	if err != nil {
		return Document{}, err
	}
	key, size, err := s.blobs.Put(ctx, bytes.NewReader(data))
	if err != nil {
		return Document{}, err
	}
	kind := KindSticker
	if emoji {
		kind = KindEmoji
	}
	d := Document{ID: newID(), AccessHash: newID(), Owner: owner, Date: s.unix(), Mime: mime, Kind: kind, Key: key, Size: size,
		Attrs: packAttrs(mime, w, h, emoji, "", &tg.InputStickerSetEmpty{}, false)}
	return d, s.tx(ctx, func(tx pgx.Tx) error { return s.insertDocument(ctx, tx, d) })
}

// StickerFile is a sticker document and its bytes, for the app's previews.
func (s *Service) StickerFile(ctx context.Context, id int64) (Document, []byte, error) {
	docs, err := s.Documents(ctx, []int64{id})
	if err != nil {
		return Document{}, nil, err
	}
	if len(docs) == 0 || docs[0].Kind != KindSticker && docs[0].Kind != KindEmoji || docs[0].Size > 4*MaxStickerBytes {
		return Document{}, nil, refuse(404, "Sticker not found.")
	}
	data, err := s.blobs.Read(ctx, docs[0].Key, 0, int(docs[0].Size))
	return docs[0], data, err
}

// PackSticker is one sticker of a pack being saved.
type PackSticker struct {
	Doc   int64
	Emoji string
}

// PackEdit is a pack to create (ID 0) or replace.
type PackEdit struct {
	ID        int64
	Title     string
	Name      string // new packs only; "" picks one
	Draft     bool   // new packs only
	Emoji     bool   // new packs only
	Stickers  []PackSticker
	Thumb     int64 // 0 keeps the current icon (the first sticker for new packs)
	TextColor *bool
}

func validEmoji(v string) bool {
	if v == "" || len(v) > 32 || !utf8.ValidString(v) {
		return false
	}
	for _, r := range v {
		if r >= 0x1F000 || unicode.Is(unicode.So, r) || r == 0x00A9 || r == 0x00AE || r >= 0x2190 && r <= 0x2BFF {
			return true
		}
	}
	return false
}

// ownPack loads a pack owner may change in the app.
func (s *Service) ownPack(ctx context.Context, q pgx.Tx, owner, id int64) (Set, error) {
	set, err := scanSet(q.QueryRow(ctx, "SELECT "+setCols+" FROM "+setFrom+" WHERE s.id=$2 FOR UPDATE OF s", owner, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return set, refuse(404, "Pack not found.")
	}
	if err != nil {
		return set, err
	}
	if set.Owner != owner || set.Default || set.Official || set.Builtin != "" {
		return set, refuse(403, "Only your own non-default packs can be managed in Stickers.")
	}
	return set, nil
}

// SavePack creates or replaces one of owner's packs. Stickers that sit in
// another pack are copied, so that pack keeps them.
func (s *Service) SavePack(ctx context.Context, owner int64, e PackEdit) (Set, error) {
	e.Title = strings.TrimSpace(e.Title)
	if e.Title == "" && e.ID == 0 || utf8.RuneCountInString(e.Title) > 64 || len(e.Stickers) == 0 || len(e.Stickers) > MaxPackEmoji {
		return Set{}, refuse(400, "Choose a title of up to 64 characters and 1–120 stickers, or up to 200 emoji.")
	}
	seen := map[int64]bool{}
	ids := make([]int64, 0, len(e.Stickers))
	for i, it := range e.Stickers {
		if it.Emoji == "" {
			e.Stickers[i].Emoji = "🫥"
		}
		if seen[it.Doc] || it.Doc == 0 || !validEmoji(e.Stickers[i].Emoji) {
			return Set{}, refuse(400, "Choose distinct stickers and valid emoji.")
		}
		seen[it.Doc] = true
		ids = append(ids, it.Doc)
	}
	if e.ID == 0 {
		if e.Name == "" {
			var b [8]byte
			rand.Read(b[:])
			e.Name = "pack_" + hex.EncodeToString(b[:])
		}
		e.Name = strings.ToLower(e.Name)
		if err := s.CheckPackName(ctx, e.Name); err != nil {
			return Set{}, err
		}
	}
	docs, err := s.Documents(ctx, ids)
	if err != nil {
		return Set{}, err
	}
	byID := make(map[int64]Document, len(docs))
	for _, d := range docs {
		byID[d.ID] = d
	}
	var out Set
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var set Set
		if e.ID == 0 {
			kind := SetStickers
			if e.Emoji {
				kind = SetEmoji
			}
			set = Set{ID: newID(), AccessHash: newID(), Name: e.Name, Kind: kind, Owner: owner, Draft: e.Draft}
			if _, err := tx.Exec(ctx, `INSERT INTO sticker_sets(id, access_hash, short_name, title, kind, owner_id, created_at, draft)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, set.ID, set.AccessHash, set.Name, e.Title, kind, owner, s.unix(), e.Draft); err != nil {
				return err
			}
		} else {
			var err error
			if set, err = s.ownPack(ctx, tx, owner, e.ID); err != nil {
				return err
			}
			if e.Title == "" {
				// the pack page saves order and deletions without the title
				e.Title = set.Title
			}
		}
		emoji := set.Kind == SetEmoji
		if !emoji && len(e.Stickers) > MaxPackStickers {
			return refuse(400, "A sticker pack can have up to 120 stickers.")
		}
		textColor := set.TextColor
		if e.TextColor != nil && emoji {
			textColor = *e.TextColor
		}
		ref := &tg.InputStickerSetID{ID: set.ID, AccessHash: set.AccessHash}
		// where each sticker sits now
		where := map[int64]int64{}
		rows, err := tx.Query(ctx, "SELECT document_id, set_id FROM sticker_set_items WHERE document_id = ANY($1)", ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var d, in int64
			if err := rows.Scan(&d, &in); err != nil {
				rows.Close()
				return err
			}
			if where[d] == 0 || in == set.ID {
				where[d] = in
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		final := make([]int64, len(e.Stickers))
		for i, it := range e.Stickers {
			d, ok := byID[it.Doc]
			if !ok || d.Kind != KindSticker && d.Kind != KindEmoji {
				return refuse(403, "This sticker does not belong to this pack or account.")
			}
			in := where[d.ID]
			if in == 0 && d.Owner != owner {
				return refuse(403, "This sticker does not belong to this pack or account.")
			}
			if in != 0 && in != set.ID {
				var has bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sticker_sets s LEFT JOIN user_sticker_sets u ON u.set_id=s.id AND u.user_id=$1
					WHERE s.id=$2 AND (s.owner_id=$1 OR u.installed OR (u.user_id IS NULL AND s.is_default)))`, owner, in).Scan(&has); err != nil {
					return err
				}
				if !has {
					return refuse(403, "This sticker does not belong to this pack or account.")
				}
			}
			w, h := StickerSize(d)
			if emoji && d.Mime != "application/x-tgsticker" && (w > 100 || h > 100) {
				return refuse(400, "Custom emoji can be up to 100 × 100 pixels.")
			}
			kind := KindSticker
			if emoji {
				kind = KindEmoji
			}
			attrs := packAttrs(d.Mime, w, h, emoji, it.Emoji, ref, textColor)
			if in != 0 && in != set.ID {
				// someone else's pack keeps its sticker; this one gets a copy
				c := Document{ID: newID(), AccessHash: newID(), Owner: owner, Date: s.unix(), Mime: d.Mime, Kind: kind, Key: d.Key, Size: d.Size, Thumb: d.Thumb, Attrs: attrs}
				if err := s.insertDocument(ctx, tx, c); err != nil {
					return err
				}
				if e.Thumb == d.ID {
					e.Thumb = c.ID
				}
				final[i] = c.ID
				continue
			}
			raw, err := encodeAttrs(attrs)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "UPDATE documents SET attributes=$2, kind=$3 WHERE id=$1", d.ID, raw, kind); err != nil {
				return err
			}
			final[i] = d.ID
		}
		thumb := e.Thumb
		if thumb == 0 {
			thumb = set.ThumbDoc
		}
		if !contains(final, thumb) {
			// Set Pack Icon uploads a picture that isn't one of the stickers. A sticker
			// just taken out still has its item row here, so it doesn't pass for one.
			var loose bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM documents d WHERE d.id=$1 AND d.owner_id=$2 AND d.kind = ANY($3)
				AND NOT EXISTS (SELECT 1 FROM sticker_set_items i WHERE i.document_id=d.id))`, thumb, owner, []string{KindSticker, KindEmoji}).Scan(&loose); err != nil {
				return err
			}
			if !loose {
				if e.Thumb != 0 {
					return refuse(403, "Choose an icon from this pack.")
				}
				thumb = final[0]
			}
		}
		sum := crc32.NewIEEE()
		sum.Write([]byte(e.Title))
		for _, id := range final {
			binary.Write(sum, binary.LittleEndian, id)
		}
		if _, err := tx.Exec(ctx, "UPDATE sticker_sets SET title=$2, text_color=$3, thumb_document_id=$4, hash=$5 WHERE id=$1",
			set.ID, e.Title, textColor, thumb, int32(sum.Sum32())); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM sticker_set_items WHERE set_id=$1", set.ID); err != nil {
			return err
		}
		for i, id := range final {
			if _, err := tx.Exec(ctx, "INSERT INTO sticker_set_items(set_id, position, document_id, emoji) VALUES ($1, $2, $3, $4)",
				set.ID, i, id, e.Stickers[i].Emoji); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_sticker_sets(user_id, set_id, installed, date) VALUES ($1, $2, true, $3)
			ON CONFLICT (user_id, set_id) DO NOTHING`, owner, set.ID, s.unix()); err != nil {
			return err
		}
		out = set
		return nil
	})
	if err != nil {
		return Set{}, err
	}
	set, _, _, err := s.Pack(ctx, owner, strconv.FormatInt(out.ID, 10))
	return set, err
}

func contains(list []int64, v int64) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// PublishPack makes a draft public, under the link its maker chose.
func (s *Service) PublishPack(ctx context.Context, owner, id int64, title, name string) (Set, error) {
	title, name = strings.TrimSpace(title), strings.ToLower(strings.TrimSpace(name))
	if title == "" || utf8.RuneCountInString(title) > 64 {
		return Set{}, refuse(400, "Choose a title of up to 64 characters.")
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		set, err := s.ownPack(ctx, tx, owner, id)
		if err != nil {
			return err
		}
		if !set.Draft {
			return refuse(400, "This pack is already published.")
		}
		if name == "" {
			name = strings.ToLower(set.Name)
		}
		if name != strings.ToLower(set.Name) {
			if err := s.CheckPackName(ctx, name); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, "UPDATE sticker_sets SET title=$2, short_name=$3, draft=false, hash=$4 WHERE id=$1", id, title, name, int32(crc32.ChecksumIEEE([]byte(title+"\x00"+name))))
		return err
	})
	if err != nil {
		return Set{}, err
	}
	set, _, _, err := s.Pack(ctx, owner, strconv.FormatInt(id, 10))
	return set, err
}

// DeletePack removes one of owner's packs; sent stickers stay in messages.
func (s *Service) DeletePack(ctx context.Context, owner, id int64) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := s.ownPack(ctx, tx, owner, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "DELETE FROM sticker_sets WHERE id=$1", id)
		return err
	})
}
