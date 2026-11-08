package site

import (
	"bytes"
	"context"
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A signed-out visitor never gets Web A. Its first screen is a phone
// number form that looks like someone else's messenger on a domain that
// isn't theirs, which is how browsers' phishing lists ended up flagging
// the site. They get a page about this instance at / instead, and every
// link into the app goes through /login, which sends them on once signed in.

//go:embed landing
var landingFiles embed.FS

var landingPage = template.Must(template.ParseFS(landingFiles, "landing/index.html"))

// Landing is what the home page says about the instance.
type Landing struct {
	Name    string // the instance's name, settings siteName
	Signup  bool   // registrationEnabled: show "create account"
	Contact string // GREENGRAM_ADMIN_CONTACT; "" leaves it out
	// GREENGRAM_COMMUNITY_URL, an http(s) link; anything else leaves it out
	Community string
}

const robots = `User-agent: *
Allow: /$
Allow: /__greengram/landing/
Disallow: /
`

func (s *Site) registerLanding(mux *http.ServeMux) {
	files, _ := fs.Sub(landingFiles, "landing")
	mux.Handle("GET /__greengram/landing/", http.StripPrefix("/__greengram/landing/", static(http.FileServerFS(files))))
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Write([]byte(robots))
	})
}

// guest answers a request web() would serve the client for, when it comes
// without a session: the landing page at /, /login for links into the app.
// The build's files stay public (the shell's scripts, icons, emoji), except
// its HTML pages. false: not a guest's request, web() goes on.
func (s *Site) guest(w http.ResponseWriter, r *http.Request) bool {
	name := path.Clean("/" + r.URL.Path)
	if s.WebDir != "" && path.Ext(name) != ".html" {
		if st, err := os.Stat(filepath.Join(s.WebDir, filepath.FromSlash(name))); err == nil && !st.IsDir() {
			return false
		}
	}
	// a missing file is a 404 either way
	if ext := path.Ext(name); ext != "" && ext != ".html" {
		return false
	}
	if s.SignedIn != nil && s.SignedIn(r) {
		return false
	}
	q := r.URL.Query()
	if r.URL.Path == "/" && !q.Has("tgaddr") && !q.Has("account") {
		s.landing(w, r)
		return true
	}
	next := url.Values{"next": {r.URL.RequestURI()}}
	if v := q.Get("account"); v != "" {
		next.Set("account", v)
	}
	page(w)
	http.Redirect(w, r, "/login?"+next.Encode(), http.StatusSeeOther)
	return true
}

func (s *Site) landing(w http.ResponseWriter, r *http.Request) {
	info := s.info(r.Context())
	data := map[string]any{
		"Name":   info.Name,
		"Signup": info.Signup,
		"Title":  "Greengram, an unofficial Telegram clone",
	}
	if info.Name != "Greengram" {
		data["Title"] = info.Name + " · Greengram, an unofficial Telegram clone"
	}
	if c := strings.TrimSpace(info.Contact); c != "" {
		data["Contact"] = c
		if u, err := url.Parse(c); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			data["ContactURL"] = u.String()
		} else if a, err := mail.ParseAddress(c); err == nil && a.Address == c {
			data["ContactURL"] = "mailto:" + c
		}
	}
	if u, err := url.Parse(strings.TrimSpace(info.Community)); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
		data["Community"] = u.String()
		data["CommunityName"] = "Community"
		if h := strings.TrimPrefix(u.Hostname(), "www."); h == "discord.gg" || h == "discord.com" {
			data["CommunityName"] = "Discord"
		}
	}
	var b bytes.Buffer
	if err := landingPage.Execute(&b, data); err != nil {
		slog.Error("site: landing", "err", err)
		http.Error(w, "Something went wrong.", http.StatusInternalServerError)
		return
	}
	page(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b.Bytes())
}

// landingCache keeps Info's answer for 10 seconds: the home page is what
// crawlers and link previews hit.
type landingCache struct {
	mu  sync.Mutex
	at  time.Time
	val Landing
}

func (s *Site) info(ctx context.Context) Landing {
	s.cache.mu.Lock()
	defer s.cache.mu.Unlock()
	if !s.cache.at.IsZero() && time.Since(s.cache.at) < 10*time.Second {
		return s.cache.val
	}
	v := Landing{Name: "Greengram"}
	if s.Info != nil {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		v = s.Info(ctx)
		cancel()
	}
	if strings.TrimSpace(v.Name) == "" {
		v.Name = "Greengram"
	}
	s.cache.at, s.cache.val = time.Now(), v
	return v
}
