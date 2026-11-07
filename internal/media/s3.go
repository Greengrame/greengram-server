package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iamxvbaba/td/tgerr"
)

// S3Config is an S3-compatible bucket: Cloudflare R2 in production
// (https://<account>.r2.cloudflarestorage.com, region auto), MinIO in tests.
// Objects are addressed path-style as /<bucket>/<prefix><key>.
type S3Config struct {
	Endpoint  string
	Region    string
	Bucket    string
	Prefix    string
	AccessKey string
	SecretKey string

	CacheDir   string // read cache, plus staging for uploads
	CacheBytes int64  // read cache cap; 0 turns it off

	// Zero means the default; tests shrink them.
	PartSize       int64         // multipart part size, 16 MB
	MultipartAbove int64         // one PUT up to this size, 64 MB
	Timeout        time.Duration // one request, plus time for its body: 15 s
	Client         *http.Client
}

const (
	defaultPartSize       = 16 << 20
	defaultMultipartAbove = 64 << 20
	defaultS3Timeout      = 15 * time.Second
	maxParts              = 10000
	// a body may take Timeout plus a second per minSpeed bytes
	minSpeed = 512 << 10
	// Put keeps a body this small in memory instead of a temp file
	stageMem = 4 << 20
	// Put writes blobs up to this size to the read cache too: a fresh
	// photo or sticker is read right after it's stored
	warmMax     = 8 << 20
	partWorkers = 4
)

// S3 is a Blobs in a bucket with a disk cache in front for reads.
type S3 struct {
	cfg   S3Config
	base  *url.URL
	hc    *http.Client
	tmp   string
	cache *chunkCache
	now   func() time.Time

	fetches flight
	puts    keyLocks
	brk     breaker
}

var (
	prefixRe = regexp.MustCompile(`^[A-Za-z0-9/_.-]*$`)
	bucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
)

// errUnavailable is what clients see while the bucket can't be reached:
// a 500 they retry, not a hang.
var errUnavailable = tgerr.New(500, "STORAGE_UNAVAILABLE")

// OpenS3 checks the config, loads the cache index and makes one request
// to see the bucket and key work. A bucket that doesn't answer is only
// logged: the cache can still serve.
func OpenS3(ctx context.Context, cfg S3Config) (*S3, error) {
	u, err := url.Parse(cfg.Endpoint)
	switch {
	case err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "":
		return nil, fmt.Errorf("media: s3 endpoint %q: want https://host", cfg.Endpoint)
	case u.Path != "" && u.Path != "/" || u.RawQuery != "":
		return nil, fmt.Errorf("media: s3 endpoint %q: no path, the bucket goes in GREENGRAM_S3_BUCKET", cfg.Endpoint)
	case !bucketRe.MatchString(cfg.Bucket):
		return nil, fmt.Errorf("media: s3 bucket %q is not a bucket name", cfg.Bucket)
	case !prefixRe.MatchString(cfg.Prefix) || strings.HasPrefix(cfg.Prefix, "/") || strings.Contains(cfg.Prefix, "//") || strings.Contains(cfg.Prefix, ".."):
		return nil, fmt.Errorf("media: s3 prefix %q: letters, digits and / _ . - only", cfg.Prefix)
	case cfg.AccessKey == "" || cfg.SecretKey == "":
		return nil, errors.New("media: s3 access key or secret is empty")
	case cfg.CacheDir == "":
		return nil, errors.New("media: s3 needs a cache directory")
	}
	u.Path = ""
	if cfg.Region == "" {
		cfg.Region = "auto"
	}
	if cfg.PartSize <= 0 {
		cfg.PartSize = defaultPartSize
	}
	if cfg.MultipartAbove <= 0 {
		cfg.MultipartAbove = defaultMultipartAbove
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultS3Timeout
	}
	hc := cfg.Client
	if hc == nil {
		hc = &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   10 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConnsPerHost:   32,
		}}
	}
	tmp := filepath.Join(cfg.CacheDir, "tmp")
	// leftovers of a crash: half-staged uploads and chunks
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, err
	}
	cache, err := openChunkCache(filepath.Join(cfg.CacheDir, "chunks"), tmp, cfg.CacheBytes)
	if err != nil {
		return nil, err
	}
	s := &S3{cfg: cfg, base: u, hc: hc, tmp: tmp, cache: cache, now: time.Now}
	if err := s.probe(ctx); unavailable(err) {
		slog.Warn("media: s3 bucket not reachable at startup, serving from cache", "err", err)
	} else if err != nil {
		return nil, fmt.Errorf("media: s3 bucket %s: %w", cfg.Bucket, err)
	}
	n, size := cache.stats()
	slog.Info("media: blobs in s3", "endpoint", u.Host, "bucket", cfg.Bucket, "prefix", cfg.Prefix,
		"cache", cfg.CacheDir, "cache_mb", cfg.CacheBytes>>20, "cached_chunks", n, "cached_mb", size>>20)
	return s, nil
}

