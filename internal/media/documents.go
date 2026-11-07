package media

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
)

var errDocInvalid = tgerr.New(400, "DOCUMENT_INVALID")

type Document struct {
	ID, AccessHash int64
	Owner          int64
	Date           int
	Mime           string
	Kind           string
	Key            string
	Size           int64
	Attrs          []tg.DocumentAttributeClass
	Thumb          *Size
}

// kindOf reads what a document is from its attributes, the way the apps
// sort shared media.
func kindOf(attrs []tg.DocumentAttributeClass, forceFile bool) string {
	if forceFile {
		return KindFile
	}
	kind, animated := KindFile, false
	for _, a := range attrs {
		switch v := a.(type) {
		case *tg.DocumentAttributeSticker:
			return KindSticker
		case *tg.DocumentAttributeCustomEmoji:
			return KindEmoji
		case *tg.DocumentAttributeVideo:
			if v.RoundMessage {
				kind = KindRound
			} else if kind == KindFile {
				kind = KindVideo
			}
		case *tg.DocumentAttributeAudio:
			if v.Voice {
				kind = KindVoice
			} else {
				kind = KindAudio
			}
		case *tg.DocumentAttributeAnimated:
			animated = true
		}
	}
	if animated && (kind == KindVideo || kind == KindFile) {
		return KindGIF
	}
	return kind
}

func hasAttr[T tg.DocumentAttributeClass](attrs []tg.DocumentAttributeClass) bool {
	for _, a := range attrs {
		if _, ok := a.(T); ok {
			return true
		}
	}
	return false
}

// UploadDocument stores an uploaded file with its attributes and a
// thumbnail: the client's, or one made from the file when it's a picture.
func (s *Service) UploadDocument(ctx context.Context, owner int64, in *tg.InputMediaUploadedDocument) (Document, error) {
	u, err := s.open(ctx, owner, in.File)
	if err != nil {
		return Document{}, err
	}
	d := Document{ID: newID(), AccessHash: newID(), Owner: owner, Date: s.unix(), Mime: strings.TrimSpace(in.MimeType), Key: u.key, Size: u.size}
	d.Attrs = append([]tg.DocumentAttributeClass(nil), in.Attributes...)
	if d.Mime == "" {
		head, err := s.blobs.Read(ctx, u.key, 0, 512)
		if err != nil {
			return d, err
		}
		d.Mime, _, _ = strings.Cut(http.DetectContentType(head), ";")
	}
	if u.name != "" && !hasAttr[*tg.DocumentAttributeFilename](d.Attrs) {
		d.Attrs = append(d.Attrs, &tg.DocumentAttributeFilename{FileName: u.name})
	}
	d.Kind = kindOf(d.Attrs, in.ForceFile)

	// a broken thumbnail is the client's problem, not a reason to refuse
	// the file
	var raw []byte
	if th, ok := in.GetThumb(); ok {
		if tu, err := s.open(ctx, owner, th); err == nil {
			raw, _ = s.bytes(ctx, tu, maxPhotoBytes, errPhotoTooBig)
		}
	} else if strings.HasPrefix(d.Mime, "image/") && d.Kind == KindFile {
		raw, _ = s.bytes(ctx, u, maxPhotoBytes, errPhotoTooBig)
		if img, err := decodeImage(raw); err == nil && !hasAttr[*tg.DocumentAttributeImageSize](d.Attrs) {
			d.Attrs = append(d.Attrs, &tg.DocumentAttributeImageSize{W: img.Bounds().Dx(), H: img.Bounds().Dy()})
		}
	}
	if len(raw) > 0 {
		if z, err := s.thumb(ctx, raw); err == nil {
			d.Thumb = &z
		} else {
			slog.Debug("media: thumbnail", "err", err)
		}
	}
	return d, s.tx(ctx, func(tx pgx.Tx) error { return s.insertDocument(ctx, tx, d) })
}

