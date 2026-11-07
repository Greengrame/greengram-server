package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
)

// The old server's embedded packs (greengram-web resources/sticker-packs):
// active.json names a build, builds/<sha256 of manifest>/manifest.json
// lists the packs, assets/<sha256>.<ext> are the files. Ids come from the
// manifest (Telegram's own document ids where it has them), so importing
// twice changes nothing and custom emoji in old messages keep working.

type bundleActive struct {
	Version int    `json:"version"`
	Build   string `json:"build"`
}

type bundleManifest struct {
	Version int          `json:"version"`
	Packs   []bundlePack `json:"packs"`
}

type bundlePack struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Kind        string          `json:"kind"`
	Official    bool            `json:"official"`
	Sources     []string        `json:"sources"`
	PremiumOnly bool            `json:"premiumOnly"`
	TextColor   bool            `json:"textColor"`
	ThumbIndex  *int            `json:"thumbIndex"`
	Stickers    []bundleSticker `json:"stickers"`
}

type bundleSticker struct {
	File          string  `json:"file"`
	SHA256        string  `json:"sha256"`
	Size          int64   `json:"size"`
	Mime          string  `json:"mime"`
	Width         int     `json:"width"`
	Height        int     `json:"height"`
	Emoji         string  `json:"emoji"`
	SourceID      string  `json:"sourceId"`
	CustomEmojiID *string `json:"customEmojiId"`
}

var (
	packNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)
	stickerExt = map[string]string{"image/webp": "webp", "image/png": "png", "application/x-tgsticker": "tgs", "video/webm": "webm"}
)

const maxStickerBytes = 1 << 20

// removedPacks are still in the old server's bundle but were taken off this
// instance on purpose; importing the bundle again must not bring them back.
var removedPacks = map[string]bool{"emojiuzbek": true}

// Imported is what ImportPacks did.
type Imported struct {
	Packs, Stickers int
}

// ImportPacks loads an old-server pack bundle into the store. Every check
// the old server made is made again: the bundle is files on disk anyone
// could have edited.
func (s *Service) ImportPacks(ctx context.Context, dir string) (Imported, error) {
	var res Imported
	raw, err := os.ReadFile(filepath.Join(dir, "active.json"))
	if err != nil {
		return res, err
	}
	var act bundleActive
	if err := json.Unmarshal(raw, &act); err != nil || act.Version != 1 || !keyRe.MatchString(act.Build) {
		return res, fmt.Errorf("media: %s/active.json: bad build selector", dir)
	}
	root := filepath.Join(dir, "builds", act.Build)
	raw, err = os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return res, err
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != act.Build {
		return res, fmt.Errorf("media: manifest checksum changed")
	}
	var man bundleManifest
	if err := json.Unmarshal(raw, &man); err != nil || man.Version != 1 {
		return res, fmt.Errorf("media: bad manifest")
	}
	for _, p := range man.Packs {
		if removedPacks[strings.ToLower(p.Name)] {
			continue
		}
		if err := checkPack(p); err != nil {
			return res, err
		}
		if err := s.importPack(ctx, root, p); err != nil {
			return res, fmt.Errorf("media: pack %s: %w", p.Name, err)
		}
		res.Packs++
		res.Stickers += len(p.Stickers)
	}
	return res, nil
}

func checkPack(p bundlePack) error {
	if !packNameRe.MatchString(p.Name) || p.Title == "" || len(p.Title) > 256 || (p.Kind != "sticker" && p.Kind != "emoji") ||
		len(p.Stickers) == 0 || len(p.Stickers) > 3000 || p.ThumbIndex != nil && (*p.ThumbIndex < 0 || *p.ThumbIndex >= len(p.Stickers)) {
		return fmt.Errorf("media: pack %q: bad metadata", p.Name)
	}
	for _, it := range p.Stickers {
		ext, ok := stickerExt[it.Mime]
		if !ok || !keyRe.MatchString(it.SHA256) || it.File != "assets/"+it.SHA256+"."+ext ||
			it.Size < 1 || it.Size > maxStickerBytes || it.Width < 1 || it.Height < 1 || it.Width > 512 || it.Height > 512 {
			return fmt.Errorf("media: pack %q: bad sticker %s", p.Name, it.File)
		}
	}
	return nil
}

// seedID is a stable id for things the manifest has no number for.
func seedID(what string) int64 {
	sum := sha256.Sum256([]byte("greengram-seed:" + what))
	return int64(binary.LittleEndian.Uint64(sum[:8]) >> 1)
}