// probe reads a key nothing has; NoSuchKey means the bucket and the key
// pair work. A GET, because HEAD answers carry no error code.
func (s *S3) probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*s.cfg.Timeout)
	defer cancel()
	_, err := s.fetchChunk(ctx, strings.Repeat("0", 64), 0)
	// errCorrupt: someone stored that key; the bucket answers all the same
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errCorrupt) {
		return nil
	}
	return err
}

// s3Error is an answer from the bucket that isn't a success.
type s3Error struct {
	Op      string
	Status  int
	Code    string
	Message string
}

func (e *s3Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("s3 %s: %d %s: %s", e.Op, e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("s3 %s: %d", e.Op, e.Status)
}

func readS3Error(op string, resp *http.Response) error {
	var body struct {
		Code    string
		Message string
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	xml.Unmarshal(raw, &body)
	return &s3Error{Op: op, Status: resp.StatusCode, Code: body.Code, Message: body.Message}
}

// unavailable is the kind of failure a retry or a wait can fix: the
// network, a timeout, the bucket's 5xx and 429.
func unavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errBreakerOpen) {
		return true
	}
	var se *s3Error
	if errors.As(err, &se) {
		return se.Status >= 500 || se.Status == 429 || se.Code == "SlowDown"
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errCorrupt) || errors.Is(err, context.Canceled) {
		return false
	}
	return true
}

var errCorrupt = errors.New("media: s3 object doesn't match its key")

type s3Request struct {
	op      string
	method  string
	key     string // without the prefix
	query   url.Values
	header  http.Header
	body    io.ReaderAt
	off     int64
	size    int64
	sha     string // payload sha256, hex
	timeout time.Duration
	tries   int
}

// do sends r, retrying what unavailable says may pass, and gives each
// answer to handle, which reads the body before the request's time is up.
func (s *S3) do(ctx context.Context, r s3Request, handle func(*http.Response) error) error {
	if r.sha == "" {
		r.sha = emptySHA
	}
	if r.tries == 0 {
		r.tries = 3
	}
	timeout := s.cfg.Timeout + time.Duration(r.size/minSpeed)*time.Second
	if r.timeout > 0 {
		timeout = r.timeout
	}
	var err error
	for try := 0; try < r.tries; try++ {
		if try > 0 {
			t := time.NewTimer(time.Duration(try*try) * 250 * time.Millisecond)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
		if err = s.brk.allow(s.now()); err != nil {
			return err
		}
		err = s.once(ctx, r, timeout, handle)
		if ctx.Err() == nil {
			s.brk.record(s.now(), err, r.op)
		}
		if err == nil || !unavailable(err) || ctx.Err() != nil {
			return err
		}
	}
	return err
}

func (s *S3) once(ctx context.Context, r s3Request, timeout time.Duration, handle func(*http.Response) error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	u := *s.base
	u.Path = "/" + s.cfg.Bucket + "/"
	if r.key != "" {
		u.Path += s.cfg.Prefix + r.key
	}
	u.RawQuery = canonQuery(r.query)
	var body io.Reader = http.NoBody
	if r.body != nil && r.size > 0 {
		body = io.NewSectionReader(r.body, r.off, r.size)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, u.String(), body)
	if err != nil {
		return err
	}
	req.ContentLength = r.size
	for k, v := range r.header {
		req.Header[k] = v
	}
	sign(req, s.cfg.AccessKey, s.cfg.SecretKey, s.cfg.Region, r.sha, s.now())
	resp, err := s.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("s3 %s: no answer in %s", r.op, timeout)
		}
		return fmt.Errorf("s3 %s: %w", r.op, err)
	}
	defer resp.Body.Close()
	err = handle(resp)
	// drain a little so the connection can be reused
	io.CopyN(io.Discard, resp.Body, 4<<10)
	return err
}

