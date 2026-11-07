package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"regexp"

	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
	"golang.org/x/image/draw"
)

// A collectibles bundle (scripts/collectibles.py) has the sticker bundles'
// shape: active.json names a build, builds/<sha256 of manifest>/manifest.json
// lists the collections, assets/<sha256>.<ext> are the files. The art is
// Telegram's and never goes into a repository; the bundle is local.

type collectibleManifest struct {
	Version     int                 `json:"version"`
	Source      string              `json:"source"`
	Collections []collectibleBundle `json:"collections"`
}

type bundleAsset struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type collectibleBundle struct {
	Name     string       `json:"name"`
	Title    string       `json:"title"`
	Original *bundleAsset `json:"original"`
	Models   []struct {
		Name     string      `json:"name"`
		Permille int         `json:"rarityPermille"`
		Sticker  bundleAsset `json:"sticker"`
	} `json:"models"`
	Patterns []struct {
		Name     string      `json:"name"`
		Permille int         `json:"rarityPermille"`
		Sticker  bundleAsset `json:"sticker"`
		Image    bundleAsset `json:"image"`
	} `json:"patterns"`
	Backdrops []struct {
		Name     string `json:"name"`
		ID       int    `json:"backdropId"`
		Permille int    `json:"rarityPermille"`
		Center   int    `json:"centerColor"`
		Edge     int    `json:"edgeColor"`
		Pattern  int    `json:"patternColor"`
		Text     int    `json:"textColor"`
	} `json:"backdrops"`
}

var (
	setNameRe  = regexp.MustCompile(`^[a-z0-9]{1,64}$`)
	notSlugRe  = regexp.MustCompile(`[^A-Za-z0-9]`)
	maxPattern = 4 << 20
)

// patternThumb is the size Web A asks a pattern for ("m") and draws its
// rings of from.
const patternThumb = 128

// ImportedSets is what ImportCollectibles did.
type ImportedSets struct {
	Sets, Models, Patterns, Backdrops int
}

// ImportCollectibles loads a collectibles bundle: documents for every model,
// pattern and original, and the collections' rows. Importing it again
// changes nothing; numbers already issued keep their attributes.
func (s *Service) ImportCollectibles(ctx context.Context, dir string) (ImportedSets, error) {
	var res ImportedSets
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
	var man collectibleManifest
	if err := json.Unmarshal(raw, &man); err != nil || man.Version != 1 {
		return res, fmt.Errorf("media: bad collectibles manifest")
	}
	// collections share most of their symbols, and shrinking a pattern's
	// picture is most of an import's time: each once per run
	thumbs := map[string]*Size{}
	for _, c := range man.Collections {
		if err := checkCollection(c); err != nil {
			return res, err
		}
		if err := s.importCollection(ctx, root, c, thumbs); err != nil {
			return res, fmt.Errorf("media: collection %s: %w", c.Name, err)
		}
		res.Sets++
		res.Models += len(c.Models)
		res.Patterns += len(c.Patterns)
		res.Backdrops += len(c.Backdrops)
	}
	return res, nil
}

func checkCollection(c collectibleBundle) error {
	bad := func(what string) error { return fmt.Errorf("media: collection %q: bad %s", c.Name, what) }
	if !setNameRe.MatchString(c.Name) || c.Title == "" || len(c.Title) > 64 || notSlugRe.ReplaceAllString(c.Title, "") == "" {
		return bad("metadata")
	}
	if len(c.Models) == 0 || len(c.Patterns) == 0 || len(c.Backdrops) == 0 || len(c.Models) > 1000 || len(c.Patterns) > 1000 || len(c.Backdrops) > 1000 {
		return bad("attribute count")
	}
	sum := func(n int, at func(int) (string, int)) error {
		total, seen := 0, map[string]bool{}
		for i := 0; i < n; i++ {
			name, p := at(i)
			if name == "" || len(name) > 64 || seen[name] || p < 1 || p > 1000 {
				return bad("attribute " + name)
			}
			seen[name] = true
			total += p
		}
		// each share is rounded to a permille, so the list is about 1000
		if total < 950 || total > 1050 {
			return bad("rarities")
		}
		return nil
	}
	if err := sum(len(c.Models), func(i int) (string, int) { return c.Models[i].Name, c.Models[i].Permille }); err != nil {
		return err
	}
	if err := sum(len(c.Patterns), func(i int) (string, int) { return c.Patterns[i].Name, c.Patterns[i].Permille }); err != nil {
		return err
	}
	if err := sum(len(c.Backdrops), func(i int) (string, int) { return c.Backdrops[i].Name, c.Backdrops[i].Permille }); err != nil {
		return err
	}
	assets := []bundleAsset{}
	if c.Original != nil {
		assets = append(assets, *c.Original)
	}
	for _, m := range c.Models {
		assets = append(assets, m.Sticker)
	}
	for _, p := range c.Patterns {
		assets = append(assets, p.Sticker)
		if p.Image.File != "assets/"+p.Image.SHA256+".png" || p.Image.Size < 1 || p.Image.Size > int64(maxPattern) {
			return bad("pattern image " + p.Name)
		}
	}
	for _, a := range assets {
		if !keyRe.MatchString(a.SHA256) || a.File != "assets/"+a.SHA256+".tgs" || a.Size < 1 || a.Size > maxStickerBytes {
			return bad("sticker " + a.File)
		}
	}
	for _, b := range c.Backdrops {
		for _, v := range []int{b.Center, b.Edge, b.Pattern, b.Text} {
			if v < 0 || v > 0xffffff {
				return bad("backdrop " + b.Name)
			}
		}
	}
	return nil
}

