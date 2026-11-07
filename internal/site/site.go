// Package site serves the browser side: the sign-in page, the admin panel
// build and the Telegram Web A build. The builds come from
// scripts/build-web.sh; this package only serves directories.
package site

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Greengrame/greengram-server/internal/account"
)

//go:embed login
var loginFiles embed.FS

// The Stars top-up page the apps open from a payment form's url.
//
//go:embed pay
var payFiles embed.FS

// The panel's Stars and Premium artwork, Web A's own with some air around it.
//
//go:embed stars-logo.svg
var starsLogo []byte

//go:embed premium-logo.svg
var premiumLogo []byte

// The old server's policy. wasm-unsafe-eval: Web A decodes media and
// stickers in wasm; blob: workers: its media workers.
const (
	cspHead = "default-src 'self'; connect-src 'self' blob: data:; script-src 'self' 'wasm-unsafe-eval'; worker-src 'self' blob:; " +
		"style-src 'self' 'unsafe-inline'; font-src 'self' data:; img-src 'self' data: blob:; media-src 'self' data: blob:; object-src 'none'; "
	cspTail = "; base-uri 'self'; frame-ancestors 'none'; form-action 'self'"
	csp     = cspHead + "frame-src 'self'" + cspTail
	// Web A's page: a bot's mini app is someone else's site in an iframe. Web A
	// sandboxes it; the admin panel and sign-in keep the narrower csp.
	webCSP = cspHead + "frame-src 'self' https:" + cspTail
)

type Site struct {
	WebDir   string // Web A dist; "" = not served
	AdminDir string // admin build: index.html + admin-assets/
	// Admin resolves a request to a signed-in administrator.
	Admin func(r *http.Request) (account.User, string, error)
	// SignedIn: the request has a live session for the account slot it
	// opens. nil: nobody is, everyone gets the landing page and /login.
	SignedIn func(r *http.Request) bool
	// Info is the instance as the landing page describes it.
	Info func(ctx context.Context) Landing

	cache landingCache
}

func (s *Site) Register(mux *http.ServeMux) {
	login, _ := fs.Sub(loginFiles, "login")
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		page(w)
		http.ServeFileFS(w, r, login, "index.html")
	})
	mux.Handle("GET /__greengram/login/", http.StripPrefix("/__greengram/login/", static(http.FileServerFS(login))))
	pay, _ := fs.Sub(payFiles, "pay")
	mux.HandleFunc("GET /pay/stars/{token}", func(w http.ResponseWriter, r *http.Request) {
		page(w)
		// the token is the whole key: no referrer, and only this origin may
		// frame it (Web A's payment modal would)
		h := w.Header()
		h.Set("Content-Security-Policy", cspHead+"frame-src 'none'; base-uri 'self'; frame-ancestors 'self'; form-action 'self'")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "no-referrer")
		http.ServeFileFS(w, r, pay, "index.html")
	})
	mux.Handle("GET /__greengram/pay/", http.StripPrefix("/__greengram/pay/", static(http.FileServerFS(pay))))
	mux.HandleFunc("GET /fonts/roboto.woff2", s.font("KFOmCnqEu92Fr1Mu4mxKKTU1Kg"))
	mux.HandleFunc("GET /fonts/roboto-medium.woff2", s.font("KFOlCnqEu92Fr1MmEU9fBBc4AMP6lQ"))
	mux.HandleFunc("GET /stars-logo.svg", svg(starsLogo))
	mux.HandleFunc("GET /premium-logo.svg", svg(premiumLogo))
	s.registerLanding(mux)
	mux.HandleFunc("GET /admin", s.admin)
	mux.HandleFunc("GET /admin/", s.admin)
	if s.AdminDir != "" {
		mux.Handle("GET /admin-assets/", static(http.FileServer(noDirs{http.Dir(s.AdminDir)})))
	}
	// "/" without a method: "GET /" would clash with method-less routes
	// like /apiws. web() also answers unknown API paths.
	mux.HandleFunc("/", s.web)
}

func svg(data []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "image/svg+xml")
		h.Set("Cache-Control", "public, max-age=86400")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		h.Set("X-Content-Type-Options", "nosniff")
		w.Write(data)
	}
}

func page(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cache-Control", "no-store")
}

