package media

import (
	"context"
	"crypto/sha256"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

var (
	errMediaInvalid = tgerr.New(400, "MEDIA_INVALID")
	errLocation     = tgerr.New(400, "LOCATION_INVALID")
	errOffset       = tgerr.New(400, "OFFSET_INVALID")
	errLimit        = tgerr.New(400, "LIMIT_INVALID")
)

// FromInput stores what a client attached, or finds what it re-sends, and
// returns it ready for a message.
func (s *Service) FromInput(ctx context.Context, owner int64, in tg.InputMediaClass) (Ref, error) {
	switch v := in.(type) {
	case *tg.InputMediaUploadedPhoto:
		p, err := s.UploadPhoto(ctx, owner, v.File)
		if err != nil {
			return Ref{}, err
		}
		if v.LivePhoto {
			return s.LivePhoto(ctx, s.PhotoRef(p, v.Spoiler), v.Video)
		}
		return s.PhotoRef(p, v.Spoiler), nil
	case *tg.InputMediaPhoto:
		p, err := s.InputPhoto(ctx, v.ID)
		if err != nil {
			return Ref{}, err
		}
		if v.LivePhoto {
			return s.LivePhoto(ctx, s.PhotoRef(p, v.Spoiler), v.Video)
		}
		return s.PhotoRef(p, v.Spoiler), nil
	case *tg.InputMediaUploadedDocument:
		d, err := s.UploadDocument(ctx, owner, v)
		if err != nil {
			return Ref{}, err
		}
		return s.DocumentRef(d, v.Spoiler), nil
	case *tg.InputMediaDocument:
		d, err := s.InputDocument(ctx, v.ID)
		if err != nil {
			return Ref{}, err
		}
		return s.DocumentRef(d, v.Spoiler), nil
	case *tg.InputMediaPoll:
		return s.pollFromInput(ctx, owner, v)
	case *tg.InputMediaTodo:
		return NewTodo(v.Todo, nil)
	case *tg.InputMediaDice:
		return dice(v.Emoticon)
	case *tg.InputMediaGeoPoint:
		return geo(v)
	case *tg.InputMediaVenue:
		return venue(v)
	case *tg.InputMediaGeoLive:
		return liveGeo(v)
	case *tg.InputMediaContact:
		return contact(v)
	}
	return Ref{}, errMediaInvalid
}

// pollFromInput takes in the media a poll carries (a picture above the
// question, one with the explanation) the same way, then the poll.
func (s *Service) pollFromInput(ctx context.Context, owner int64, in *tg.InputMediaPoll) (Ref, error) {
	var attached, solution tg.MessageMediaClass
	for _, it := range []struct {
		in  func() (tg.InputMediaClass, bool)
		out *tg.MessageMediaClass
	}{{in.GetAttachedMedia, &attached}, {in.GetSolutionMedia, &solution}} {
		m, ok := it.in()
		if !ok {
			continue
		}
		if _, nested := m.(*tg.InputMediaPoll); nested {
			return Ref{}, errMediaInvalid
		}
		ref, err := s.FromInput(ctx, owner, m)
		if err != nil {
			return Ref{}, err
		}
		*it.out = ref.TL
	}
	p, err := s.CreatePoll(ctx, owner, in, attached, solution)
	if err != nil {
		return Ref{}, err
	}
	return s.PollRef(p), nil
}

// File is something downloadable: a blob and how to label it.
type File struct {
	Key  string
	Size int64
	Mime string
	Date int
}

func (p Photo) File(z Size) File {
	return File{Key: z.Key, Size: int64(z.Bytes), Mime: "image/jpeg", Date: p.Date}
}

// Locate resolves photo and document locations. Peer photos have no
// access hash of their own, so the caller checks whose photo it is and
// uses Photo + File.
func (s *Service) Locate(ctx context.Context, loc tg.InputFileLocationClass) (File, error) {
	switch v := loc.(type) {
	case *tg.InputPhotoFileLocation:
		p, err := s.InputPhoto(ctx, &tg.InputPhoto{ID: v.ID, AccessHash: v.AccessHash})
		if err != nil {
			return File{}, errLocation
		}
		z, ok := p.Size(v.ThumbSize)
		if !ok {
			return File{}, errLocation
		}
		return p.File(z), nil
	case *tg.InputDocumentFileLocation:
		d, err := s.InputDocument(ctx, &tg.InputDocument{ID: v.ID, AccessHash: v.AccessHash})
		if err != nil {
			return File{}, errLocation
		}
		switch {
		case v.ThumbSize == "":
			return File{Key: d.Key, Size: d.Size, Mime: d.Mime, Date: d.Date}, nil
		case d.Thumb != nil && v.ThumbSize == d.Thumb.Type:
			return File{Key: d.Thumb.Key, Size: int64(d.Thumb.Bytes), Mime: "image/jpeg", Date: d.Date}, nil
		}
	}
	return File{}, errLocation
}

const maxChunk = 1 << 20

// checkRange is upload.getFile's rule set from core.telegram.org/api/files:
// 4 KB aligned, limit divides 1 MB, and one request never crosses a 1 MB
// boundary. precise relaxes it to 1 KB alignment for streaming players.
func checkRange(offset int64, limit int, precise bool) error {
	if offset < 0 {
		return errOffset
	}
	if limit <= 0 || limit > maxChunk {
		return errLimit
	}
	if precise {
		if offset%1024 != 0 {
			return errOffset
		}
		if limit%1024 != 0 {
			return errLimit
		}
		return nil
	}
	if limit%4096 != 0 || maxChunk%limit != 0 {
		return errLimit
	}
	if offset%4096 != 0 {
		return errOffset
	}
	if offset/maxChunk != (offset+int64(limit)-1)/maxChunk {
		return errLimit
	}
	return nil
}

// Read is one upload.getFile answer.
func (s *Service) Read(ctx context.Context, f File, offset int64, limit int, precise bool) (*tg.UploadFile, error) {
	if err := checkRange(offset, limit, precise); err != nil {
		return nil, err
	}
	out := &tg.UploadFile{Type: storageType(f.Mime), Mtime: f.Date, Bytes: []byte{}}
	if offset >= f.Size {
		return out, nil
	}
	b, err := s.blobs.Read(ctx, f.Key, offset, int(min(int64(limit), f.Size-offset)))
	if err != nil {
		return nil, err
	}
	out.Bytes = b
	return out, nil
}

func storageType(mime string) tg.StorageFileTypeClass {
	switch mime {
	case "image/jpeg":
		return &tg.StorageFileJpeg{}
	case "image/png":
		return &tg.StorageFilePng{}
	case "image/gif":
		return &tg.StorageFileGif{}
	case "image/webp":
		return &tg.StorageFileWebp{}
	case "video/mp4":
		return &tg.StorageFileMp4{}
	case "video/quicktime":
		return &tg.StorageFileMov{}
	case "audio/mpeg":
		return &tg.StorageFileMp3{}
	case "application/pdf":
		return &tg.StorageFilePdf{}
	}
	return &tg.StorageFileUnknown{}
}

const (
	hashChunk = 128 << 10
	hashCount = 8
)

// Hashes is upload.getFileHashes: sha256 of the 128 KB pieces from offset
// on, a few at a time.
func (s *Service) Hashes(ctx context.Context, f File, offset int64) ([]tg.FileHash, error) {
	if offset < 0 {
		return nil, errOffset
	}
	start := offset - offset%hashChunk
	out := []tg.FileHash{}
	for range hashCount {
		if start >= f.Size {
			break
		}
		n := int(min(hashChunk, f.Size-start))
		b, err := s.blobs.Read(ctx, f.Key, start, n)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		out = append(out, tg.FileHash{Offset: start, Limit: n, Hash: sum[:]})
		start += int64(n)
	}
	return out, nil
}
