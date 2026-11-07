package media

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
)

// Telegram's limits: 512 KB parts, 4000 of them (2000 MB).
const (
	MaxPartSize = 512 << 10
	MaxParts    = 4000
)

var (
	errPartInvalid  = tgerr.New(400, "FILE_PART_INVALID")
	errPartEmpty    = tgerr.New(400, "FILE_PART_EMPTY")
	errPartTooBig   = tgerr.New(400, "FILE_PART_TOO_BIG")
	errPartsInvalid = tgerr.New(400, "FILE_PARTS_INVALID")
	errPartSize     = tgerr.New(400, "FILE_PART_SIZE_INVALID")
	errPartChanged  = tgerr.New(400, "FILE_PART_SIZE_CHANGED")
	errFileInvalid  = tgerr.New(400, "FILE_ID_INVALID")
	errMD5          = tgerr.New(400, "MD5_CHECKSUM_INVALID")
)

func (s *Service) partDir(owner, fileID int64) string {
	return filepath.Join(s.parts, strconv.FormatInt(owner, 10), strconv.FormatUint(uint64(fileID), 16))
}

// SavePart stores one part of an upload. total is the part count a big
// file (saveBigFilePart) promises, 0 for a small one.
func (s *Service) SavePart(ctx context.Context, owner, fileID int64, part, total int, data []byte) error {
	switch {
	case part < 0 || part >= MaxParts:
		return errPartInvalid
	case len(data) == 0:
		return errPartEmpty
	case len(data) > MaxPartSize:
		return errPartTooBig
	case total < 0 || total > MaxParts || total > 0 && part >= total:
		return errPartsInvalid
	}
	s.maybeSweep()
	defer s.files.lock(owner, fileID)()
	dir := s.partDir(owner, fileID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if total > 0 {
		if err := writeAtomic(filepath.Join(dir, "total"), []byte(strconv.Itoa(total))); err != nil {
			return err
		}
	}
	return writeAtomic(filepath.Join(dir, strconv.Itoa(part)), data)
}

// writeAtomic, because a client re-sends a part it saw no answer for and
// a reader must never see half of one.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// upload is a finished InputFile.
type upload struct {
	key  string
	size int64
	name string
}

func inputFile(in tg.InputFileClass) (id int64, parts int, name, sum string, big bool, err error) {
	switch v := in.(type) {
	case *tg.InputFile:
		return v.ID, v.Parts, v.Name, v.MD5Checksum, false, nil
	case *tg.InputFileBig:
		return v.ID, v.Parts, v.Name, "", true, nil
	}
	return 0, 0, "", "", false, errFileInvalid
}

// open turns the client's InputFile into a stored blob, putting its parts
// together the first time.
func (s *Service) open(ctx context.Context, owner int64, in tg.InputFileClass) (upload, error) {
	id, parts, name, sum, big, err := inputFile(in)
	if err != nil {
		return upload{}, err
	}
	if parts < 1 || parts > MaxParts {
		return upload{}, errPartsInvalid
	}
	// held until the parts are a blob and gone, so two sends of one file
	// don't both assemble it and no part changes under the reader
	defer s.files.lock(owner, id)()
	u := upload{name: name}
	err = s.db.QueryRow(ctx, "SELECT key, size FROM uploads WHERE owner_id=$1 AND file_id=$2", owner, id).Scan(&u.key, &u.size)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return u, err
	}
	dir := s.partDir(owner, id)
	u.key, u.size, err = s.assemble(ctx, dir, parts, sum, big)
	if err != nil {
		return u, err
	}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := putFile(ctx, tx, u.key, u.size); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO uploads(owner_id, file_id, key, size) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING", owner, id, u.key, u.size)
		return err
	})
	if err != nil {
		return u, err
	}
	os.RemoveAll(dir)
	return u, nil
}