// --- Blobs ---

func (s *S3) Put(ctx context.Context, r io.Reader) (string, int64, error) {
	src, size, key, done, err := s.stage(ctx, r)
	if err != nil {
		return "", 0, err
	}
	defer done()
	if err := s.ensure(ctx, key, src, size); err != nil {
		return "", 0, s.clientErr(ctx, "put", key, err)
	}
	if size <= warmMax {
		s.warm(key, src, size)
	}
	return key, size, nil
}

// Read gets [offset, offset+limit) chunk by chunk, each from the cache or
// one ranged GET.
func (s *S3) Read(ctx context.Context, key string, offset int64, limit int) ([]byte, error) {
	if !keyRe.MatchString(key) {
		return nil, errBadKey
	}
	if offset < 0 || limit < 0 {
		return nil, fmt.Errorf("media: blob read %d+%d", offset, limit)
	}
	end := offset + int64(limit)
	var out []byte
	for c := offset / chunkSize; c*chunkSize < end; c++ {
		base := c * chunkSize
		from, to := int(max(offset, base)-base), int(min(end, base+chunkSize)-base)
		b, n, err := s.chunk(ctx, key, c, from, to)
		if err != nil {
			return nil, s.clientErr(ctx, "read", key, err)
		}
		if out == nil && (n < chunkSize || base+chunkSize >= end) {
			return b, nil
		}
		if out == nil {
			out = make([]byte, 0, limit)
		}
		out = append(out, b...)
		if n < chunkSize {
			break
		}
	}
	if out == nil {
		out = []byte{}
	}
	return out, nil
}

// clientErr logs a bucket failure and turns the passing kind into
// STORAGE_UNAVAILABLE. A missing object stays an error the edge logs.
func (s *S3) clientErr(ctx context.Context, op, key string, err error) error {
	if ctx.Err() != nil {
		// the caller gave up; nothing to say about the bucket
		return err
	}
	if errors.Is(err, errBreakerOpen) {
		return fmt.Errorf("%w: %v", errUnavailable, err)
	}
	if unavailable(err) {
		slog.Warn("media: s3 "+op, "key", key, "err", err)
		return fmt.Errorf("%w: %v", errUnavailable, err)
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, context.Canceled) {
		return err
	}
	slog.Error("media: s3 "+op, "key", key, "err", err)
	return err
}

func (s *S3) chunk(ctx context.Context, key string, c int64, from, to int) ([]byte, int, error) {
	name := key + "." + strconv.FormatInt(c, 10)
	if b, n, ok := s.cache.get(name, from, to); ok {
		return b, n, nil
	}
	// one GET per chunk however many want it; the fetch outlives a caller
	// that gives up, the cache gets it either way
	data, err := s.fetches.do(ctx, name, func() ([]byte, error) {
		d, err := s.fetchChunk(context.WithoutCancel(ctx), key, c)
		if err == nil {
			s.cache.put(name, d)
		}
		return d, err
	})
	if err != nil {
		return nil, 0, err
	}
	to = min(to, len(data))
	if from >= to {
		return []byte{}, len(data), nil
	}
	return bytes.Clone(data[from:to]), len(data), nil
}

