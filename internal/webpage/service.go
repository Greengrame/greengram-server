package webpage

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// How long a fetch is trusted. A page that had nothing (or didn't
// answer) is asked again sooner.
const (
	keepFound   = 24 * time.Hour
	keepMissing = time.Hour
	maxURLLen   = 2048
)

// Preview is a cached page, the way messages show it.
type Preview struct {
	ID          int64
	URL         string // what the message linked
	DisplayURL  string
	Type        string
	SiteName    string
	Title       string
	Description string
	PhotoID     int64
	Hash        int
}

type Service struct {
	db    *pgxpool.Pool
	fetch *Fetcher
	// stores a preview's picture, returns the photo id
	photo func(ctx context.Context, raw []byte) (int64, error)
	now   func() time.Time

	mu       sync.Mutex
	inflight map[string]*call
}

type call struct {
	done chan struct{}
	p    Preview
	ok   bool
}

func New(db *pgxpool.Pool, f *Fetcher, photo func(context.Context, []byte) (int64, error)) *Service {
	return &Service{db: db, fetch: f, photo: photo, now: time.Now, inflight: map[string]*call{}}
}

// ID is a page's stable id, so every message with the link shares it.
func ID(u string) int64 {
	sum := sha256.Sum256([]byte("greengram-webpage:" + u))
	return int64(binary.LittleEndian.Uint64(sum[:8]) >> 1)
}

// Normalize makes a link fetchable: a scheme, a lowercase host, no
// fragment. "" when it isn't a web link.
func Normalize(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxURLLen {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || checkURL(u) != nil || !strings.Contains(u.Hostname(), ".") {
		return ""
	}
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	return u.String()
}

var linkRe = regexp.MustCompile(`(?i)\bhttps?://[^\s<>"']+`)

// FirstURL is the link a message's preview is for: a text link, a URL
// entity, or else anything that looks like an http link.
func FirstURL(text string, entities []tg.MessageEntityClass) string {
	var units []uint16
	for _, e := range entities {
		switch v := e.(type) {
		case *tg.MessageEntityTextURL:
			if u := Normalize(v.URL); u != "" {
				return u
			}
		case *tg.MessageEntityURL:
			if units == nil {
				units = utf16.Encode([]rune(text))
			}
			if v.Offset >= 0 && v.Length > 0 && v.Offset+v.Length <= len(units) {
				if u := Normalize(string(utf16.Decode(units[v.Offset : v.Offset+v.Length]))); u != "" {
					return u
				}
			}
		}
	}
	if m := linkRe.FindString(text); m != "" {
		return Normalize(strings.TrimRight(m, ".,;:!?)]}"))
	}
	return ""
}

const cols = "id, url, type, site_name, title, description, coalesce(photo_id, 0), hash, ok, fetched_at"

// Cached is what's known about a link without fetching it; fresh is
// false when nothing is known or it's too old.
func (s *Service) Cached(ctx context.Context, u string) (p Preview, ok, fresh bool, err error) {
	var at int
	err = s.db.QueryRow(ctx, "SELECT "+cols+" FROM webpages WHERE url=$1", u).Scan(&p.ID, &p.URL, &p.Type, &p.SiteName, &p.Title, &p.Description, &p.PhotoID, &p.Hash, &ok, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, false, false, nil
	}
	if err != nil {
		return p, false, false, err
	}
	keep := keepMissing
	if ok {
		keep = keepFound
	}
	p.DisplayURL = display(u)
	return p, ok, time.Unix(int64(at), 0).Add(keep).After(s.now()), nil
}

func display(u string) string {
	d := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	return strings.TrimSuffix(strings.TrimPrefix(d, "www."), "/")
}

// Resolve is the preview for a link, fetched if the cache has nothing
// fresh. One fetch per link at a time; the others wait for it.
func (s *Service) Resolve(ctx context.Context, u string) (Preview, bool, error) {
	p, ok, fresh, err := s.Cached(ctx, u)
	if err != nil || fresh {
		return p, ok, err
	}
	s.mu.Lock()
	c := s.inflight[u]
	if c == nil {
		c = &call{done: make(chan struct{})}
		s.inflight[u] = c
		s.mu.Unlock()
		// detached: a client giving up shouldn't waste the fetch for the next one
		go func() {
			fctx, cancel := context.WithTimeout(context.Background(), 2*fetchTimeout)
			defer cancel()
			c.p, c.ok = s.load(fctx, u)
			s.mu.Lock()
			delete(s.inflight, u)
			s.mu.Unlock()
			close(c.done)
		}()
	} else {
		s.mu.Unlock()
	}
	select {
	case <-c.done:
		return c.p, c.ok, nil
	case <-ctx.Done():
		return Preview{}, false, nil
	}
}

func (s *Service) load(ctx context.Context, u string) (Preview, bool) {
	p := Preview{ID: ID(u), URL: u, DisplayURL: display(u)}
	page, err := s.fetch.Fetch(ctx, u)
	ok := err == nil && (page.Title != "" || page.Description != "" || page.Image != "")
	if err != nil {
		slog.Debug("webpage: fetch", "url", u, "err", err)
	}
	if ok {
		p.Type, p.SiteName, p.Title, p.Description = page.Type, page.SiteName, page.Title, page.Description
		if page.Image != "" && s.photo != nil {
			if raw, err := s.fetch.Image(ctx, page.Image); err == nil {
				if id, err := s.photo(ctx, raw); err == nil {
					p.PhotoID = id
				} else {
					slog.Debug("webpage: photo", "url", page.Image, "err", err)
				}
			}
		}
		h := crc32.NewIEEE()
		for _, f := range []string{p.Type, p.SiteName, p.Title, p.Description} {
			h.Write([]byte(f))
			h.Write([]byte{0})
		}
		binary.Write(h, binary.LittleEndian, p.PhotoID)
		p.Hash = int(int32(h.Sum32()))
	}
	_, err = s.db.Exec(ctx, `INSERT INTO webpages(url, id, type, site_name, title, description, photo_id, hash, ok, fetched_at)
		VALUES ($1, $2, $3, $4, $5, $6, nullif($7::bigint, 0), $8, $9, $10)
		ON CONFLICT (url) DO UPDATE SET type=excluded.type, site_name=excluded.site_name, title=excluded.title, description=excluded.description,
			photo_id=excluded.photo_id, hash=excluded.hash, ok=excluded.ok, fetched_at=excluded.fetched_at`,
		u, p.ID, p.Type, p.SiteName, p.Title, p.Description, p.PhotoID, p.Hash, ok, int(s.now().Unix()))
	if err != nil {
		slog.Warn("webpage: store", "url", u, "err", err)
	}
	return p, ok
}