func (s *Service) assemble(ctx context.Context, dir string, parts int, sum string, big bool) (string, int64, error) {
	if big {
		raw, err := os.ReadFile(filepath.Join(dir, "total"))
		if err != nil || strings.TrimSpace(string(raw)) != strconv.Itoa(parts) {
			return "", 0, errPartsInvalid
		}
	}
	// a missing part first: the apps re-upload exactly the one named
	paths := make([]string, parts)
	sizes := make([]int64, parts)
	for i := range parts {
		paths[i] = filepath.Join(dir, strconv.Itoa(i))
		st, err := os.Stat(paths[i])
		if err != nil {
			return "", 0, tgerr.New(400, fmt.Sprintf("FILE_PART_%d_MISSING", i))
		}
		sizes[i] = st.Size()
	}
	first := sizes[0]
	for i, n := range sizes[1:] {
		if i+1 < parts-1 && n != first || n > first {
			return "", 0, errPartChanged
		}
	}
	// every part but the last has the same size, and it divides 512 KB
	if parts > 1 && (first%1024 != 0 || MaxPartSize%first != 0) {
		return "", 0, errPartSize
	}
	r := &partsReader{paths: paths, sizes: sizes}
	defer r.Close()
	var src io.Reader = r
	var h hash.Hash
	if sum != "" {
		h = md5.New()
		src = io.TeeReader(r, h)
	}
	key, size, err := s.blobs.Put(ctx, src)
	if err != nil {
		return "", 0, err
	}
	// the blob may stay behind without a files row; it's the client's
	// bytes either way and a later upload of them reuses it
	if h != nil && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), sum) {
		return "", 0, errMD5
	}
	return key, size, nil
}

// partsReader reads the part files one after another, one open at a time.
// It is also an io.ReaderAt, so the S3 store can hash the parts and then
// upload them without a copy of the whole file.
type partsReader struct {
	paths []string
	sizes []int64
	next  int
	cur   *os.File
}

func (r *partsReader) Read(p []byte) (int, error) {
	for {
		if r.cur == nil {
			if r.next == len(r.paths) {
				return 0, io.EOF
			}
			f, err := os.Open(r.paths[r.next])
			if err != nil {
				return 0, err
			}
			r.next++
			r.cur = f
		}
		n, err := r.cur.Read(p)
		if errors.Is(err, io.EOF) {
			r.cur.Close()
			r.cur = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (r *partsReader) Size() int64 {
	var n int64
	for _, s := range r.sizes {
		n += s
	}
	return n
}

// fresh: Read hasn't started, so ReadAt and Read see the same bytes.
func (r *partsReader) fresh() bool {
	return r.next == 0 && r.cur == nil && len(r.sizes) == len(r.paths)
}

// ReadAt opens the parts it needs per call; safe for concurrent use.
func (r *partsReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("media: negative offset")
	}
	i, base := 0, int64(0)
	for i < len(r.sizes) && off >= base+r.sizes[i] {
		base += r.sizes[i]
		i++
	}
	n := 0
	for n < len(p) && i < len(r.sizes) {
		f, err := os.Open(r.paths[i])
		if err != nil {
			return n, err
		}
		want := min(int64(len(p)-n), r.sizes[i]-(off-base))
		m, err := f.ReadAt(p[n:n+int(want)], off-base)
		f.Close()
		n += m
		off += int64(m)
		if err != nil && !errors.Is(err, io.EOF) {
			return n, err
		}
		if int64(m) < want {
			// a part shorter than when it was measured
			return n, io.ErrUnexpectedEOF
		}
		base += r.sizes[i]
		i++
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (r *partsReader) Close() {
	if r.cur != nil {
		r.cur.Close()
	}
}

// bytes reads a whole upload; for photos and thumbs, which are small.
func (s *Service) bytes(ctx context.Context, u upload, max int64, tooBig error) ([]byte, error) {
	if u.size > max {
		return nil, tooBig
	}
	return s.blobs.Read(ctx, u.key, 0, int(u.size))
}

const (
	partsMaxAge  = 24 * time.Hour
	sweepEvery   = time.Hour
	uploadsAlive = 7 * 24 * time.Hour
)

// maybeSweep drops abandoned uploads at most once an hour, off the
// request path.
func (s *Service) maybeSweep() {
	s.sweepMu.Lock()
	now := s.now()
	if now.Sub(s.swept) < sweepEvery {
		s.sweepMu.Unlock()
		return
	}
	s.swept = now
	s.sweepMu.Unlock()
	go s.sweep(now)
}

func (s *Service) sweep(now time.Time) {
	owners, _ := os.ReadDir(s.parts)
	for _, o := range owners {
		dir := filepath.Join(s.parts, o.Name())
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			if info, err := f.Info(); err == nil && now.Sub(info.ModTime()) > partsMaxAge {
				os.RemoveAll(filepath.Join(dir, f.Name()))
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := s.db.Exec(ctx, "DELETE FROM uploads WHERE created_at < $1", now.Add(-uploadsAlive)); err != nil {
		slog.Warn("media: sweep uploads", "err", err)
	}
}
