package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// Blobs stores file bytes by content: the key is the sha256 of the bytes in
// hex. Disk is the only backend for now; S3 slots in behind this.
type Blobs interface {
	Put(ctx context.Context, r io.Reader) (key string, size int64, err error)
	// Read returns up to limit bytes at offset; fewer at the end of the file.
	Read(ctx context.Context, key string, offset int64, limit int) ([]byte, error)
}

var keyRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

var errBadKey = errors.New("media: bad blob key")

// Disk keeps blobs as root/ab/cd/<key>.
type Disk struct {
	root string
}

func OpenDisk(root string) (*Disk, error) {
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o700); err != nil {
		return nil, err
	}
	return &Disk{root: root}, nil
}

func (d *Disk) path(key string) (string, error) {
	if !keyRe.MatchString(key) {
		return "", errBadKey
	}
	return filepath.Join(d.root, key[:2], key[2:4], key), nil
}

func (d *Disk) Put(ctx context.Context, r io.Reader) (string, int64, error) {
	tmp, err := os.CreateTemp(filepath.Join(d.root, "tmp"), "put-")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), ctxReader{ctx, r})
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	key := hex.EncodeToString(h.Sum(nil))
	dst, _ := d.path(key)
	if _, err := os.Stat(dst); err == nil {
		return key, n, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", 0, err
	}
	// same bytes, same name: losing a rename race to another Put is fine
	if err := os.Rename(tmp.Name(), dst); err != nil {
		if _, serr := os.Stat(dst); serr != nil {
			return "", 0, err
		}
	}
	return key, n, nil
}

func (d *Disk) Read(_ context.Context, key string, offset int64, limit int) ([]byte, error) {
	p, err := d.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("media: blob %s: %w", key, err)
	}
	defer f.Close()
	buf := make([]byte, limit)
	n, err := f.ReadAt(buf, offset)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return buf[:n], err
}

// ctxReader stops a long copy when the request is gone.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