func readAsset(root string, a bundleAsset) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(a.File)))
	if err != nil {
		return nil, err
	}
	if h := sha256.Sum256(raw); int64(len(raw)) != a.Size || hex.EncodeToString(h[:]) != a.SHA256 {
		return nil, fmt.Errorf("%s: checksum changed", a.File)
	}
	return raw, nil
}

// collectibleDoc stores one sticker of a collection. The id comes from the
// file, so the same art imported twice is one document. emojiOf is the same
// file as a custom emoji, for emoji statuses.
func emojiOf(d Document, alt string) Document {
	id := seedID(fmt.Sprintf("collectible-emoji:%d", d.ID))
	d.ID, d.AccessHash, d.Kind = id, seedID(fmt.Sprintf("doc-hash:%d", id)), KindEmoji
	d.Attrs = []tg.DocumentAttributeClass{&tg.DocumentAttributeImageSize{W: 512, H: 512},
		&tg.DocumentAttributeCustomEmoji{Alt: alt, Stickerset: &tg.InputStickerSetEmpty{}, Free: true},
		&tg.DocumentAttributeFilename{FileName: "AnimatedSticker.tgs"}}
	return d
}

func (s *Service) collectibleDoc(ctx context.Context, root string, a bundleAsset, alt string, thumb *Size) (Document, error) {
	raw, err := readAsset(root, a)
	if err != nil {
		return Document{}, err
	}
	if _, msg := checkTGS(raw); msg != "" {
		return Document{}, fmt.Errorf("%s: %s", a.File, msg)
	}
	key, size, err := s.blobs.Put(ctx, bytes.NewReader(raw))
	if err != nil {
		return Document{}, err
	}
	id := seedID("collectible:" + a.SHA256)
	return Document{ID: id, AccessHash: seedID(fmt.Sprintf("doc-hash:%d", id)), Date: s.unix(), Mime: mimeTGS, Kind: KindSticker, Key: key, Size: size,
		Attrs: []tg.DocumentAttributeClass{&tg.DocumentAttributeImageSize{W: 512, H: 512},
			&tg.DocumentAttributeSticker{Alt: alt, Stickerset: &tg.InputStickerSetEmpty{}},
			&tg.DocumentAttributeFilename{FileName: "AnimatedSticker.tgs"}},
		Thumb: thumb}, nil
}

// patternImage shrinks a pattern's picture to the thumbnail Web A draws.
func (s *Service) patternImage(ctx context.Context, root string, a bundleAsset) (*Size, error) {
	raw, err := readAsset(root, a)
	if err != nil {
		return nil, err
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 {
		return nil, fmt.Errorf("%s: not a picture", a.File)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", a.File, err)
	}
	dst := image.NewNRGBA(image.Rect(0, 0, patternThumb, patternThumb))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, dst); err != nil {
		return nil, err
	}
	key, size, err := s.blobs.Put(ctx, bytes.NewReader(out.Bytes()))
	if err != nil {
		return nil, err
	}
	return &Size{Type: "m", W: patternThumb, H: patternThumb, Key: key, Bytes: int(size)}, nil
}