var contentRangeRe = regexp.MustCompile(`^bytes (\d+)-(\d+)/(\d+)$`)

// fetchChunk is one ranged GET. Its answer must be exactly the chunk:
// a short body is an error, never a short file.
func (s *S3) fetchChunk(ctx context.Context, key string, c int64) ([]byte, error) {
	start := c * chunkSize
	var out []byte
	err := s.do(ctx, s3Request{op: "get", method: http.MethodGet, key: key, tries: 2,
		header: http.Header{"Range": {fmt.Sprintf("bytes=%d-%d", start, start+chunkSize-1)}}},
		func(resp *http.Response) error {
			var n, total int64
			switch resp.StatusCode {
			case http.StatusRequestedRangeNotSatisfiable:
				out = []byte{}
				return nil
			case http.StatusNotFound:
				err := readS3Error("get", resp)
				if se := err.(*s3Error); se.Code == "NoSuchBucket" {
					return err
				}
				return fmt.Errorf("media: blob %s: %w", key, fs.ErrNotExist)
			case http.StatusPartialContent:
				m := contentRangeRe.FindStringSubmatch(resp.Header.Get("Content-Range"))
				if m == nil {
					return fmt.Errorf("s3 get: Content-Range %q", resp.Header.Get("Content-Range"))
				}
				a, _ := strconv.ParseInt(m[1], 10, 64)
				b, _ := strconv.ParseInt(m[2], 10, 64)
				total, _ = strconv.ParseInt(m[3], 10, 64)
				n = b - a + 1
				if a != start || n > chunkSize || n < chunkSize && b != total-1 {
					return fmt.Errorf("s3 get: asked %d+%d, got %s", start, chunkSize, m[0])
				}
			case http.StatusOK:
				// the whole object: a server that ignores Range
				if start != 0 || resp.ContentLength < 0 {
					return fmt.Errorf("s3 get: range ignored")
				}
				total = resp.ContentLength
				n = min(total, chunkSize)
			default:
				return readS3Error("get", resp)
			}
			buf := make([]byte, n)
			if _, err := io.ReadFull(resp.Body, buf); err != nil {
				return fmt.Errorf("s3 get: body cut short: %w", err)
			}
			// a blob in one chunk can be checked against its name for free
			if start == 0 && n == total {
				if sum := sha256.Sum256(buf); hex.EncodeToString(sum[:]) != key {
					return fmt.Errorf("%w: %s", errCorrupt, key)
				}
			}
			out = buf
			return nil
		})
	return out, err
}

// stage makes the body something that can be read twice, hashing it on
// the way: as is when it already is (bytes.Reader, upload parts), in
// memory when small, else a temp file.
func (s *S3) stage(ctx context.Context, r io.Reader) (io.ReaderAt, int64, string, func(), error) {
	nop := func() {}
	if ra, n, ok := readerAt(r); ok {
		h := sha256.New()
		if _, err := io.Copy(h, ctxReader{ctx, io.NewSectionReader(ra, 0, n)}); err != nil {
			return nil, 0, "", nil, err
		}
		return ra, n, hex.EncodeToString(h.Sum(nil)), nop, nil
	}
	var mem bytes.Buffer
	n, err := io.Copy(&mem, io.LimitReader(ctxReader{ctx, r}, stageMem+1))
	if err != nil {
		return nil, 0, "", nil, err
	}
	if n <= stageMem {
		sum := sha256.Sum256(mem.Bytes())
		return bytes.NewReader(mem.Bytes()), n, hex.EncodeToString(sum[:]), nop, nil
	}
	f, err := os.CreateTemp(s.tmp, "put-")
	if err != nil {
		return nil, 0, "", nil, err
	}
	done := func() {
		f.Close()
		os.Remove(f.Name())
	}
	h := sha256.New()
	w := io.MultiWriter(f, h)
	if _, err = w.Write(mem.Bytes()); err == nil {
		var m int64
		m, err = io.Copy(w, ctxReader{ctx, r})
		n += m
	}
	if err != nil {
		done()
		return nil, 0, "", nil, err
	}
	return f, n, hex.EncodeToString(h.Sum(nil)), done, nil
}

