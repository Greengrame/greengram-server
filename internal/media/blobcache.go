package media

import (
	"container/list"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// chunkSize is how blobs are fetched and cached. upload.getFile never
// crosses a 1 MB boundary, so one request is one chunk.
const chunkSize = 1 << 20

var chunkRe = regexp.MustCompile(`^[0-9a-f]{64}\.[0-9]+$`)

// chunkCache keeps blob chunks on local disk, least recently used out
// first once it holds more than max bytes. The bucket has every byte; a
// chunk that's gone (evicted, deleted by hand) is fetched again.
type chunkCache struct {
	dir string // dir/ab/<key>.<chunk>
	tmp string
	max int64

	mu   sync.Mutex
	lru  *list.List // of *cacheEntry, newest first
	idx  map[string]*list.Element
	size int64
}

type cacheEntry struct {
	name    string
	size    int64
	touched time.Time
}

func openChunkCache(dir, tmp string, max int64) (*chunkCache, error) {
	c := &chunkCache{dir: dir, tmp: tmp, max: max, lru: list.New(), idx: map[string]*list.Element{}}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var found []cacheEntry
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		// a size 0 chunk is what a crash mid-write can leave
		if !chunkRe.MatchString(d.Name()) || info.Size() == 0 || info.Size() > chunkSize || filepath.Dir(p) != filepath.Join(dir, d.Name()[:2]) {
			os.Remove(p)
			return nil
		}
		found = append(found, cacheEntry{name: d.Name(), size: info.Size(), touched: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(found, func(i, j int) bool { return found[i].touched.Before(found[j].touched) })
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range found {
		e := found[i]
		c.idx[e.name] = c.lru.PushFront(&e)
		c.size += e.size
	}
	c.evictLocked()
	return c, nil
}

func (c *chunkCache) path(name string) string {
	return filepath.Join(c.dir, name[:2], name)
}

// get returns bytes [from, to) of a cached chunk and the chunk's length.
func (c *chunkCache) get(name string, from, to int) ([]byte, int, bool) {
	c.mu.Lock()
	el, ok := c.idx[name]
	if !ok {
		c.mu.Unlock()
		return nil, 0, false
	}
	c.lru.MoveToFront(el)
	e := el.Value.(*cacheEntry)
	n := int(e.size)
	// the order survives a restart through mtimes; an hour is close enough
	touch := time.Since(e.touched) > time.Hour
	if touch {
		e.touched = time.Now()
	}
	c.mu.Unlock()

	f, err := os.Open(c.path(name))
	if err != nil {
		// evicted after the lookup, or removed by hand
		c.drop(name)
		return nil, 0, false
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.Size() != int64(n) {
		c.drop(name)
		return nil, 0, false
	}
	if touch {
		now := time.Now()
		os.Chtimes(f.Name(), now, now)
	}
	to = min(to, n)
	if from >= to {
		return []byte{}, n, true
	}
	buf := make([]byte, to-from)
	if _, err := f.ReadAt(buf, int64(from)); err != nil {
		c.drop(name)
		return nil, 0, false
	}
	return buf, n, true
}

// put stores a whole chunk. It goes to a temp file first and is renamed
// into place, so a chunk is either complete or not there.
func (c *chunkCache) put(name string, data []byte) {
	if len(data) == 0 || int64(len(data)) > c.max {
		return
	}
	c.mu.Lock()
	_, have := c.idx[name]
	c.mu.Unlock()
	if have {
		return
	}
	tmp, err := os.CreateTemp(c.tmp, "chunk-")
	if err != nil {
		slog.Warn("media: blob cache", "err", err)
		return
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		slog.Warn("media: blob cache", "err", err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, have := c.idx[name]; have {
		os.Remove(tmp.Name())
		return
	}
	dst := c.path(name)
	err = os.MkdirAll(filepath.Dir(dst), 0o700)
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		os.Remove(tmp.Name())
		slog.Warn("media: blob cache", "err", err)
		return
	}
	c.idx[name] = c.lru.PushFront(&cacheEntry{name: name, size: int64(len(data)), touched: time.Now()})
	c.size += int64(len(data))
	c.evictLocked()
}

func (c *chunkCache) drop(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.idx[name]; ok {
		c.removeLocked(el)
	}
}

// evictLocked deletes under the lock, so a put of the same name can't land
// between taking an entry out and removing its file. Readers that already
// opened the file keep reading it.
func (c *chunkCache) evictLocked() {
	for c.size > c.max {
		el := c.lru.Back()
		if el == nil {
			return
		}
		c.removeLocked(el)
	}
}

func (c *chunkCache) removeLocked(el *list.Element) {
	e := el.Value.(*cacheEntry)
	c.lru.Remove(el)
	delete(c.idx, e.name)
	c.size -= e.size
	if err := os.Remove(c.path(e.name)); err != nil && !os.IsNotExist(err) {
		slog.Warn("media: blob cache evict", "err", err)
	}
}

// stats is for tests and the startup log.
func (c *chunkCache) stats() (entries int, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.idx), c.size
}