func (s *Service) insertDocument(ctx context.Context, q execer, d Document) error {
	attrs, err := encodeAttrs(d.Attrs)
	if err != nil {
		return err
	}
	if err := putFile(ctx, q, d.Key, d.Size); err != nil {
		return err
	}
	var tkey *string
	var tw, th, tsize *int
	if d.Thumb != nil {
		if err := putFile(ctx, q, d.Thumb.Key, int64(d.Thumb.Bytes)); err != nil {
			return err
		}
		tkey, tw, th, tsize = &d.Thumb.Key, &d.Thumb.W, &d.Thumb.H, &d.Thumb.Bytes
	}
	var ownerID *int64
	if d.Owner != 0 {
		ownerID = &d.Owner
	}
	_, err = q.Exec(ctx, `INSERT INTO documents(id, access_hash, owner_id, date, mime, kind, key, size, attributes, thumb_key, thumb_w, thumb_h, thumb_size)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) ON CONFLICT (id) DO NOTHING`,
		d.ID, d.AccessHash, ownerID, d.Date, d.Mime, d.Kind, d.Key, d.Size, attrs, tkey, tw, th, tsize)
	return err
}

const docCols = "id, access_hash, coalesce(owner_id, 0), date, mime, kind, key, size, attributes, thumb_key, thumb_w, thumb_h, thumb_size"

func scanDocument(row pgx.Row) (Document, error) {
	var (
		d      Document
		attrs  []byte
		tkey   *string
		tw, th *int
		tsize  *int
	)
	if err := row.Scan(&d.ID, &d.AccessHash, &d.Owner, &d.Date, &d.Mime, &d.Kind, &d.Key, &d.Size, &attrs, &tkey, &tw, &th, &tsize); err != nil {
		return d, err
	}
	if tkey != nil {
		d.Thumb = &Size{Type: "m", Key: *tkey, W: deref(tw), H: deref(th), Bytes: deref(tsize)}
	}
	var err error
	d.Attrs, err = decodeAttrs(attrs)
	return d, err
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// Documents loads documents by id, in the order asked.
func (s *Service) Documents(ctx context.Context, list []int64) ([]Document, error) {
	rows, err := s.db.Query(ctx, "SELECT "+docCols+" FROM documents WHERE id = ANY($1)", list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int64]Document{}
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		byID[d.ID] = d
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Document, 0, len(byID))
	for _, id := range list {
		if d, ok := byID[id]; ok {
			out = append(out, d)
			delete(byID, id)
		}
	}
	return out, nil
}

// InputDocument resolves a document a client names by id and access hash.
func (s *Service) InputDocument(ctx context.Context, in tg.InputDocumentClass) (Document, error) {
	v, ok := in.(*tg.InputDocument)
	if !ok {
		return Document{}, errDocInvalid
	}
	list, err := s.Documents(ctx, []int64{v.ID})
	if err != nil {
		return Document{}, err
	}
	if len(list) == 0 || list[0].AccessHash != v.AccessHash {
		return Document{}, errDocInvalid
	}
	return list[0], nil
}

const mimeTGS = "application/x-tgsticker"

// blankOutline is a photoPathSize that decodes to "M0,0z" and draws nothing.
// The Android app won't call a .tgs an animated sticker while its thumbs list
// is empty (MessageObject.isAnimatedStickerDocument): custom emoji stay blank
// wherever it asks for a still frame, which is every emoji for a non-Premium
// account on a mid-range phone, and sticker messages turn into file rows.
// Our seeded packs have no thumbnails to send.
var blankOutline = []byte{0x00, 0x80}

func (s *Service) DocumentTL(d Document) *tg.Document {
	out := &tg.Document{ID: d.ID, AccessHash: d.AccessHash, FileReference: fileRef(d.ID), Date: d.Date, MimeType: d.Mime, Size: d.Size,
		DCID: s.dc, Attributes: d.Attrs}
	if out.Attributes == nil {
		out.Attributes = []tg.DocumentAttributeClass{}
	}
	switch {
	case d.Thumb != nil:
		out.SetThumbs([]tg.PhotoSizeClass{&tg.PhotoSize{Type: d.Thumb.Type, W: d.Thumb.W, H: d.Thumb.H, Size: d.Thumb.Bytes}})
	case d.Mime == mimeTGS && (d.Kind == KindEmoji || d.Kind == KindSticker):
		out.SetThumbs([]tg.PhotoSizeClass{&tg.PhotoPathSize{Type: "j", Bytes: blankOutline}})
	}
	return out
}

// DocumentRef is a document ready to attach to a message.
func (s *Service) DocumentRef(d Document, spoiler bool) Ref {
	m := &tg.MessageMediaDocument{
		Spoiler: spoiler,
		Video:   d.Kind == KindVideo || d.Kind == KindRound,
		Round:   d.Kind == KindRound,
		Voice:   d.Kind == KindVoice,
	}
	m.SetDocument(s.DocumentTL(d))
	return Ref{Kind: d.Kind, ID: d.ID, TL: m}
}
