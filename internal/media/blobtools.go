package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"
)

// What cmd/ggblobs does: move a disk store into a bucket and check that
// the bucket has everything the database points at.

type CopyOptions struct {
	Jobs int
	// Quick trusts an object that is already there with the right size
	// instead of downloading and hashing it.
	Quick bool
	Log   func(format string, args ...any)
}

type CopyReport struct {
	Files     int   // blobs found on disk
	Uploaded  int   // put in the bucket by this run
	Present   int   // already there
	Verified  int   // bucket copy hashed and matched
	BadLocal  int   // disk file doesn't hash to its name: skipped
	Failed    int   // couldn't be uploaded or didn't verify
	BytesSent int64 // uploaded
	Bytes     int64 // of all files
}

// CopyDisk puts every blob under root (a Disk's directory) into dst.
// What's there already with the right size is skipped, so a run that was
// cut off can be started again. The disk is only read.
func CopyDisk(ctx context.Context, root string, dst *S3, opt CopyOptions) (CopyReport, error) {
	var rep CopyReport
	if opt.Jobs < 1 {
		opt.Jobs = 4
	}
	logf := opt.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	var mu sync.Mutex
	count := func(f func()) {
		mu.Lock()
		f()
		mu.Unlock()
	}
	paths := make(chan string)
	var wg sync.WaitGroup
	var done atomic.Int64
	for range opt.Jobs {
		wg.Go(func() {
			for p := range paths {
				res, n, err := copyOne(ctx, p, dst, opt.Quick)
				count(func() {
					rep.Bytes += n
					switch res {
					case copyUploaded:
						rep.Uploaded++
						rep.Verified++
						rep.BytesSent += n
					case copyPresent:
						rep.Present++
					case copyPresentVerified:
						rep.Present++
						rep.Verified++
					case copyBadLocal:
						rep.BadLocal++
					case copyFailed:
						rep.Failed++
					}
				})
				if err != nil {
					logf("%s: %v", filepath.Base(p), err)
				}
				if d := done.Add(1); d%500 == 0 {
					logf("%d done", d)
				}
			}
		})
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "tmp" && filepath.Dir(p) == filepath.Clean(root) {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !keyRe.MatchString(name) || filepath.Dir(p) != filepath.Join(root, name[:2], name[2:4]) {
			return nil
		}
		rep.Files++
		select {
		case paths <- p:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	close(paths)
	wg.Wait()
	return rep, err
}

type copyResult int

const (
	copyFailed copyResult = iota
	copyUploaded
	copyPresent
	copyPresentVerified
	copyBadLocal
)

func copyOne(ctx context.Context, p string, dst *S3, quick bool) (copyResult, int64, error) {
	key := filepath.Base(p)
	f, err := os.Open(p)
	if err != nil {
		return copyFailed, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return copyFailed, 0, err
	}
	size := st.Size()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return copyFailed, size, err
	}
	if hex.EncodeToString(h.Sum(nil)) != key {
		return copyBadLocal, size, errors.New("disk file doesn't match its name, skipped")
	}
	have, ok, err := dst.Stat(ctx, key)
	if err != nil {
		return copyFailed, size, err
	}
	if ok && have == size {
		if quick {
			return copyPresent, size, nil
		}
		sum, n, err := dst.Sum(ctx, key, size)
		if err != nil {
			return copyFailed, size, err
		}
		if sum == key && n == size {
			return copyPresentVerified, size, nil
		}
		// a bad copy in the bucket: replace it
	}
	if err := dst.Upload(ctx, key, f, size); err != nil {
		return copyFailed, size, err
	}
	sum, n, err := dst.Sum(ctx, key, size)
	if err != nil {
		return copyFailed, size, err
	}
	if sum != key || n != size {
		return copyFailed, size, fmt.Errorf("bucket copy hashes to %s (%d bytes)", sum, n)
	}
	return copyUploaded, size, nil
}

type VerifyOptions struct {
	Jobs int
	// Hash downloads every object and checks its sha256 instead of only
	// its size.
	Hash bool
}

type VerifyReport struct {
	Keys      int
	OK        int
	Missing   int
	WrongSize int
	BadHash   int
	Failed    int // couldn't ask
	Bytes     int64
	// up to 20 of each, for the operator
	MissingKeys []string
	BadKeys     []string
}

// blobKeysSQL is every blob key a table holds. The others reference files,
// so files alone would do; listing them all doesn't rely on that.
const blobKeysSQL = `
SELECT k.key, f.size FROM (
	SELECT key FROM files
	UNION SELECT key FROM uploads
	UNION SELECT key FROM photo_sizes
	UNION SELECT key FROM documents
	UNION SELECT thumb_key FROM documents WHERE thumb_key IS NOT NULL
) k LEFT JOIN files f ON f.key = k.key ORDER BY k.key`

// VerifyBlobs checks that every key the database references is in the
// bucket with its size (and with Hash, its bytes).
func VerifyBlobs(ctx context.Context, db *pgxpool.Pool, s *S3, opt VerifyOptions) (VerifyReport, error) {
	var rep VerifyReport
	type want struct {
		key  string
		size *int64
	}
	rows, err := db.Query(ctx, blobKeysSQL)
	if err != nil {
		return rep, err
	}
	var list []want
	for rows.Next() {
		var w want
		if err := rows.Scan(&w.key, &w.size); err != nil {
			rows.Close()
			return rep, err
		}
		list = append(list, w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return rep, err
	}
	rep.Keys = len(list)
	if opt.Jobs < 1 {
		opt.Jobs = 8
	}
	var mu sync.Mutex
	note := func(keys *[]string, key string) {
		if len(*keys) < 20 {
			*keys = append(*keys, key)
		}
	}
	jobs := make(chan want)
	var wg sync.WaitGroup
	for range opt.Jobs {
		wg.Go(func() {
			for w := range jobs {
				size, ok, err := s.Stat(ctx, w.key)
				var sum string
				if err == nil && ok && opt.Hash {
					sum, size, err = s.Sum(ctx, w.key, size)
				}
				mu.Lock()
				switch {
				case err != nil:
					rep.Failed++
					note(&rep.BadKeys, w.key)
				case !ok:
					rep.Missing++
					note(&rep.MissingKeys, w.key)
				case w.size != nil && size != *w.size:
					rep.WrongSize++
					note(&rep.BadKeys, w.key)
				case opt.Hash && sum != w.key:
					rep.BadHash++
					note(&rep.BadKeys, w.key)
				default:
					rep.OK++
					rep.Bytes += size
				}
				mu.Unlock()
			}
		})
	}
feed:
	for _, w := range list {
		select {
		case jobs <- w:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	return rep, ctx.Err()
}