func readerAt(r io.Reader) (io.ReaderAt, int64, bool) {
	switch v := r.(type) {
	case *bytes.Reader:
		// ReadAt ignores what was read already; Put takes the rest
		if int64(v.Len()) == v.Size() {
			return v, v.Size(), true
		}
	case *partsReader:
		if v.fresh() {
			return v, v.Size(), true
		}
	}
	return nil, 0, false
}

// ensure makes key hold src unless it does already. Two Puts of the same
// bytes upload once.
func (s *S3) ensure(ctx context.Context, key string, src io.ReaderAt, size int64) error {
	unlock, err := s.puts.lock(ctx, key)
	if err != nil {
		return err
	}
	defer unlock()
	have, ok, err := s.Stat(ctx, key)
	if err != nil {
		return err
	}
	if ok && have == size {
		return nil
	}
	if ok {
		slog.Warn("media: s3 object has the wrong size, uploading it again", "key", key, "size", have, "want", size)
	}
	return s.Upload(ctx, key, src, size)
}

func (s *S3) warm(key string, src io.ReaderAt, size int64) {
	for c := int64(0); c*chunkSize < size; c++ {
		n := min(chunkSize, size-c*chunkSize)
		buf := make([]byte, n)
		if _, err := src.ReadAt(buf, c*chunkSize); err != nil && !errors.Is(err, io.EOF) {
			return
		}
		s.cache.put(key+"."+strconv.FormatInt(c, 10), buf)
	}
}

// Stat is the object's size, or ok false when there is none.
func (s *S3) Stat(ctx context.Context, key string) (size int64, ok bool, err error) {
	if !keyRe.MatchString(key) {
		return 0, false, errBadKey
	}
	err = s.do(ctx, s3Request{op: "head", method: http.MethodHead, key: key, tries: 2}, func(resp *http.Response) error {
		switch resp.StatusCode {
		case http.StatusOK:
			size, ok = resp.ContentLength, true
			return nil
		case http.StatusNotFound:
			return nil
		}
		return &s3Error{Op: "head", Status: resp.StatusCode}
	})
	return size, ok, err
}

// Upload stores size bytes of src as key, which must be their sha256: the
// bucket checks the signed payload hash, so an object either holds
// exactly those bytes or isn't there. The caller has hashed src.
func (s *S3) Upload(ctx context.Context, key string, src io.ReaderAt, size int64) error {
	if !keyRe.MatchString(key) {
		return errBadKey
	}
	if size <= s.cfg.MultipartAbove {
		return s.putObject(ctx, key, src, size, key)
	}
	return s.multipart(ctx, key, src, size)
}

func (s *S3) putObject(ctx context.Context, key string, src io.ReaderAt, size int64, sha string) error {
	return s.do(ctx, s3Request{op: "put", method: http.MethodPut, key: key, body: src, size: size, sha: sha,
		header: http.Header{"Content-Type": {"application/octet-stream"}}},
		func(resp *http.Response) error {
			if resp.StatusCode != http.StatusOK {
				return readS3Error("put", resp)
			}
			return nil
		})
}

// multipart uploads in parts of the same size, a few at a time. The
// object appears only when the bucket completes it; until then, and if
// it's aborted, a read of key finds nothing.
func (s *S3) multipart(ctx context.Context, key string, src io.ReaderAt, size int64) error {
	part := s.cfg.PartSize
	if (size+part-1)/part > maxParts {
		part = (size/(maxParts-1) + 1<<20) &^ (1<<20 - 1)
	}
	id, err := s.createMultipart(ctx, key)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			s.abortMultipart(key, id)
		}
	}()
	n := int((size + part - 1) / part)
	etags := make([]string, n)
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg      sync.WaitGroup
		errOnce sync.Once
		perr    error
	)
	jobs := make(chan int)
	for range min(partWorkers, n) {
		wg.Go(func() {
			for i := range jobs {
				off := int64(i) * part
				ln := min(part, size-off)
				etag, err := s.putPart(pctx, key, id, i+1, src, off, ln)
				if err != nil {
					errOnce.Do(func() { perr = err; cancel() })
					continue
				}
				etags[i] = etag
			}
		})
	}
