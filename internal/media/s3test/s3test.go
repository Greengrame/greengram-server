// Package s3test is an in-memory S3 bucket for tests: objects, ranged GETs
// and multipart uploads, path-style, with hooks to make it fail. It checks
// payload hashes but not signatures; MinIO does that in the e2e run.
package s3test

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Server struct {
	*httptest.Server
	Bucket string

	mu      sync.Mutex
	objects map[string][]byte
	uploads map[string]map[int][]byte
	nextID  int

	// Fault, when set, may answer a request instead of the bucket: it
	// returns a status (0 lets the request through).
	Fault func(r *http.Request) int
	// Stall holds every request this long first.
	Stall atomic.Int64 // nanoseconds
	// CutBody sends GET bodies short of their Content-Length.
	CutBody atomic.Bool

	Requests atomic.Int64
	Gets     atomic.Int64
	Puts     atomic.Int64 // object PUTs and part PUTs
	Heads    atomic.Int64
	Aborts   atomic.Int64
}

func New(bucket string) *Server {
	s := &Server{Bucket: bucket, objects: map[string][]byte{}, uploads: map[string]map[int][]byte{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Object is what the bucket holds under name (prefix included).
func (s *Server) Object(name string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.objects[name]
	return b, ok
}

// SetObject puts bytes in behind the client's back.
func (s *Server) SetObject(name string, b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[name] = b
}

func (s *Server) Uploads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.uploads)
}

func fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message></Error>", code, code)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.Requests.Add(1)
	if d := s.Stall.Load(); d > 0 {
		select {
		case <-time.After(time.Duration(d)):
		case <-r.Context().Done():
			return
		}
	}
	if s.Fault != nil {
		if st := s.Fault(r); st != 0 {
			io.Copy(io.Discard, r.Body)
			code := "InternalError"
			switch {
			case st == 403:
				code = "AccessDenied"
			case st == 429:
				code = "SlowDown"
			case st < 500:
				code = "InvalidRequest"
			}
			fail(w, st, code)
			return
		}
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
		fail(w, 403, "AccessDenied")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	bucket, name, _ := strings.Cut(path, "/")
	if bucket != s.Bucket {
		fail(w, 404, "NoSuchBucket")
		return
	}
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodPost && q.Has("uploads"):
		s.mu.Lock()
		s.nextID++
		id := fmt.Sprintf("up-%d", s.nextID)
		s.uploads[id] = map[int][]byte{}
		s.mu.Unlock()
		fmt.Fprintf(w, "<InitiateMultipartUploadResult><Bucket>%s</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>", bucket, name, id)
	case r.Method == http.MethodPut && q.Has("uploadId"):
		s.Puts.Add(1)
		body, ok := readBody(w, r)
		if !ok {
			return
		}
		n, _ := strconv.Atoi(q.Get("partNumber"))
		s.mu.Lock()
		up, ok := s.uploads[q.Get("uploadId")]
		if ok {
			up[n] = body
		}
		s.mu.Unlock()
		if !ok {
			fail(w, 404, "NoSuchUpload")
			return
		}
		sum := md5.Sum(body)
		w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
	case r.Method == http.MethodPost && q.Has("uploadId"):
		var req struct {
			Parts []struct {
				PartNumber int
				ETag       string
			} `xml:"Part"`
		}
		if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, 400, "MalformedXML")
			return
		}
		s.mu.Lock()
		up, ok := s.uploads[q.Get("uploadId")]
		if !ok {
			s.mu.Unlock()
			fail(w, 404, "NoSuchUpload")
			return
		}
		nums := make([]int, 0, len(req.Parts))
		for _, p := range req.Parts {
			nums = append(nums, p.PartNumber)
		}
		if !sort.IntsAreSorted(nums) || len(nums) != len(up) {
			s.mu.Unlock()
			fail(w, 400, "InvalidPart")
			return
		}
		var all []byte
		for _, p := range req.Parts {
			b := up[p.PartNumber]
			sum := md5.Sum(b)
			if `"`+hex.EncodeToString(sum[:])+`"` != p.ETag {
				s.mu.Unlock()
				fail(w, 400, "InvalidPart")
				return
			}
			all = append(all, b...)
		}
		delete(s.uploads, q.Get("uploadId"))
		s.objects[name] = all
		s.mu.Unlock()
		fmt.Fprintf(w, "<CompleteMultipartUploadResult><Key>%s</Key></CompleteMultipartUploadResult>", name)
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		s.Aborts.Add(1)
		s.mu.Lock()
		delete(s.uploads, q.Get("uploadId"))
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut:
		s.Puts.Add(1)
		body, ok := readBody(w, r)
		if !ok {
			return
		}
		s.mu.Lock()
		s.objects[name] = body
		s.mu.Unlock()
	case r.Method == http.MethodHead || r.Method == http.MethodGet:
		if r.Method == http.MethodHead {
			s.Heads.Add(1)
		} else {
			s.Gets.Add(1)
		}
		s.mu.Lock()
		b, ok := s.objects[name]
		s.mu.Unlock()
		if !ok {
			if r.Method == http.MethodHead {
				w.WriteHeader(404)
				return
			}
			fail(w, 404, "NoSuchKey")
			return
		}
		status := 200
		if rg := r.Header.Get("Range"); rg != "" {
			var a, z int
			if _, err := fmt.Sscanf(rg, "bytes=%d-%d", &a, &z); err != nil {
				fail(w, 400, "InvalidRange")
				return
			}
			if a >= len(b) {
				fail(w, 416, "InvalidRange")
				return
			}
			z = min(z, len(b)-1)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", a, z, len(b)))
			b = b[a : z+1]
			status = 206
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.WriteHeader(status)
		if r.Method == http.MethodGet {
			if s.CutBody.Load() && len(b) > 1 {
				b = b[:len(b)/2]
			}
			w.Write(b)
		}
	default:
		fail(w, 405, "MethodNotAllowed")
	}
}

// readBody checks the signed payload hash like S3 does.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, false
	}
	if r.ContentLength >= 0 && int64(len(body)) != r.ContentLength {
		fail(w, 400, "IncompleteBody")
		return nil, false
	}
	want := r.Header.Get("X-Amz-Content-Sha256")
	sum := sha256.Sum256(body)
	if want != "UNSIGNED-PAYLOAD" && want != hex.EncodeToString(sum[:]) {
		fail(w, 400, "XAmzContentSHA256Mismatch")
		return nil, false
	}
	return body, true
}