func (s *Service) importCollection(ctx context.Context, root string, c collectibleBundle, thumbs map[string]*Size) error {
	var docs []Document
	var original *int64
	if c.Original != nil {
		d, err := s.collectibleDoc(ctx, root, *c.Original, "🎁", nil)
		if err != nil {
			return err
		}
		docs = append(docs, d)
		original = &d.ID
	}
	models := make([][2]int64, len(c.Models))
	for i, m := range c.Models {
		d, err := s.collectibleDoc(ctx, root, m.Sticker, "🎁", nil)
		if err != nil {
			return err
		}
		e := emojiOf(d, "🎁")
		docs = append(docs, d, e)
		models[i] = [2]int64{d.ID, e.ID}
	}
	patterns := make([][2]int64, len(c.Patterns))
	for i, p := range c.Patterns {
		thumb := thumbs[p.Image.SHA256]
		if thumb == nil {
			var err error
			if thumb, err = s.patternImage(ctx, root, p.Image); err != nil {
				return err
			}
			thumbs[p.Image.SHA256] = thumb
		}
		d, err := s.collectibleDoc(ctx, root, p.Sticker, "✨", thumb)
		if err != nil {
			return err
		}
		e := emojiOf(d, "✨")
		docs = append(docs, d, e)
		patterns[i] = [2]int64{d.ID, e.ID}
	}
	// links say PlushPepe-1, as Telegram's t.me/nft/PlushPepe-1
	slug := notSlugRe.ReplaceAllString(c.Title, "")
	now := s.unix()
	return s.tx(ctx, func(tx pgx.Tx) error {
		for _, d := range docs {
			if err := s.insertDocument(ctx, tx, d); err != nil {
				return err
			}
		}
		var set int64
		err := tx.QueryRow(ctx, `INSERT INTO collectible_sets(name, title, slug, original_document_id, imported_at) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (name) DO UPDATE SET title=excluded.title, original_document_id=excluded.original_document_id, imported_at=excluded.imported_at
			RETURNING id`, c.Name, c.Title, slug, original, now).Scan(&set)
		if err != nil {
			return err
		}
		var names []string
		for i, m := range c.Models {
			names = append(names, m.Name)
			if _, err := tx.Exec(ctx, `INSERT INTO collectible_models(set_id, name, document_id, emoji_document_id, rarity_permille) VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (set_id, name) DO UPDATE SET document_id=excluded.document_id, emoji_document_id=excluded.emoji_document_id, rarity_permille=excluded.rarity_permille`,
				set, m.Name, models[i][0], models[i][1], m.Permille); err != nil {
				return err
			}
		}
		if err := dropOthers(ctx, tx, "collectible_models", "model", set, names); err != nil {
			return err
		}
		names = names[:0]
		for i, p := range c.Patterns {
			names = append(names, p.Name)
			if _, err := tx.Exec(ctx, `INSERT INTO collectible_patterns(set_id, name, document_id, emoji_document_id, rarity_permille) VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (set_id, name) DO UPDATE SET document_id=excluded.document_id, emoji_document_id=excluded.emoji_document_id, rarity_permille=excluded.rarity_permille`,
				set, p.Name, patterns[i][0], patterns[i][1], p.Permille); err != nil {
				return err
			}
		}
		if err := dropOthers(ctx, tx, "collectible_patterns", "pattern", set, names); err != nil {
			return err
		}
		names = names[:0]
		for _, b := range c.Backdrops {
			names = append(names, b.Name)
			if _, err := tx.Exec(ctx, `INSERT INTO collectible_backdrops(set_id, name, backdrop_id, center_color, edge_color, pattern_color, text_color, rarity_permille)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
				ON CONFLICT (set_id, name) DO UPDATE SET backdrop_id=excluded.backdrop_id, center_color=excluded.center_color, edge_color=excluded.edge_color,
					pattern_color=excluded.pattern_color, text_color=excluded.text_color, rarity_permille=excluded.rarity_permille`,
				set, b.Name, b.ID, b.Center, b.Edge, b.Pattern, b.Text, b.Permille); err != nil {
				return err
			}
		}
		return dropOthers(ctx, tx, "collectible_backdrops", "backdrop", set, names)
	})
}

// dropOthers removes what a new bundle no longer has. One an issued gift
// wears has to stay, and is still drawn: bundles only ever grow.
func dropOthers(ctx context.Context, tx pgx.Tx, table, column string, set int64, keep []string) error {
	_, err := tx.Exec(ctx, "DELETE FROM "+table+" t WHERE t.set_id=$1 AND NOT (t.name = ANY($2)) AND NOT EXISTS (SELECT 1 FROM unique_gifts u WHERE u.set_id=t.set_id AND u."+column+"=t.name)", set, keep)
	return err
}