func static(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", csp)
		h.ServeHTTP(w, r)
	})
}

func (s *Site) admin(w http.ResponseWriter, r *http.Request) {
	_, _, err := s.Admin(r)
	var e *account.Error
	switch {
	case errors.As(err, &e) && e.Status == 401:
		next := "/login?next=admin"
		if v := r.URL.Query().Get("account"); v != "" {
			next += "&account=" + v
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	case err != nil:
		page(w)
		http.Error(w, "Administrator access is required.", http.StatusForbidden)
		return
	}
	page(w)
	if s.AdminDir == "" {
		// otherwise /admin would read as a link to a user called admin
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("The admin panel is not installed on this server.\nBuild it with scripts/build-admin.sh <out dir> and set GREENGRAM_ADMIN_DIR=<out dir>/admin.\nThe admin API under /api/admin works without it.\n"))
		return
	}
	http.ServeFile(w, r, filepath.Join(s.AdminDir, "index.html"))
}

var (
	reUserLink   = regexp.MustCompile(`^/u/([A-Za-z0-9_]+)(?:/([0-9]+))?/?$`)
	reChatLink   = regexp.MustCompile(`^/c/([0-9]+)(?:/([0-9]+))?/?$`)
	reInviteLink = regexp.MustCompile(`^/\+([A-Za-z0-9_-]+)/?$`)
	reRootLink   = regexp.MustCompile(`^/[A-Za-z0-9_]+(?:/[0-9]+)?/?$`)
	rePackLink   = regexp.MustCompile(`^/(addemoji|addstickers)/([A-Za-z0-9_]+)/?$`)
	reGiftLink   = regexp.MustCompile(`^/nft/([A-Za-z0-9_]+-[0-9]+)/?$`)
	reAppLink    = regexp.MustCompile(`^/u/([A-Za-z0-9_]+)/([a-z][a-z0-9_]{2,31})/?$`)
	reRootApp    = regexp.MustCompile(`^/([A-Za-z0-9_]+)/([a-z][a-z0-9_]{2,31})/?$`)
)

// notBots are first path words that mean something else to Web A or this server;
// /name/app under any other word is a bot's direct link.
var notBots = map[string]bool{"addemoji": true, "addstickers": true, "addlist": true, "login": true, "m": true, "boost": true, "nft": true, "auction": true,
	"c": true, "u": true, "share": true, "api": true, "admin": true, "assets": true}

var underU = map[string]bool{"nft": true, "addstickers": true, "addemoji": true, "c": true}

// clientLink turns a t.me-style link into the deep link Web A reads from its
// hash on start; it doesn't look at the path itself.
func clientLink(u *url.URL) string {
	// the Android app builds links from me_url_prefix (<base>/u/), so its
	// gift, pack and private post links come as /u/nft/..., /u/c/...
	if rest, ok := strings.CutPrefix(u.Path, "/u/"); ok {
		if word, _, more := strings.Cut(rest, "/"); more && underU[word] {
			u = &url.URL{Path: "/" + rest, RawQuery: u.RawQuery}
		}
	}
	var link string
	if m := reUserLink.FindStringSubmatch(u.Path); m != nil {
		if u.Query().Has("boost") {
			link = "tg://boost?domain=" + m[1]
		} else {
			link = "tg://resolve?domain=" + m[1]
			if m[2] != "" {
				link += "&post=" + m[2]
			}
			if u.RawQuery != "" {
				link += "&" + u.RawQuery
			}
		}
	} else if m := reAppLink.FindStringSubmatch(u.Path); m != nil {
		link = "tg://resolve?domain=" + m[1] + "&appname=" + m[2]
		if u.RawQuery != "" {
			link += "&" + u.RawQuery
		}
	} else if m := reChatLink.FindStringSubmatch(u.Path); m != nil {
		if u.Query().Has("boost") {
			link = "tg://boost?channel=" + m[1]
		} else if m[2] != "" {
			link = "tg://privatepost?channel=" + m[1] + "&post=" + m[2]
		}
	} else if m := reInviteLink.FindStringSubmatch(u.Path); m != nil {
		link = "tg://join?invite=" + m[1]
	} else if m := rePackLink.FindStringSubmatch(u.Path); m != nil {
		link = "tg://" + m[1] + "?set=" + m[2]
	} else if m := reGiftLink.FindStringSubmatch(u.Path); m != nil {
		link = "tg://nft?slug=" + m[1]
	}
	if link == "" {
		return ""
	}
	return "/#?tgaddr=" + url.QueryEscape(link)
}

