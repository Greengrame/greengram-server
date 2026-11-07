package miniapps

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
)

var (
	inlineScript = regexp.MustCompile(`(?is)<script(?:\s[^>]*)?>.*?</script>`)
	srcScript    = regexp.MustCompile(`(?i)\bsrc=`)
	moduleScript = regexp.MustCompile(`(?is)<script[^>]*type="module"[^>]*>.*?</script>`)
	waltChunkSrc = regexp.MustCompile(`src="/(static/js/[\w.-]+\.js)"`)
	telegramOrg  = regexp.MustCompile(`((?:src|href)=["'])(?:https:)?//telegram\.org/`)
	rootAsset    = regexp.MustCompile(`((?:src|href)=["'])/(css|js|img|fonts|static|public)/`)
	headOpen     = regexp.MustCompile(`(?i)<head[^>]*>`)
)

// kinds maps an app to the built-in bot whose launch data it gets.
var kinds = map[string]string{"botfather": bots.BotFather, "stickers": bots.Stickers, "walt": bots.Wallet, "fragment": bots.Fragment}

// waltEntries are the bundles walt-adapter.js imports itself, in order.
var waltEntries = []string{"runtime", "react", "openapi", "core", "earlyLangpack", "vendors", "app", "init", "main"}

// sharedWaltChunks are the numbered bundles the page loads next to the named
// ones, in page order. Which code they hold, and how many there are, changes
// with every build, and the named bundles can't run without them.
func sharedWaltChunks(html []byte) []string {
	out := []string{}
	for _, tag := range moduleScript.FindAll(html, -1) {
		m := waltChunkSrc.FindSubmatch(tag)
		if m == nil {
			continue
		}
		named := false
		for _, role := range waltEntries {
			named = named || bytes.HasPrefix(m[1], []byte("static/js/"+role+"."))
		}
		if !named {
			out = append(out, string(m[1]))
		}
	}
	return out
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(staticFiles, "static/"+name)
	if err != nil {
		data, err = s.extraStatic(name)
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(name)))
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", assetCSP)
	w.Write(data)
}

func insert(html []byte, before string, add string) []byte {
	i := bytes.LastIndex(html, []byte(before))
	if i < 0 {
		return append(html, add...)
	}
	return append(html[:i:i], append([]byte(add), html[i:]...)...)
}

// document serves an app's index.html, pinned like the assets, with
// greengram-web's runtime and the app's adapter added, the way its
// miniapps.js document() did. Inline scripts go (the CSP forbids them)
// and links are pointed at /__miniapps/. Who is using the app is only
// known from the launch data in the URL fragment, so the document is
// the same for everyone; pages that differ per person are asked for
// again with X-Greengram-Page and come back as JSON (page.go).
func (s *Server) document(w http.ResponseWriter, r *http.Request) {
	top := "/" + strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)[0]
	name := pages[top]
	if r.Header.Get("X-Greengram-Page") != "" {
		s.page(w, r, name)
		return
	}
	l, ok := s.lock(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	a, ok := l.Assets["index.html"]
	if !ok {
		missingPage(w, name)
		return
	}
	data, err := s.read(name, "index.html", a)
	if err != nil {
		slog.Warn("miniapps: document", "err", err)
		missingPage(w, name)
		return
	}
	html := data
	var waltShared []string
	if name == "walt" {
		waltShared = sharedWaltChunks(html)
		html = moduleScript.ReplaceAll(html, nil)
	}
	html = inlineScript.ReplaceAllFunc(html, func(tag []byte) []byte {
		open := tag[:bytes.IndexByte(tag, '>')]
		if srcScript.Match(open) && name != "fragment" {
			return tag
		}
		return nil
	})
	html = telegramOrg.ReplaceAll(html, []byte("${1}/__miniapps/"+name+"/_external/telegram.org/"))
	html = rootAsset.ReplaceAll(html, []byte("${1}/__miniapps/"+name+"/${2}/"))
	cfg := map[string]any{"app": name, "commit": l.Commit, "version": l.Version, "originalPath": r.URL.Path, "api": "/__miniapps/" + name + "/api"}
	if name == "walt" {
		keys := make([]string, 0, len(l.Assets))
		for k := range l.Assets {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		entries := map[string]string{}
		for _, role := range waltEntries {
			for _, k := range keys {
				if strings.HasPrefix(k, "static/js/"+role+".") && strings.HasSuffix(k, ".js") {
					entries[role] = k
					break
				}
			}
		}
		cfg["entries"] = entries
		cfg["shared"] = waltShared
	}
	raw, _ := json.Marshal(cfg) // escapes <, so it can't close the script
	boot := `<script type="application/json" id="greengram-miniapp-config">` + string(raw) + `</script><script src="/__greengram/miniapp-runtime.js"></script>`
	if loc := headOpen.FindIndex(html); loc != nil {
		html = append(html[:loc[1]:loc[1]], append([]byte(boot), html[loc[1]:]...)...)
	}
	switch name {
	case "walt":
		html = insert(html, "</body>", `<script type="module" src="/__greengram/walt-adapter.js"></script>`)
	case "fragment":
		if html, err = fragmentShell(html); err != nil {
			slog.Warn("miniapps: fragment", "err", err)
			unavailable(w, "Mini App files are unavailable.")
			return
		}
		html = insert(html, "</body>", `<script src="/__greengram/fragment-adapter.js"></script>`)
	default:
		html = insert(html, "</head>", `<link rel="stylesheet" href="/__greengram/`+name+`-adapter.css">`)
		html = insert(html, "</body>", `<script src="/__greengram/`+name+`-adapter.js"></script>`)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", documentCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(html)
}

// pageView is what a per-person page sends the adapter: the markup for
// #aj_content, the page state the captured scripts start from, and for
// Fragment the popups and config fragment.js reads.
type pageView struct {
	Title  string         `json:"title"`
	Markup string         `json:"markup"`
	State  map[string]any `json:"state,omitempty"`
	Extra  string         `json:"extra,omitempty"`
	Config map[string]any `json:"config,omitempty"`
}

func (s *Server) page(w http.ResponseWriter, r *http.Request, name string) {
	ctx := r.Context()
	if name == "fragment" && r.Header.Get("X-Telegram-Init-Data") == "" {
		// opened as a plain page (Web A's "Learn more"), not launched
		s.fragmentDirect(w, r)
		return
	}
	l, err := s.Bots.Launched(ctx, kinds[name], r.Header.Get("X-Telegram-Init-Data"))
	if err != nil {
		fail(w, 401, "Session expired. Restart the app to continue.")
		return
	}
	u, ok, err := s.Accounts.User(ctx, l.User)
	if err == nil && (!ok || u.Disabled || u.IsSystem || u.IsBot) {
		fail(w, 401, "Session expired. Restart the app to continue.")
		return
	}
	var v pageView
	if err == nil {
		switch name {
		case "stickers":
			v, err = s.stickerPage(r, u)
		case "botfather":
			v, err = s.botfatherPage(r, u)
		case "fragment":
			v, err = s.fragmentPage(r, u, 1)
		default:
			http.NotFound(w, r)
			return
		}
	}
	var refusal *account.Error
	switch {
	case errors.As(err, &refusal):
		fail(w, refusal.Status, refusal.Message)
	case err != nil:
		slog.Error("miniapps: page "+r.URL.Path, "err", err)
		fail(w, 500, "Something went wrong. Try again.")
	default:
		reply(w, 200, v)
	}
}