feed:
	for i := range n {
		select {
		case jobs <- i:
		case <-pctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if perr != nil {
		return perr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.completeMultipart(ctx, key, id, etags); err != nil {
		// a retried complete can fail after the first one went through
		if have, ok, serr := s.Stat(ctx, key); serr == nil && ok && have == size {
			done = true
			return nil
		}
		return err
	}
	done = true
	have, ok, err := s.Stat(ctx, key)
	if err != nil {
		return err
	}
	if !ok || have != size {
		return fmt.Errorf("s3 multipart %s: completed with %d bytes, want %d", key, have, size)
	}
	return nil
}

func (s *S3) createMultipart(ctx context.Context, key string) (string, error) {
	var id string
	err := s.do(ctx, s3Request{op: "create multipart", method: http.MethodPost, key: key, query: url.Values{"uploads": {""}},
		header: http.Header{"Content-Type": {"application/octet-stream"}}},
		func(resp *http.Response) error {
			if resp.StatusCode != http.StatusOK {
				return readS3Error("create multipart", resp)
			}
			var res struct{ UploadId string }
			if err := xml.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&res); err != nil || res.UploadId == "" {
				return fmt.Errorf("s3 create multipart: no upload id (%v)", err)
			}
			id = res.UploadId
			return nil
		})
	return id, err
}

func (s *S3) putPart(ctx context.Context, key, id string, num int, src io.ReaderAt, off, size int64) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, ctxReader{ctx, io.NewSectionReader(src, off, size)}); err != nil {
		return "", err
	}
	var etag string
	err := s.do(ctx, s3Request{op: "put part", method: http.MethodPut, key: key, body: src, off: off, size: size,
		sha:   hex.EncodeToString(h.Sum(nil)),
		query: url.Values{"partNumber": {strconv.Itoa(num)}, "uploadId": {id}}},
		func(resp *http.Response) error {
			if resp.StatusCode != http.StatusOK {
				return readS3Error("put part", resp)
			}
			etag = resp.Header.Get("ETag")
			if etag == "" {
				return errors.New("s3 put part: no ETag")
			}
			return nil
		})
	return etag, err
}

func (s *S3) completeMultipart(ctx context.Context, key, id string, etags []string) error {
	type part struct {
		PartNumber int
		ETag       string
	}
	body := struct {
		XMLName xml.Name `xml:"CompleteMultipartUpload"`
		Parts   []part   `xml:"Part"`
	}{}
	for i, e := range etags {
		body.Parts = append(body.Parts, part{i + 1, e})
	}
	raw, err := xml.Marshal(body)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	return s.do(ctx, s3Request{op: "complete multipart", method: http.MethodPost, key: key,
		query: url.Values{"uploadId": {id}}, body: bytes.NewReader(raw), size: int64(len(raw)), sha: hex.EncodeToString(sum[:]),
		header: http.Header{"Content-Type": {"application/xml"}},
		// assembling a big object can take the bucket a while
		timeout: 2*s.cfg.Timeout + time.Minute},
		func(resp *http.Response) error {
			if resp.StatusCode != http.StatusOK {
				return readS3Error("complete multipart", resp)
			}
			// S3 may answer 200 and put the error in the body
			raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			if err != nil {
				return fmt.Errorf("s3 complete multipart: %w", err)
			}
			var res struct {
				XMLName xml.Name
				Code    string
				Message string
			}
			if xml.Unmarshal(raw, &res) == nil && res.XMLName.Local == "Error" {
				return &s3Error{Op: "complete multipart", Status: 500, Code: res.Code, Message: res.Message}
			}
			return nil
		})
}