func stickerID(it bundleSticker) int64 {
	for _, v := range []*string{it.CustomEmojiID, &it.SourceID} {
		if v == nil {
			continue
		}
		if n, err := strconv.ParseInt(*v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return seedID("doc:" + it.SHA256)
}

// builtin is the inputStickerSet* type name a "builtin:" source names.
func builtin(sources []string) string {
	for _, src := range sources {
		if name, ok := strings.CutPrefix(src, "builtin:"); ok && name != "" {
			r := []rune(name)
			r[0] = unicode.ToLower(r[0])
			return string(r)
		}
	}
	return ""
}

func stickerAttrs(p bundlePack, it bundleSticker, set *tg.InputStickerSetID) []tg.DocumentAttributeClass {
	var kind tg.DocumentAttributeClass = &tg.DocumentAttributeSticker{Alt: it.Emoji, Stickerset: set}
	if p.Kind == "emoji" {
		kind = &tg.DocumentAttributeCustomEmoji{Alt: it.Emoji, Stickerset: set, Free: !p.PremiumOnly, TextColor: p.TextColor}
	}
	switch it.Mime {
	case "application/x-tgsticker":
		return []tg.DocumentAttributeClass{&tg.DocumentAttributeImageSize{W: it.Width, H: it.Height}, kind, &tg.DocumentAttributeFilename{FileName: "AnimatedSticker.tgs"}}
	case "video/webm":
		return []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{W: it.Width, H: it.Height}, kind, &tg.DocumentAttributeFilename{FileName: "sticker.webm"}}
	}
	return []tg.DocumentAttributeClass{&tg.DocumentAttributeImageSize{W: it.Width, H: it.Height}, kind, &tg.DocumentAttributeFilename{FileName: "sticker." + stickerExt[it.Mime]}}
}

func (s *Service) importPack(ctx context.Context, root string, p bundlePack) error {
	set := &tg.InputStickerSetID{ID: seedID("set:" + strings.ToLower(p.Name)), AccessHash: seedID("set-hash:" + strings.ToLower(p.Name))}
	kind, docKind := SetStickers, KindSticker
	if p.Kind == "emoji" {
		kind, docKind = SetEmoji, KindEmoji
	}
	now := s.unix()
	docs := make([]Document, len(p.Stickers))
	sum := crc32.NewIEEE()
	sum.Write([]byte(p.Title))
	for i, it := range p.Stickers {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(it.File)))
		if err != nil {
			return err
		}
		if h := sha256.Sum256(raw); int64(len(raw)) != it.Size || hex.EncodeToString(h[:]) != it.SHA256 {
			return fmt.Errorf("%s: checksum changed", it.File)
		}
		key, size, err := s.blobs.Put(ctx, bytes.NewReader(raw))
		if err != nil {
			return err
		}
		id := stickerID(it)
		docs[i] = Document{ID: id, AccessHash: seedID(fmt.Sprintf("doc-hash:%d", id)), Date: now, Mime: it.Mime, Kind: docKind,
			Key: key, Size: size, Attrs: stickerAttrs(p, it, set)}
		binary.Write(sum, binary.LittleEndian, id)
	}
	thumb := docs[0].ID
	if p.ThumbIndex != nil {
		thumb = docs[*p.ThumbIndex].ID
	}
	bi := builtin(p.Sources)
	var biArg *string
	if bi != "" {
		biArg = &bi
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		for _, d := range docs {
			if err := s.insertDocument(ctx, tx, d); err != nil {
				return err
			}
		}
		// built-in sets (topic icons...) are fetched by the apps on their
		// own and never sit in anyone's panel
		_, err := tx.Exec(ctx, `INSERT INTO sticker_sets(id, access_hash, short_name, title, kind, official, text_color, is_default, builtin, thumb_document_id, hash, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (id) DO UPDATE SET short_name=excluded.short_name, title=excluded.title, kind=excluded.kind, official=excluded.official,
				text_color=excluded.text_color, is_default=excluded.is_default, builtin=excluded.builtin,
				thumb_document_id=excluded.thumb_document_id, hash=excluded.hash`,
			set.ID, set.AccessHash, p.Name, p.Title, kind, p.Official, p.TextColor, bi == "", biArg, thumb, int32(sum.Sum32()), now)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM sticker_set_items WHERE set_id=$1", set.ID); err != nil {
			return err
		}
		for i, d := range docs {
			if _, err := tx.Exec(ctx, "INSERT INTO sticker_set_items(set_id, position, document_id, emoji) VALUES ($1, $2, $3, $4)",
				set.ID, i, d.ID, p.Stickers[i].Emoji); err != nil {
				return err
			}
		}
		return nil
	})
}