// web serves the Web A build. Paths that aren't files get index.html, and
// t.me-style links (/u/name, /+invite, /c/1/2) go to the client as deep links.
func (s *Site) web(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"Not found."}` + "\n"))
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
		return
	}
	if s.staleWorker(w, r) {
		return
	}
	if s.guest(w, r) {
		return
	}
	if s.WebDir == "" {
		page(w)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("Greengram is running. No web client is installed (GREENGRAM_WEB_DIR).\n"))
		return
	}
	if loc := clientLink(r.URL); loc != "" {
		http.Redirect(w, r, loc, http.StatusFound)
		return
	}
	name := path.Clean("/" + r.URL.Path)
	if strings.Contains(name, "/.") || strings.HasSuffix(name, ".map") {
		http.NotFound(w, r)
		return
	}
	file := filepath.Join(s.WebDir, filepath.FromSlash(name))
	if st, err := os.Stat(file); err == nil && !st.IsDir() && name != "/index.html" {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// vite names assets by content hash; the rest may change per build
		if strings.HasPrefix(name, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else if strings.HasPrefix(name, "/img-apple-") {
			// one file per emoji, named by code point: revalidating hundreds of them
			// is what makes emoji show up late
			w.Header().Set("Cache-Control", "public, max-age=2592000")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeFile(w, r, file)
		return
	}
	if path.Ext(name) != "" && name != "/index.html" {
		http.NotFound(w, r)
		return
	}
	// /name and /name/123, the links Web A shows and copies. Last: the server's
	// own pages and the build's files have been ruled out by now.
	root := reRootLink.MatchString(r.URL.Path)
	if m := reRootApp.FindStringSubmatch(r.URL.Path); m != nil && !notBots[m[1]] {
		root = true
	}
	if root {
		u := *r.URL
		u.Path = "/u" + r.URL.Path
		if loc := clientLink(&u); loc != "" {
			http.Redirect(w, r, loc, http.StatusFound)
			return
		}
	}
	page(w)
	w.Header().Set("Content-Security-Policy", webCSP)
	w.Header().Set("X-Robots-Tag", "noindex")
	http.ServeFile(w, r, filepath.Join(s.WebDir, "index.html"))
}

// font is Web A's Roboto, which the admin panel and sign-in page borrow.
// font serves one of Web A's own Roboto files, by the name before its build hash.
func (s *Site) font(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.WebDir != "" {
			m, _ := filepath.Glob(filepath.Join(s.WebDir, "assets", name+"-*.woff2"))
			if len(m) > 0 {
				w.Header().Set("Cache-Control", "public, max-age=86400")
				http.ServeFile(w, r, m[0])
				return
			}
		}
		http.NotFound(w, r)
	}
}

// noDirs keeps http.FileServer from listing directories.
type noDirs struct{ fs http.FileSystem }

func (n noDirs) Open(name string) (http.File, error) {
	f, err := n.fs.Open(name)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err == nil && st.IsDir() {
		f.Close()
		return nil, os.ErrNotExist
	}
	return f, nil
}

// staleWorkerJS replaces a service worker from an older build: it drops
// itself and reloads its pages, so they come back through the network (and
// through any redirect, e.g. after the instance moved to a new domain).
const staleWorkerJS = `self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil((async () => {
  for (const k of await caches.keys()) await caches.delete(k);
  await self.registration.unregister();
  for (const c of await self.clients.matchAll({ type: 'window' })) c.navigate(c.url);
})()));
`

// staleWorker answers a request for a service worker file the current build
// doesn't have, which only a browser still running an older build asks for.
func (s *Site) staleWorker(w http.ResponseWriter, r *http.Request) bool {
	name := r.URL.Path
	if ok, _ := path.Match("/service.worker-*.js", name); !ok || s.WebDir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(s.WebDir, path.Base(name))); err == nil {
		return false
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(staleWorkerJS))
	return true
}
