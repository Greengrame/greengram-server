package miniapps

import (
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Greengrame/greengram-server/internal/account"
)

// Extras are the files made from captured Telegram pages that the server reads
// itself rather than serving them as they are: BotFather's page templates, the
// payment providers' logos, the Stickers emoji lists, the Bugs and Suggestions
// page with its stylesheets, fonts and pictures, and the built-in bots'
// pictures. They are not in this repository. They sit next to the captured
// apps in the mini apps bundle, under greengram/, and cmd/ggminiapps installs
// that bundle:
//
//	<dir>/greengram/botfather_pages/*.html
//	<dir>/greengram/static/<file>     served at /__greengram/<file>
//	<dir>/greengram/avatars/<kind>.jpg
//	<dir>/greengram/bugs/index.html   the /bugs page
//	<dir>/greengram/bugs/<path>       served at /__bugs/<path>
//
// Without them the server runs; the mini apps say what is missing.
const ExtrasDir = "greengram"

// MissingHelp is what an admin is told to do about a missing bundle.
const MissingHelp = "The server's admin can add it with ggminiapps install (see the README)."

var errNoPages = &account.Error{Status: http.StatusServiceUnavailable,
	Message: "BotFather's pages are not installed on this server. " + MissingHelp}

type pageSet struct {
	mu   sync.Mutex
	dir  string
	tmpl *template.Template
}

// get parses the templates the first time they are all there; until then
// every call looks again, so installing the bundle needs no restart.
func (p *pageSet) get(dir string) (*template.Template, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tmpl != nil && p.dir == dir {
		return p.tmpl, nil
	}
	if dir == "" {
		return nil, errNoPages
	}
	glob := filepath.Join(dir, ExtrasDir, "botfather_pages", "*.html")
	if m, _ := filepath.Glob(glob); len(m) == 0 {
		return nil, errNoPages
	}
	t, err := template.ParseGlob(glob)
	if err != nil {
		return nil, fmt.Errorf("botfather pages: %w", err)
	}
	p.dir, p.tmpl = dir, t
	return t, nil
}

// extraStatic reads <dir>/greengram/static/<name>. name is one path segment.
func (s *Server) extraStatic(name string) ([]byte, error) {
	if s.Dir == "" || name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(filepath.Join(s.Dir, ExtrasDir, "static", name))
}

// missingPage is what the mini app's frame shows when its files aren't on
// this server: a page, not a bare status line.
func missingPage(w http.ResponseWriter, app string) {
	title := map[string]string{"botfather": "BotFather", "stickers": "Stickers", "walt": "Wallet", "fragment": "Fragment", "bugs": "Bugs and Suggestions"}[app]
	if title == "" {
		title = "This mini app"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors *")
	w.WriteHeader(http.StatusServiceUnavailable)
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%[1]s</title>
<style>body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;font:15px/1.45 system-ui,sans-serif;background:#17212b;color:#e9eef2}main{max-width:340px;padding:24px;text-align:center}h1{font-size:18px;margin:0 0 8px}p{margin:0;color:#9aa7b3}</style></head>
<body><main><h1>%[1]s isn't available here</h1><p>This server doesn't have the mini app's files. %[2]s</p></main></body></html>`,
		template.HTMLEscapeString(title), template.HTMLEscapeString(MissingHelp))
}