func (s *S3) abortMultipart(key, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*s.cfg.Timeout)
	defer cancel()
	err := s.do(ctx, s3Request{op: "abort multipart", method: http.MethodDelete, key: key, query: url.Values{"uploadId": {id}}},
		func(resp *http.Response) error {
			if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
				return readS3Error("abort multipart", resp)
			}
			return nil
		})
	if err != nil {
		// the bucket drops unfinished uploads by itself after a while (R2: 7 days)
		slog.Warn("media: s3 abort multipart", "key", key, "err", err)
	}
}

// Sum downloads the object and hashes it, for ggblobs. size is what the
// object should hold; it sets the time allowed.
func (s *S3) Sum(ctx context.Context, key string, size int64) (string, int64, error) {
	if !keyRe.MatchString(key) {
		return "", 0, errBadKey
	}
	var sum string
	var n int64
	err := s.do(ctx, s3Request{op: "get", method: http.MethodGet, key: key, tries: 2,
		timeout: s.cfg.Timeout + time.Duration(size/minSpeed)*time.Second},
		func(resp *http.Response) error {
			switch resp.StatusCode {
			case http.StatusOK:
			case http.StatusNotFound:
				return fmt.Errorf("media: blob %s: %w", key, fs.ErrNotExist)
			default:
				return readS3Error("get", resp)
			}
			h := sha256.New()
			var err error
			if n, err = io.Copy(h, resp.Body); err != nil {
				return fmt.Errorf("s3 get: %w", err)
			}
			sum = hex.EncodeToString(h.Sum(nil))
			return nil
		})
	return sum, n, err
}

// --- plumbing ---

var errBreakerOpen = errors.New("s3: bucket unreachable, not trying for a few seconds")

// breaker stops sending after a run of failures, so while the bucket is
// down a request fails at once instead of waiting out its timeouts.
type breaker struct {
	mu    sync.Mutex
	fails int
	until time.Time
}

const (
	breakerFails = 5
	breakerPause = 10 * time.Second
)

func (b *breaker) allow(now time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if now.Before(b.until) {
		return errBreakerOpen
	}
	return nil
}

func (b *breaker) record(now time.Time, err error, op string) {
	if errors.Is(err, context.Canceled) {
		return // the caller left; says nothing about the bucket
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !unavailable(err) {
		b.fails = 0
		return
	}
	if b.fails++; b.fails >= breakerFails {
		b.fails = 0
		b.until = now.Add(breakerPause)
		slog.Warn("media: s3 keeps failing, pausing requests", "for", breakerPause, "last_op", op, "err", err)
	}
}

// flight runs one fn per name at a time and hands its result to every
// caller that asked meanwhile.
type flight struct {
	mu sync.Mutex
	m  map[string]*flightCall
}

type flightCall struct {
	done chan struct{}
	val  []byte
	err  error
}

func (f *flight) do(ctx context.Context, name string, fn func() ([]byte, error)) ([]byte, error) {
	f.mu.Lock()
	c, ok := f.m[name]
	if !ok {
		if f.m == nil {
			f.m = map[string]*flightCall{}
		}
		c = &flightCall{done: make(chan struct{})}
		f.m[name] = c
		go func() {
			c.val, c.err = fn()
			f.mu.Lock()
			delete(f.m, name)
			f.mu.Unlock()
			close(c.done)
		}()
	}
	f.mu.Unlock()
	select {
	case <-c.done:
		return c.val, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// keyLocks is a mutex per key that a caller can stop waiting for.
type keyLocks struct {
	mu sync.Mutex
	m  map[string]chan struct{}
}

func (l *keyLocks) lock(ctx context.Context, key string) (func(), error) {
	for {
		l.mu.Lock()
		ch, held := l.m[key]
		if !held {
			if l.m == nil {
				l.m = map[string]chan struct{}{}
			}
			ch = make(chan struct{})
			l.m[key] = ch
			l.mu.Unlock()
			return func() {
				l.mu.Lock()
				delete(l.m, key)
				l.mu.Unlock()
				close(ch)
			}, nil
		}
		l.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
