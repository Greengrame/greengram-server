package media

import (
	"bytes"
	"context"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Telegram's photo sizes: the letter says which box the size fits in.
var photoBoxes = []struct {
	typ string
	box int
}{{"s", 100}, {"m", 320}, {"x", 800}, {"y", 1280}, {"w", 2560}}

const (
	maxPhotoBytes = 10 << 20
	MaxPhotoBytes = maxPhotoBytes
	maxPixels     = 60_000_000 // decoding a bigger one costs more memory than it's worth
	maxRatio      = 20
	jpegQuality   = 87
	thumbBox      = 320
)

var (
	errPhotoTooBig  = tgerr.New(400, "PHOTO_SAVE_FILE_INVALID")
	errPhotoDims    = tgerr.New(400, "PHOTO_INVALID_DIMENSIONS")
	errImageFailed  = tgerr.New(400, "IMAGE_PROCESS_FAILED")
	errPhotoInvalid = tgerr.New(400, "PHOTO_ID_INVALID")
)

type Size struct {
	Type  string
	W, H  int
	Key   string
	Bytes int
}

type Photo struct {
	ID, AccessHash int64
	Owner          int64
	Date           int
	Sizes          []Size // smallest first
}

// Largest is the biggest size, for profile photo downloads.
func (p Photo) Largest() Size { return p.Sizes[len(p.Sizes)-1] }

// Fit is the smallest size covering box, or the largest there is.
func (p Photo) Fit(box int) Size {
	for _, z := range p.Sizes {
		if max(z.W, z.H) >= box {
			return z
		}
	}
	return p.Largest()
}

func (p Photo) Size(typ string) (Size, bool) {
	for _, z := range p.Sizes {
		if z.Type == typ {
			return z, true
		}
	}
	return Size{}, false
}

func decodeImage(raw []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, errImageFailed
	}
	w, h := cfg.Width, cfg.Height
	if w <= 0 || h <= 0 || w*h > maxPixels || w > h*maxRatio || h > w*maxRatio {
		return nil, errPhotoDims
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, errImageFailed
	}
	return img, nil
}

func fit(w, h, box int) (int, int) {
	if w <= box && h <= box {
		return w, h
	}
	if w >= h {
		return box, max(1, (h*box+w/2)/w)
	}
	return max(1, (w*box+h/2)/h), box
}

// scale draws src into a w×h canvas over white: JPEG has no alpha and a
// transparent PNG would come out black.
func scale(src image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	if b := src.Bounds(); b.Dx() == w && b.Dy() == h {
		draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Over)
		return dst
	}
	draw.BiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}

func (s *Service) putJPEG(ctx context.Context, img image.Image, typ string) (Size, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return Size{}, err
	}
	n := buf.Len()
	key, _, err := s.blobs.Put(ctx, &buf)
	b := img.Bounds()
	return Size{Type: typ, W: b.Dx(), H: b.Dy(), Key: key, Bytes: n}, err
}

// photoSizes makes every size up to the original (or 2560). Each comes
// from the one above it, which is much cheaper than scaling the original
// every time and looks the same.
func (s *Service) photoSizes(ctx context.Context, img image.Image) ([]Size, error) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	type target struct {
		typ  string
		w, h int
	}
	var want []target
	for _, b := range photoBoxes {
		tw, th := fit(w, h, b.box)
		want = append(want, target{b.typ, tw, th})
		if tw == w && th == h {
			break
		}
	}
	out := make([]Size, len(want))
	src := img
	for i := len(want) - 1; i >= 0; i-- {
		t := want[i]
		scaled := scale(src, t.w, t.h)
		z, err := s.putJPEG(ctx, scaled, t.typ)
		if err != nil {
			return nil, err
		}
		out[i] = z
		src = scaled
	}
	return out, nil
}

// thumb is the "m" thumbnail of a document.
func (s *Service) thumb(ctx context.Context, raw []byte) (Size, error) {
	img, err := decodeImage(raw)
	if err != nil {
		return Size{}, err
	}
	w, h := fit(img.Bounds().Dx(), img.Bounds().Dy(), thumbBox)
	return s.putJPEG(ctx, scale(img, w, h), "m")
}

