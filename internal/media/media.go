// Package media is files, photos, documents and sticker sets: uploads in
// parts, the blob store, thumbnails, upload.getFile and the TL clients see
// for all of it. Messages carry media as a Ref built here.
package media

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"sync"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	Blobs Blobs
	Parts string // staging directory for upload parts
	DC    int    // the DC clients download from
}

type Service struct {
	db    *pgxpool.Pool
	blobs Blobs
	parts string
	dc    int
	now   func() time.Time

	sweepMu sync.Mutex
	swept   time.Time

	files fileLocks
}

// fileLocks serialize what touches one upload's parts: a part the client
// re-sends must not land while the parts are being read into a blob, or
// after they were put together and removed.
type fileLocks struct {
	mu sync.Mutex
	m  map[[2]int64]*fileLock
}

type fileLock struct {
	sync.Mutex
	refs int
}

func (l *fileLocks) lock(owner, fileID int64) (unlock func()) {
	k := [2]int64{owner, fileID}
	l.mu.Lock()
	if l.m == nil {
		l.m = map[[2]int64]*fileLock{}
	}
	f := l.m[k]
	if f == nil {
		f = &fileLock{}
		l.m[k] = f
	}
	f.refs++
	l.mu.Unlock()
	f.Lock()
	return func() {
		f.Unlock()
		l.mu.Lock()
		if f.refs--; f.refs == 0 {
			delete(l.m, k)
		}
		l.mu.Unlock()
	}
}

func New(db *pgxpool.Pool, cfg Config) *Service {
	return &Service{db: db, blobs: cfg.Blobs, parts: cfg.Parts, dc: cfg.DC, now: time.Now}
}

func (s *Service) unix() int { return int(s.now().Unix()) }

func (s *Service) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.db, fn)
}

// Kinds, as stored in documents.kind and messages.media_kind.
const (
	KindPhoto   = "photo"
	KindFile    = "file"
	KindVideo   = "video"
	KindRound   = "round"
	KindVoice   = "voice"
	KindAudio   = "audio"
	KindGIF     = "gif"
	KindSticker = "sticker"
	KindEmoji   = "emoji"
	KindWebPage = "webpage" // a link preview
)

// Ref is media attached to a message: what kind it is (for search
// filters), which photo or document, and the TL clients get. The zero Ref
// is no media.
type Ref struct {
	Kind string
	ID   int64
	TL   tg.MessageMediaClass
}

func (r Ref) Empty() bool { return r.TL == nil }

// Encode is the TL for the message row; nil without media.
func (r Ref) Encode() ([]byte, error) {
	if r.TL == nil {
		return nil, nil
	}
	var b bin.Buffer
	if err := r.TL.Encode(&b); err != nil {
		return nil, err
	}
	return b.Buf, nil
}

// Load fills a Ref from the message row's columns.
func Load(raw []byte, kind *string, id *int64) (Ref, error) {
	if len(raw) == 0 {
		return Ref{}, nil
	}
	m, err := tg.DecodeMessageMedia(&bin.Buffer{Buf: raw})
	if err != nil {
		return Ref{}, err
	}
	r := Ref{TL: m}
	if kind != nil {
		r.Kind = *kind
	}
	if id != nil {
		r.ID = *id
	}
	return r, nil
}

// newID is a random positive id for photos and documents; also used for
// access hashes.
func newID() int64 {
	var b [8]byte
	rand.Read(b[:])
	return int64(binary.LittleEndian.Uint64(b[:]) >> 1)
}

// fileRef is what we hand out as file_reference. Access is the access
// hash; the reference only has to be non-empty and stable, apps resend it
// and some treat an empty one as "refetch".
func fileRef(id int64) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(id)^0x6772656e)
	return b[:]
}

func encodeAttrs(list []tg.DocumentAttributeClass) ([]byte, error) {
	if len(list) == 0 {
		return nil, nil
	}
	var b bin.Buffer
	b.PutVectorHeader(len(list))
	for _, a := range list {
		if err := a.Encode(&b); err != nil {
			return nil, err
		}
	}
	return b.Buf, nil
}

func decodeAttrs(raw []byte) ([]tg.DocumentAttributeClass, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	b := &bin.Buffer{Buf: raw}
	n, err := b.VectorHeader()
	if err != nil {
		return nil, err
	}
	out := make([]tg.DocumentAttributeClass, 0, n)
	for range n {
		a, err := tg.DecodeDocumentAttribute(b)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

type execer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// putFile registers a stored blob; the same bytes twice are one row.
func putFile(ctx context.Context, q execer, key string, size int64) error {
	_, err := q.Exec(ctx, "INSERT INTO files(key, size) VALUES ($1, $2) ON CONFLICT DO NOTHING", key, size)
	return err
}
