package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Greengrame/greengram-server/internal/media/s3test"
	"github.com/iamxvbaba/td/tgerr"
)

// These run against an in-memory bucket, or against a real one (MinIO)
// when GREENGRAM_TEST_S3_ENDPOINT, _BUCKET, _ACCESS_KEY_ID and
// _SECRET_ACCESS_KEY are set. Fault tests always use the in-memory one.

// counter counts the requests a client makes, by method.
type counter struct {
	next http.RoundTripper
	mu   sync.Mutex
	n    map[string]int
}

func (c *counter) RoundTrip(r *http.Request) (*http.Response, error) {
	m := r.Method
	if r.URL.Query().Has("partNumber") {
		m = "PART"
	}
	c.mu.Lock()
	c.n[m]++
	c.mu.Unlock()
	return c.next.RoundTrip(r)
}

func (c *counter) get(m string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[m]
}

type bucket struct {
	*S3
	fake *s3test.Server // nil on a real bucket
	reqs *counter
}

func realS3Env() (S3Config, bool) {
	c := S3Config{
		Endpoint:  os.Getenv("GREENGRAM_TEST_S3_ENDPOINT"),
		Bucket:    os.Getenv("GREENGRAM_TEST_S3_BUCKET"),
		AccessKey: os.Getenv("GREENGRAM_TEST_S3_ACCESS_KEY_ID"),
		SecretKey: os.Getenv("GREENGRAM_TEST_S3_SECRET_ACCESS_KEY"),
	}
	return c, c.Endpoint != ""
}

// openBucket gives each test its own prefix, so a real bucket can be shared.
func openBucket(t *testing.T, fakeOnly bool, tune func(*S3Config)) *bucket {
	t.Helper()
	cfg, real := realS3Env()
	b := &bucket{reqs: &counter{next: http.DefaultTransport, n: map[string]int{}}}
	if !real || fakeOnly {
		b.fake = s3test.New("gg-test")
		t.Cleanup(b.fake.Close)
		cfg = S3Config{Endpoint: b.fake.URL, Bucket: "gg-test", AccessKey: "test", SecretKey: "test"}
	}
	cfg.Prefix = fmt.Sprintf("t/%s-%d/", regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(t.Name(), "_"), time.Now().UnixNano())
	cfg.CacheDir = t.TempDir()
	cfg.CacheBytes = 64 << 20
	cfg.Client = &http.Client{Transport: b.reqs}
	if tune != nil {
		tune(&cfg)
	}
	s, err := OpenS3(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	b.S3 = s
	return b
}

func randBytes(seed uint64, n int) []byte {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	b := make([]byte, n)
	for i := 0; i+8 <= n; i += 8 {
		v := r.Uint64()
		for j := range 8 {
			b[i+j] = byte(v >> (8 * j))
		}
	}
	for i := n &^ 7; i < n; i++ {
		b[i] = byte(r.Uint32())
	}
	return b
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// onlyReader hides bytes.Reader's ReadAt, the way a network body would.
type onlyReader struct{ r io.Reader }

func (o onlyReader) Read(p []byte) (int, error) { return o.r.Read(p) }

func TestS3PutReadDedupe(t *testing.T) {
	ctx := context.Background()
	b := openBucket(t, false, nil)
	for i, n := range []int{0, 10, chunkSize, chunkSize + 1, 5<<20 + 77} {
		data := randBytes(uint64(i), n)
		var src io.Reader = bytes.NewReader(data)
		if i%2 == 1 {
			src = onlyReader{src}
		}
		key, size, err := b.Put(ctx, src)
		if err != nil || key != sha(data) || size != int64(n) {
			t.Fatalf("put %d bytes: %s %d %v", n, key, size, err)
		}
		got, err := b.Read(ctx, key, 0, n)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("read %d bytes back: %d %v", n, len(got), err)
		}
		puts := b.reqs.get("PUT")
		heads := b.reqs.get("HEAD")
		if k2, _, err := b.Put(ctx, bytes.NewReader(data)); err != nil || k2 != key {
			t.Fatal(k2, err)
		}
		if b.reqs.get("PUT") != puts || b.reqs.get("HEAD") != heads+1 {
			t.Fatalf("second put of the same bytes uploaded again (puts %d -> %d)", puts, b.reqs.get("PUT"))
		}
	}
	// a fresh store (cold cache) reads them from the bucket
	if _, err := b.Read(ctx, strings.Repeat("ab", 32), 0, 10); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing key: %v", err)
	}
	if _, err := b.Read(ctx, "../x", 0, 1); err == nil {
		t.Fatal("bad key read")
	}
}

