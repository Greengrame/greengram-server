// Package miniapps serves the mini apps the built-in bots open: the
// BotFather, Stickers, Wallet and Fragment pages captured from Telegram.
// The files themselves are not in this repository; they come in the mini
// apps bundle (GREENGRAM_MINIAPPS_DIR, installed by cmd/ggminiapps). The
// lock files pinning every one of them by sha256 are built in, and nothing
// is served that doesn't match.
package miniapps

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/fragment"
	"github.com/Greengrame/greengram-server/internal/media"
)

//go:embed locks/*.lock.json
var lockFiles embed.FS

// static is greengram-web's frontend/ runtime and adapters, served at
// /__greengram/: they hook the captured pages up to this server. Files made
// from the captured pages come from the bundle instead (bundle.go).
//
//go:embed static
var staticFiles embed.FS

// Asset is one pinned file; URL is where greengram-web fetched it from.
type Asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Lock struct {
	Name    string           `json:"name"`
	Origin  string           `json:"origin"`
	Version string           `json:"version"`
	Commit  string           `json:"commit"`
	Assets  map[string]Asset `json:"assets"`
}

// Apps by name, and the paths their documents answer on.
var (
	Apps  = []string{"botfather", "stickers", "walt", "fragment"}
	pages = map[string]string{"/botfather": "botfather", "/stickers": "stickers", "/wallet": "walt", "/fragment": "fragment"}
)

// LoadLocks reads the built-in lock files, by app.
func LoadLocks() (map[string]Lock, error) {
	out := map[string]Lock{}
	for _, name := range Apps {
		raw, err := lockFiles.ReadFile("locks/" + name + ".lock.json")
		if err != nil {
			return nil, err
		}
		var l Lock
		if err := json.Unmarshal(raw, &l); err != nil {
			return nil, fmt.Errorf("%s lock: %w", name, err)
		}
		if name == "walt" {
			// the wallet would open without its passcode gate
			if err := waltPatchesFit(l); err != nil {
				slog.Error("miniapps: wallet lock and patches differ", "err", err)
				continue
			}
		}
		out[name] = l
	}
	return out, nil
}

type Server struct {
	Dir      string // the mini apps bundle: <Dir>/<app>/<path>, <Dir>/greengram/
	Bots     *bots.Service
	Accounts *account.Service
	Fragment *fragment.Service
	Media    *media.Service

	once  sync.Once
	locks map[string]Lock
	err   error

	mu       sync.Mutex
	verified map[string]string // app/path -> stamp of the file that matched

	pages pageSet
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /bugs", s.bugsDocument)
	mux.HandleFunc("GET /bugs/c/{id}", s.bugsDocument)
	mux.HandleFunc("GET /bugs/files/{id}", s.bugsFile)
	mux.HandleFunc("GET /bugs/api", s.bugsAPI)
	mux.HandleFunc("POST /bugs/api", s.bugsAPI)
	mux.HandleFunc("GET /__bugs/{path...}", s.bugsAsset)
	mux.HandleFunc("GET /__miniapps/{app}/{path...}", s.asset)
	mux.HandleFunc("GET /__greengram/{file}", s.static)
	// Wallet's webpack loads chunks and images from the site root
	mux.HandleFunc("GET /static/{path...}", s.waltRoot)
	mux.HandleFunc("GET /public/{path...}", s.waltRoot)
	mux.HandleFunc("GET /__miniapps/walt/coin/{code}", s.coinLogo)
	mux.HandleFunc("POST /__miniapps/botfather/api", s.botfatherAPI)
	mux.HandleFunc("POST /__miniapps/botfather/upload", s.botfatherUpload)
	mux.HandleFunc("GET /__miniapps/botfather/photo/{photo}", s.botfatherPhoto)
	mux.HandleFunc("POST /__miniapps/stickers/api", s.stickersAPI)
	mux.HandleFunc("POST /__miniapps/stickers/upload", s.stickerUpload)
	mux.HandleFunc("POST /__miniapps/stickers/import", s.stickerZip)
	mux.HandleFunc("GET /__miniapps/stickers/media/{doc}", s.stickerMedia)
	for p := range pages {
		mux.HandleFunc("GET "+p, s.document)
		mux.HandleFunc("GET "+p+"/{rest...}", s.document)
	}
}

func (s *Server) lock(name string) (Lock, bool) {
	s.once.Do(func() { s.locks, s.err = LoadLocks() })
	if s.err != nil {
		slog.Error("miniapps: locks", "err", s.err)
		return Lock{}, false
	}
	l, ok := s.locks[name]
	return l, ok
}