// UploadPhoto stores an uploaded picture as a photo with all its sizes.
func (s *Service) UploadPhoto(ctx context.Context, owner int64, in tg.InputFileClass) (Photo, error) {
	u, err := s.open(ctx, owner, in)
	if err != nil {
		return Photo{}, err
	}
	raw, err := s.bytes(ctx, u, maxPhotoBytes, errPhotoTooBig)
	if err != nil {
		return Photo{}, err
	}
	return s.NewPhoto(ctx, owner, raw)
}

// NewPhoto stores a picture as a photo with all its sizes; owner 0 is nobody
// (a link preview's picture).
func (s *Service) NewPhoto(ctx context.Context, owner int64, raw []byte) (Photo, error) {
	img, err := decodeImage(raw)
	if err != nil {
		return Photo{}, err
	}
	sizes, err := s.photoSizes(ctx, img)
	if err != nil {
		return Photo{}, err
	}
	p := Photo{ID: newID(), AccessHash: newID(), Owner: owner, Date: s.unix(), Sizes: sizes}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO photos(id, access_hash, owner_id, date) VALUES ($1, $2, nullif($3::bigint, 0), $4)", p.ID, p.AccessHash, owner, p.Date); err != nil {
			return err
		}
		for _, z := range sizes {
			if err := putFile(ctx, tx, z.Key, int64(z.Bytes)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "INSERT INTO photo_sizes(photo_id, type, w, h, key, size) VALUES ($1, $2, $3, $4, $5, $6)",
				p.ID, z.Type, z.W, z.H, z.Key, z.Bytes); err != nil {
				return err
			}
		}
		return nil
	})
	return p, err
}

// Photos loads photos by id, in the order asked; missing ones are left out.
func (s *Service) Photos(ctx context.Context, list []int64) ([]Photo, error) {
	rows, err := s.db.Query(ctx, `SELECT p.id, p.access_hash, coalesce(p.owner_id, 0), p.date, z.type, z.w, z.h, z.key, z.size
		FROM photos p JOIN photo_sizes z ON z.photo_id = p.id WHERE p.id = ANY($1) ORDER BY p.id, z.w * z.h`, list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int64]*Photo{}
	for rows.Next() {
		var p Photo
		var z Size
		if err := rows.Scan(&p.ID, &p.AccessHash, &p.Owner, &p.Date, &z.Type, &z.W, &z.H, &z.Key, &z.Bytes); err != nil {
			return nil, err
		}
		if byID[p.ID] == nil {
			byID[p.ID] = &p
		}
		byID[p.ID].Sizes = append(byID[p.ID].Sizes, z)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Photo, 0, len(byID))
	for _, id := range list {
		if p, ok := byID[id]; ok {
			out = append(out, *p)
			delete(byID, id)
		}
	}
	return out, nil
}

func (s *Service) Photo(ctx context.Context, id int64) (Photo, bool, error) {
	list, err := s.Photos(ctx, []int64{id})
	if err != nil || len(list) == 0 {
		return Photo{}, false, err
	}
	return list[0], true, nil
}

// InputPhoto resolves a photo a client names by id and access hash.
func (s *Service) InputPhoto(ctx context.Context, in tg.InputPhotoClass) (Photo, error) {
	v, ok := in.(*tg.InputPhoto)
	if !ok {
		return Photo{}, errPhotoInvalid
	}
	p, ok, err := s.Photo(ctx, v.ID)
	if err != nil {
		return p, err
	}
	if !ok || p.AccessHash != v.AccessHash {
		return p, errPhotoInvalid
	}
	return p, nil
}

func (s *Service) PhotoTL(p Photo) *tg.Photo {
	out := &tg.Photo{ID: p.ID, AccessHash: p.AccessHash, FileReference: fileRef(p.ID), Date: p.Date, DCID: s.dc}
	out.Sizes = make([]tg.PhotoSizeClass, 0, len(p.Sizes))
	for _, z := range p.Sizes {
		out.Sizes = append(out.Sizes, &tg.PhotoSize{Type: z.Type, W: z.W, H: z.H, Size: z.Bytes})
	}
	return out
}

// PhotoRef is a photo ready to attach to a message.
func (s *Service) PhotoRef(p Photo, spoiler bool) Ref {
	m := &tg.MessageMediaPhoto{Spoiler: spoiler}
	m.SetPhoto(s.PhotoTL(p))
	return Ref{Kind: KindPhoto, ID: p.ID, TL: m}
}