func TestS3RangedReads(t *testing.T) {
	ctx := context.Background()
	for _, cache := range []int64{0, 64 << 20} {
		t.Run(fmt.Sprint("cache", cache>>20), func(t *testing.T) {
			b := openBucket(t, false, func(c *S3Config) { c.CacheBytes = cache })
			data := randBytes(7, 3<<20+512<<10+333)
			key, _, err := b.Put(ctx, bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			n := int64(len(data))
			for _, c := range []struct {
				off   int64
				limit int
			}{
				{0, 1}, {1, 1}, {4095, 3}, {chunkSize - 1, 2}, {chunkSize - 7, 1 << 20}, {chunkSize, 4096},
				{3*chunkSize + 123, 1 << 20}, {n - 5, 100}, {n, 10}, {n + 4096, 10}, {12345, 2*chunkSize + 999},
				{0, int(n)}, {1, int(n)}, {chunkSize * 3, 0},
			} {
				got, err := b.Read(ctx, key, c.off, c.limit)
				if err != nil {
					t.Fatalf("read %d+%d: %v", c.off, c.limit, err)
				}
				lo, hi := min(c.off, n), min(c.off+int64(c.limit), n)
				if !bytes.Equal(got, data[lo:hi]) {
					t.Fatalf("read %d+%d: got %d bytes, want %d", c.off, c.limit, len(got), hi-lo)
				}
			}
			// upload.getFile's way: 128 KB at a time, in parallel
			var wg sync.WaitGroup
			var bad atomic.Int32
			for off := int64(0); off < n; off += 128 << 10 {
				wg.Go(func() {
					got, err := b.Read(ctx, key, off, 128<<10)
					if err != nil || !bytes.Equal(got, data[off:min(off+128<<10, n)]) {
						bad.Add(1)
					}
				})
			}
			wg.Wait()
			if bad.Load() > 0 {
				t.Fatalf("%d parallel part reads differ", bad.Load())
			}
		})
	}
}

func TestS3Multipart(t *testing.T) {
	ctx := context.Background()
	b := openBucket(t, false, func(c *S3Config) {
		c.PartSize = 5 << 20
		c.MultipartAbove = 6 << 20
		c.CacheBytes = 0
	})
	// through a temp file: the body can't be read twice
	data := randBytes(3, 17<<20+3)
	key, size, err := b.Put(ctx, onlyReader{bytes.NewReader(data)})
	if err != nil || key != sha(data) || size != int64(len(data)) {
		t.Fatal(key, size, err)
	}
	if n := b.reqs.get("PART"); n != 4 {
		t.Fatalf("%d part uploads, want 4", n)
	}
	got, err := b.Read(ctx, key, 0, len(data))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read back %d bytes: %v", len(got), err)
	}
	if ents, _ := os.ReadDir(b.tmp); len(ents) != 0 {
		t.Fatalf("staging left %d files", len(ents))
	}

	// a big upload from parts goes up without a temp copy
	s := New(nil, Config{Blobs: b, Parts: t.TempDir(), DC: 2})
	s.swept = time.Now()
	whole := randBytes(4, 40*MaxPartSize-1000)
	for i := 0; i < 40; i++ {
		p := whole[i*MaxPartSize : min((i+1)*MaxPartSize, len(whole))]
		if err := s.SavePart(ctx, 1, 99, i, 40, p); err != nil {
			t.Fatal(err)
		}
	}
	key, size, err = s.assemble(ctx, s.partDir(1, 99), 40, "", true)
	if err != nil || key != sha(whole) || size != int64(len(whole)) {
		t.Fatal(key, size, err)
	}
	if sum, n, err := b.Sum(ctx, key, size); err != nil || sum != key || n != size {
		t.Fatal(sum, n, err)
	}
}