func unavailable(w http.ResponseWriter, msg string) {
	http.Error(w, msg, http.StatusServiceUnavailable)
}

// read returns a pinned file if it is on disk and matches its lock entry.
// A file is hashed once per change of its size and mtime.
func (s *Server) read(name, key string, a Asset) ([]byte, error) {
	file := filepath.Join(s.Dir, name, filepath.FromSlash(key))
	st, err := os.Stat(file)
	if err != nil {
		return nil, fmt.Errorf("missing %s asset %s: install the mini apps bundle with ggminiapps (GREENGRAM_MINIAPPS_DIR)", name, key)
	}
	stamp := fmt.Sprintf("%d:%d", st.Size(), st.ModTime().UnixNano())
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	ok := s.verified[name+"/"+key] == stamp
	s.mu.Unlock()
	if ok {
		return data, nil
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != a.SHA256 {
		return nil, fmt.Errorf("a pinned %s asset changed: %s (ggminiapps status lists them)", name, key)
	}
	s.mu.Lock()
	if s.verified == nil {
		s.verified = map[string]string{}
	}
	s.verified[name+"/"+key] = stamp
	s.mu.Unlock()
	return data, nil
}

func validKey(key string) bool {
	if key == "" || strings.HasSuffix(key, ".map") {
		return false
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

const assetCSP = "default-src 'self'; connect-src 'self' data: blob:; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; worker-src 'self' blob:; object-src 'none'"

// cssRoot points absolute url(/...) references in CSS back under the app.
var cssRoot = regexp.MustCompile(`url\((['"]?)/([^/])`)

func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	s.serveAsset(w, r, r.PathValue("app"), r.PathValue("path"))
}

func (s *Server) waltRoot(w http.ResponseWriter, r *http.Request) {
	s.serveAsset(w, r, "walt", strings.TrimPrefix(r.URL.Path, "/"))
}

// coinLogo is the Wallet's own logo of an asset (Stars top-up page): the
// chunk its webpack loads for it, an SVG component in a module. Gram is TON.
func (s *Server) coinLogo(w http.ResponseWriter, r *http.Request) {
	code := strings.ToUpper(r.PathValue("code"))
	if code == "TON" {
		code = "GRAM"
	}
	l, ok := s.lock("walt")
	if !ok || !coinCode.MatchString(code) {
		http.NotFound(w, r)
		return
	}
	key := ""
	for k := range l.Assets {
		name, ok := strings.CutPrefix(k, "static/js/currency-logo-")
		if !ok {
			continue
		}
		name, _, _ = strings.Cut(name, ".")
		// AAPLx is "AAPLX" there; a lower-case twin is some other chunk
		if name == code || key == "" && strings.EqualFold(name, code) {
			key = k
		}
	}
	if key == "" {
		http.NotFound(w, r)
		return
	}
	s.serveAsset(w, r, "walt", key)
}

var coinCode = regexp.MustCompile(`^[A-Z0-9]{1,16}$`)

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, name, key string) {
	l, ok := s.lock(name)
	if !ok || !validKey(key) {
		http.NotFound(w, r)
		return
	}
	found := ""
	for _, k := range []string{key, "_external/telegram.org/" + key} {
		if _, ok := l.Assets[k]; ok {
			found = k
			break
		}
	}
	if found == "" || strings.HasSuffix(found, ".html") {
		http.NotFound(w, r)
		return
	}
	data, err := s.read(name, found, l.Assets[found])
	if err != nil {
		slog.Warn("miniapps: asset", "err", err)
		unavailable(w, "Mini App files are unavailable.")
		return
	}
	cache := "public, max-age=31536000, immutable"
	if name == "walt" {
		patched, changed, err := adaptWallet(found, data)
		if err != nil {
			unavailable(w, "Mini App files are unavailable.")
			return
		}
		if changed {
			data, cache = patched, "no-cache"
		}
	} else if strings.HasSuffix(found, ".css") {
		data = cssRoot.ReplaceAll(data, []byte(`url($1/__miniapps/`+name+`/$2`))
		cache = "no-cache"
	}
	ct := mime.TypeByExtension(path.Ext(found))
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", cache)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", assetCSP)
	w.Write(data)
}

const documentCSP = "default-src 'self'; connect-src 'self' data: blob:; script-src 'self' 'wasm-unsafe-eval'; worker-src 'self' blob:; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; media-src 'self' blob: data:; frame-src 'self'; frame-ancestors *; object-src 'none'; base-uri 'none'; form-action 'self'"