// TestS3LargeMultipart: a 300 MB upload in default-sized parts, with the
// heap watched. Only against a real bucket.
func TestS3LargeMultipart(t *testing.T) {
	if _, ok := realS3Env(); !ok {
		t.Skip("GREENGRAM_TEST_S3_ENDPOINT not set")
	}
	ctx := context.Background()
	b := openBucket(t, false, func(c *S3Config) { c.CacheBytes = 0 })
	const n = 300 << 20
	stream := func() io.Reader { return io.LimitReader(rand.NewChaCha8([32]byte{1, 2}), n) }
	h := sha256.New()
	src := io.TeeReader(stream(), h)
	runtime.GC()
	var peak atomic.Uint64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		var ms runtime.MemStats
		for {
			runtime.ReadMemStats(&ms)
			if ms.HeapInuse > peak.Load() {
				peak.Store(ms.HeapInuse)
			}
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	})
	began := time.Now()
	key, size, err := b.Put(ctx, onlyReader{src})
	close(stop)
	wg.Wait()
	if err != nil || size != n || key != hex.EncodeToString(h.Sum(nil)) {
		t.Fatal(key, size, err)
	}
	t.Logf("300 MB in %s, %d parts, heap peak %d MB", time.Since(began).Round(time.Millisecond), b.reqs.get("PART"), peak.Load()>>20)
	if b.reqs.get("PART") != 19 {
		t.Fatalf("%d parts, want 19 of 16 MB", b.reqs.get("PART"))
	}
	if peak.Load() > 128<<20 {
		t.Fatalf("heap went to %d MB for a 300 MB upload", peak.Load()>>20)
	}
	if sum, got, err := b.Sum(ctx, key, size); err != nil || sum != key || got != n {
		t.Fatal(sum, got, err)
	}
	// odd ranges deep in
	for _, off := range []int64{n - 1, 16<<20 - 3, 123456789} {
		r := stream()
		io.CopyN(io.Discard, r, off)
		want, _ := io.ReadAll(io.LimitReader(r, 1<<20))
		got, err := b.Read(ctx, key, off, 1<<20)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("read at %d: %v", off, err)
		}
	}
}

func TestS3Cache(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	b := openBucket(t, false, func(c *S3Config) { c.CacheBytes = 3 << 20; c.CacheDir = dir })
	var keys []string
	var blobs [][]byte
	for i := range 6 {
		data := randBytes(uint64(100+i), 900<<10)
		k, _, err := b.Put(ctx, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		keys, blobs = append(keys, k), append(blobs, data)
	}
	if n, size := b.cache.stats(); size > 3<<20 || n != 3 {
		t.Fatalf("cache holds %d chunks, %d bytes over a 3 MB cap", n, size)
	}
	// the newest were kept: reading them costs no GET
	gets := b.reqs.get("GET")
	for i := 3; i < 6; i++ {
		if got, err := b.Read(ctx, keys[i], 100, 1000); err != nil || !bytes.Equal(got, blobs[i][100:1100]) {
			t.Fatal(err)
		}
	}
	if b.reqs.get("GET") != gets {
		t.Fatalf("cached reads went to the bucket %d times", b.reqs.get("GET")-gets)
	}
	// evicted ones come back from the bucket, and push others out
	for i := range 3 {
		if got, err := b.Read(ctx, keys[i], 0, len(blobs[i])); err != nil || !bytes.Equal(got, blobs[i]) {
			t.Fatal(err)
		}
	}
	if b.reqs.get("GET") != gets+3 {
		t.Fatalf("%d GETs for 3 evicted blobs", b.reqs.get("GET")-gets)
	}
	if _, size := b.cache.stats(); size > 3<<20 {
		t.Fatalf("cache at %d bytes", size)
	}
	// a chunk deleted by hand is fetched again
	os.Remove(b.cache.path(keys[0] + ".0"))
	if got, err := b.Read(ctx, keys[0], 0, len(blobs[0])); err != nil || !bytes.Equal(got, blobs[0]) {
		t.Fatal(err)
	}
	// a truncated chunk (a crash, a full disk) is dropped, not served short
	p := b.cache.path(keys[1] + ".0")
	if err := os.Truncate(p, 1000); err == nil {
		if got, err := b.Read(ctx, keys[1], 0, len(blobs[1])); err != nil || !bytes.Equal(got, blobs[1]) {
			t.Fatalf("read over a truncated chunk: %d bytes, %v", len(got), err)
		}
	}

	// a restart keeps the index; a lower cap trims it
	os.WriteFile(filepath.Join(dir, "chunks", "junk"), []byte("x"), 0o600)
	b2 := openBucket(t, false, func(c *S3Config) { c.CacheBytes = 1 << 20; c.CacheDir = dir })
	if n, size := b2.cache.stats(); n != 1 || size > 1<<20 {
		t.Fatalf("after restart: %d chunks, %d bytes", n, size)
	}
	if _, err := os.Stat(filepath.Join(dir, "chunks", "junk")); err == nil {
		t.Fatal("junk in the cache dir survived")
	}
}

func TestS3ConcurrentPut(t *testing.T) {
	ctx := context.Background()
	b := openBucket(t, false, func(c *S3Config) {
		c.PartSize = 5 << 20
		c.MultipartAbove = 6 << 20
	})
	data := randBytes(9, 7<<20)
	var wg sync.WaitGroup
	keys := make([]string, 12)
	errs := make([]error, 12)
	for i := range keys {
		wg.Go(func() {
			var src io.Reader = bytes.NewReader(data)
			if i%2 == 0 {
				src = onlyReader{src}
			}
			keys[i], _, errs[i] = b.Put(ctx, src)
		})
	}
	wg.Wait()
	for i := range keys {
		if errs[i] != nil || keys[i] != sha(data) {
			t.Fatalf("put %d: %s %v", i, keys[i], errs[i])
		}
	}
	if n := b.reqs.get("PART"); n != 2 {
		t.Fatalf("12 puts of the same bytes uploaded %d parts, want one upload of 2", n)
	}
	if sum, _, err := b.Sum(ctx, keys[0], int64(len(data))); err != nil || sum != keys[0] {
		t.Fatal(sum, err)
	}
}

func TestS3ConcurrentReadsOneFetch(t *testing.T) {
	ctx := context.Background()
	b := openBucket(t, true, nil)
	data := randBytes(11, 300<<10)
	key := sha(data)
	b.fake.SetObject(b.cfg.Prefix+key, data)
	b.fake.Stall.Store(int64(100 * time.Millisecond))
	gets := b.fake.Gets.Load()
	var wg sync.WaitGroup
	var bad atomic.Int32
	for range 20 {
		wg.Go(func() {
			if got, err := b.Read(ctx, key, 4096, 4096); err != nil || !bytes.Equal(got, data[4096:8192]) {
				bad.Add(1)
			}
		})
	}
	wg.Wait()
	if bad.Load() > 0 || b.fake.Gets.Load()-gets != 1 {
		t.Fatalf("%d bad reads, %d GETs for one chunk", bad.Load(), b.fake.Gets.Load()-gets)
	}
}

func isUnavailable(err error) bool {
	e, ok := tgerr.As(err)
	return ok && e.Message == "STORAGE_UNAVAILABLE"
}

func TestS3Errors(t *testing.T) {
	ctx := context.Background()
	fast := func(c *S3Config) { c.Timeout = 300 * time.Millisecond }

	t.Run("5xx then breaker", func(t *testing.T) {
		b := openBucket(t, true, fast)
		key := sha([]byte("x"))
		b.fake.SetObject(b.cfg.Prefix+key, []byte("x"))
		b.fake.Fault = func(*http.Request) int { return 503 }
		began := time.Now()
		_, err := b.Read(ctx, key, 0, 1)
		if !isUnavailable(err) || time.Since(began) > 2*time.Second {
			t.Fatalf("read on a 503 bucket: %v after %s", err, time.Since(began))
		}
		if _, _, err := b.Put(ctx, bytes.NewReader([]byte("y"))); !isUnavailable(err) {
			t.Fatalf("put on a 503 bucket: %v", err)
		}
		// by now the breaker is open: no request goes out
		for range 3 {
			b.Read(ctx, key, 0, 1)
		}
		reqs := b.fake.Requests.Load()
		began = time.Now()
		if _, err := b.Read(ctx, key, 0, 1); !isUnavailable(err) || time.Since(began) > 50*time.Millisecond {
			t.Fatalf("open breaker: %v after %s", err, time.Since(began))
		}
		if b.fake.Requests.Load() != reqs {
			t.Fatal("open breaker still sent a request")
		}
		// it closes again after its pause
		b.fake.Fault = nil
		b.now = func() time.Time { return time.Now().Add(breakerPause) }
		if got, err := b.Read(ctx, key, 0, 1); err != nil || string(got) != "x" {
			t.Fatalf("after the pause: %q %v", got, err)
		}
	})

	t.Run("stalled bucket times out", func(t *testing.T) {
		b := openBucket(t, true, fast)
		key := sha([]byte("x"))
		b.fake.SetObject(b.cfg.Prefix+key, []byte("x"))
		b.fake.Stall.Store(int64(5 * time.Second))
		began := time.Now()
		_, err := b.Read(ctx, key, 0, 1)
		if !isUnavailable(err) || time.Since(began) > 2*time.Second {
			t.Fatalf("stalled read: %v after %s", err, time.Since(began))
		}
		// the caller leaving ends its wait at once
		b.brk = breaker{}
		cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		began = time.Now()
		if _, err := b.Read(cctx, strings.Repeat("1", 64), 0, 1); err == nil || time.Since(began) > time.Second {
			t.Fatalf("cancelled read: %v after %s", err, time.Since(began))
		}
	})

	t.Run("cut body is not cached", func(t *testing.T) {
		b := openBucket(t, true, fast)
		data := randBytes(5, 2<<20)
		key := sha(data)
		b.fake.SetObject(b.cfg.Prefix+key, data)
		b.fake.CutBody.Store(true)
		if _, err := b.Read(ctx, key, 0, len(data)); err == nil {
			t.Fatal("short body read as a file")
		}
		if n, _ := b.cache.stats(); n != 0 {
			t.Fatalf("%d chunks cached from short bodies", n)
		}
		b.fake.CutBody.Store(false)
		b.brk = breaker{}
		if got, err := b.Read(ctx, key, 0, len(data)); err != nil || !bytes.Equal(got, data) {
			t.Fatal(err)
		}
	})

	t.Run("corrupt object", func(t *testing.T) {
		b := openBucket(t, true, fast)
		key := sha([]byte("right"))
		b.fake.SetObject(b.cfg.Prefix+key, []byte("wrong"))
		if _, err := b.Read(ctx, key, 0, 5); !errors.Is(err, errCorrupt) {
			t.Fatalf("corrupt object: %v", err)
		}
		if n, _ := b.cache.stats(); n != 0 {
			t.Fatal("corrupt chunk cached")
		}
	})

	t.Run("failed multipart leaves nothing", func(t *testing.T) {
		b := openBucket(t, true, func(c *S3Config) {
			fast(c)
			c.PartSize = 5 << 20
			c.MultipartAbove = 6 << 20
		})
		b.fake.Fault = func(r *http.Request) int {
			if r.URL.Query().Get("partNumber") == "2" {
				return 500
			}
			return 0
		}
		data := randBytes(6, 12<<20)
		if _, _, err := b.Put(ctx, bytes.NewReader(data)); !isUnavailable(err) {
			t.Fatalf("put with a failing part: %v", err)
		}
		if _, ok := b.fake.Object(b.cfg.Prefix + sha(data)); ok {
			t.Fatal("half an upload became an object")
		}
		if b.fake.Aborts.Load() == 0 || b.fake.Uploads() != 0 {
			t.Fatalf("multipart not aborted: %d aborts, %d open", b.fake.Aborts.Load(), b.fake.Uploads())
		}
	})

	t.Run("payload hash is checked", func(t *testing.T) {
		b := openBucket(t, true, fast)
		// what Upload sends is signed with the key as its sha256
		if err := b.Upload(ctx, sha([]byte("a")), bytes.NewReader([]byte("b")), 1); err == nil {
			t.Fatal("bytes that don't match their key were stored")
		}
		if _, ok := b.fake.Object(b.cfg.Prefix + sha([]byte("a"))); ok {
			t.Fatal("object exists")
		}
	})

	t.Run("bad bucket or key fails at open", func(t *testing.T) {
		fake := s3test.New("gg-test")
		defer fake.Close()
		cfg := S3Config{Endpoint: fake.URL, Bucket: "other", AccessKey: "a", SecretKey: "b", CacheDir: t.TempDir()}
		if _, err := OpenS3(ctx, cfg); err == nil || !strings.Contains(err.Error(), "NoSuchBucket") {
			t.Fatalf("wrong bucket: %v", err)
		}
		fake.Fault = func(*http.Request) int { return 403 }
		cfg.Bucket = "gg-test"
		if _, err := OpenS3(ctx, cfg); err == nil {
			t.Fatal("403 at open passed")
		}
		// a bucket that doesn't answer only warns: the cache still serves
		fake.Fault = func(*http.Request) int { return 503 }
		cfg.Timeout = 100 * time.Millisecond
		if _, err := OpenS3(ctx, cfg); err != nil {
			t.Fatalf("503 at open: %v", err)
		}
		for _, bad := range []S3Config{
			{Endpoint: "ftp://x", Bucket: "gg-test", AccessKey: "a", SecretKey: "b", CacheDir: "x"},
			{Endpoint: "https://x/bucket", Bucket: "gg-test", AccessKey: "a", SecretKey: "b", CacheDir: "x"},
			{Endpoint: "https://x", Bucket: "gg-test", Prefix: "../", AccessKey: "a", SecretKey: "b", CacheDir: "x"},
			{Endpoint: "https://x", Bucket: "gg-test", AccessKey: "a", CacheDir: "x"},
		} {
			if _, err := OpenS3(ctx, bad); err == nil {
				t.Fatalf("config %+v accepted", bad)
			}
		}
	})
}

func TestCopyDisk(t *testing.T) {
	ctx := context.Background()
	b := openBucket(t, false, func(c *S3Config) {
		c.PartSize = 5 << 20
		c.MultipartAbove = 6 << 20
		c.CacheBytes = 0
	})
	root := t.TempDir()
	d, err := OpenDisk(root)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	var blobs [][]byte
	for i, n := range []int{1, 1000, 300 << 10, 7 << 20} {
		data := randBytes(uint64(200+i), n)
		k, _, err := d.Put(ctx, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		keys, blobs = append(keys, k), append(blobs, data)
	}
	// a file whose bytes don't match its name: reported, not uploaded
	bad := sha([]byte("not this"))
	p, _ := d.path(bad)
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, []byte("something else"), 0o600)
	os.WriteFile(filepath.Join(root, "tmp", "put-123"), []byte("half"), 0o600)

	run := func(quick bool) CopyReport {
		t.Helper()
		rep, err := CopyDisk(ctx, root, b.S3, CopyOptions{Jobs: 3, Quick: quick, Log: t.Logf})
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}
	rep := run(false)
	if rep.Files != 5 || rep.Uploaded != 4 || rep.BadLocal != 1 || rep.Verified != 4 || rep.Failed != 0 {
		t.Fatalf("first copy: %+v", rep)
	}
	if _, ok, _ := b.Stat(ctx, bad); ok {
		t.Fatal("bad disk file uploaded")
	}
	rep = run(false)
	if rep.Uploaded != 0 || rep.Present != 4 || rep.Verified != 4 {
		t.Fatalf("second copy: %+v", rep)
	}
	rep = run(true)
	if rep.Uploaded != 0 || rep.Present != 4 || rep.Verified != 0 {
		t.Fatalf("quick copy: %+v", rep)
	}
	// a damaged object in the bucket is replaced by a full run
	wrong := randBytes(1, 1000)
	if err := b.putObject(ctx, keys[1], bytes.NewReader(wrong), int64(len(wrong)), sha(wrong)); err != nil {
		t.Fatal(err)
	}
	if rep = run(false); rep.Uploaded != 1 || rep.Present != 3 {
		t.Fatalf("copy over a damaged object: %+v", rep)
	}
	for i, k := range keys {
		if sum, _, err := b.Sum(ctx, k, int64(len(blobs[i]))); err != nil || sum != k {
			t.Fatalf("%s in the bucket: %s %v", k, sum, err)
		}
	}
	// the disk store is untouched
	for i, k := range keys {
		if got, err := d.Read(ctx, k, 0, len(blobs[i])); err != nil || !bytes.Equal(got, blobs[i]) {
			t.Fatal("disk blob changed")
		}
	}
}

func TestPartsReaderAt(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	var sizes []int64
	var whole []byte
	for i, n := range []int{4096, 4096, 4096, 100} {
		p := filepath.Join(dir, fmt.Sprint(i))
		data := randBytes(uint64(i), n)
		os.WriteFile(p, data, 0o600)
		paths, sizes, whole = append(paths, p), append(sizes, int64(n)), append(whole, data...)
	}
	r := &partsReader{paths: paths, sizes: sizes}
	if r.Size() != int64(len(whole)) || !r.fresh() {
		t.Fatal(r.Size())
	}
	for _, c := range [][2]int{{0, 10}, {4090, 20}, {4096, 4096}, {100, 12000}, {12200, 100}, {12300, 200}, {0, len(whole)}} {
		buf := make([]byte, c[1])
		n, err := r.ReadAt(buf, int64(c[0]))
		want := whole[c[0]:min(c[0]+c[1], len(whole))]
		if !bytes.Equal(buf[:n], want) || (n < c[1]) != errors.Is(err, io.EOF) {
			t.Fatalf("ReadAt(%d, %d) = %d, %v", c[0], c[1], n, err)
		}
	}
	got, _ := io.ReadAll(r)
	if !bytes.Equal(got, whole) || r.fresh() {
		t.Fatal("Read after ReadAt")
	}
}

// TestS3WrongSecret: a real bucket refuses the signature, and the store
// refuses to start on it.
func TestS3WrongSecret(t *testing.T) {
	cfg, ok := realS3Env()
	if !ok {
		t.Skip("GREENGRAM_TEST_S3_ENDPOINT not set")
	}
	cfg.SecretKey += "x"
	cfg.CacheDir = t.TempDir()
	if _, err := OpenS3(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("wrong secret: %v", err)
	}
}
