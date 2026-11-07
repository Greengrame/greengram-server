package miniapps

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serveApp is serve() for any app name.
func serveApp(t *testing.T, name string, files map[string]string) *http.ServeMux {
	t.Helper()
	dir := t.TempDir()
	l := Lock{Name: name, Version: "1.2", Commit: "abc", Assets: map[string]Asset{}}
	for k, v := range files {
		p := filepath.Join(dir, name, filepath.FromSlash(k))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(v), 0o644)
		l.Assets[k] = pin(v)
	}
	s := &Server{Dir: dir}
	s.once.Do(func() { s.locks = map[string]Lock{name: l} })
	mux := http.NewServeMux()
	s.Register(mux)
	return mux
}

func TestDocumentLoadsAdapter(t *testing.T) {
	index := `<html><head><title>Stickers</title></head><body><div id="aj_content">Session expired.</div><script src="/js/bot-stickers.js"></script><script>ajInit({})</script></body></html>`
	mux := serveApp(t, "stickers", map[string]string{"index.html": index, "js/bot-stickers.js": "1"})
	body := get(mux, "/stickers/pack/5").Body.String()
	head, rest, _ := strings.Cut(body, "</head>")
	if !strings.Contains(head, `id="greengram-miniapp-config"`) || !strings.Contains(head, `<script src="/__greengram/miniapp-runtime.js"></script>`) ||
		!strings.Contains(head, `href="/__greengram/stickers-adapter.css"`) {
		t.Fatalf("head: %s", head)
	}
	// the adapter runs after the captured scripts it drives
	if i, j := strings.Index(rest, "/__miniapps/stickers/js/bot-stickers.js"), strings.Index(rest, "/__greengram/stickers-adapter.js"); i < 0 || j < i || strings.Contains(rest, "ajInit") {
		t.Fatalf("body: %s", rest)
	}
}

func TestWalletDocument(t *testing.T) {
	index := `<html><head><script>window.__WALT__={}</script><script defer src="/public/js/telegram-web-app.js"></script>` +
		`<script type="module" src="/static/js/runtime.aa.js"></script><script type="module" src="/static/js/5.cc.js"></script><script type="module" src="/static/js/main.bb.js"></script></head><body><div id="root"></div></body></html>`
	mux := serveApp(t, "walt", map[string]string{"index.html": index, "static/js/runtime.aa.js": "r", "static/js/main.bb.js": "m", "public/js/telegram-web-app.js": "t"})
	body := get(mux, "/wallet/main").Body.String()
	if strings.Contains(body, `type="module" src="/__miniapps`) || strings.Contains(body, "__WALT__={}") || !strings.Contains(body, `<script type="module" src="/__greengram/walt-adapter.js"></script>`) ||
		!strings.Contains(body, `"entries":{"main":"static/js/main.bb.js","runtime":"static/js/runtime.aa.js"}`) || !strings.Contains(body, `"shared":["static/js/5.cc.js"]`) || !strings.Contains(body, "/__miniapps/walt/public/js/telegram-web-app.js") {
		t.Fatalf("document: %s", body)
	}
	// webpack asks for chunks at the site root
	if w := get(mux, "/static/js/main.bb.js"); w.Code != 200 || w.Body.String() != "m" {
		t.Fatalf("root chunk: %d %s", w.Code, w.Body.String())
	}
	if w := get(mux, "/static/js/other.js"); w.Code != 404 {
		t.Fatalf("unpinned root chunk: %d", w.Code)
	}
}

func TestStaticFiles(t *testing.T) {
	mux := serveApp(t, "botfather", map[string]string{"index.html": "<html></html>"})
	for _, name := range []string{"miniapp-runtime.js", "stickers-adapter.js", "botfather-adapter.js", "walt-adapter.js", "walt-transfers.js", "wallet-recipient-request.js", "fragment.js", "fragment-adapter.js"} {
		w := get(mux, "/__greengram/"+name)
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") {
			t.Errorf("%s: %d %q", name, w.Code, w.Header().Get("Content-Type"))
		}
	}
	if w := get(mux, "/__greengram/stickers-adapter.css"); w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/css") {
		t.Errorf("css: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	for _, p := range []string{"/__greengram/nope.js", "/__greengram/static", "/__greengram/.hidden"} {
		if w := get(mux, p); w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}

// tryPatch adds a patch for the length of a test.
func tryPatch(t *testing.T, p waltPatch) {
	t.Helper()
	waltPatches[p.File] = p
	t.Cleanup(func() { delete(waltPatches, p.File) })
}

func TestAdaptWallet(t *testing.T) {
	const key = "static/js/1.aa.js"
	tryPatch(t, waltPatch{Name: "fix", File: key, Edits: []waltEdit{{Old: "a;", New: "b;"}, {Old: "c", New: "d", Count: 2}}})
	out, changed, err := adaptWallet(key, []byte("a;cc"))
	if err != nil || !changed || string(out) != "b;dd" {
		t.Fatalf("patch: %v %v %s", err, changed, out)
	}
	// a plain fix that lost its anchor serves the original
	for _, data := range []string{"nothing here", "a;c", "a;a;cc"} {
		if out, changed, err := adaptWallet(key, []byte(data)); err != nil || changed || string(out) != data {
			t.Fatalf("missing anchor in %q: %v %v %s", data, err, changed, out)
		}
	}
	if out, changed, err := adaptWallet("static/js/2.bb.js", []byte("a;cc")); err != nil || changed || string(out) != "a;cc" {
		t.Fatalf("patched an unrelated chunk: %v %v %s", err, changed, out)
	}
}

// The client side of the passcode can't go missing quietly.
func TestAdaptWalletGuard(t *testing.T) {
	const key = "static/js/1.aa.js"
	tryPatch(t, waltPatch{Name: "gate", File: key, Guard: true, Edits: []waltEdit{{Old: "root", New: "gate(root)"}}})
	if out, changed, err := adaptWallet(key, []byte("root")); err != nil || !changed || string(out) != "gate(root)" {
		t.Fatalf("patch: %v %v %s", err, changed, out)
	}
	if _, _, err := adaptWallet(key, []byte("moved")); err == nil {
		t.Fatal("a guard patch missed its anchor without an error")
	}
	mux := serveApp(t, "walt", map[string]string{"index.html": "<html></html>", key: "moved"})
	if w := get(mux, "/"+key); w.Code != 503 || strings.Contains(w.Body.String(), "moved") {
		t.Fatalf("served without its guard: %d %s", w.Code, w.Body.String())
	}
}

func TestWaltPatchesFit(t *testing.T) {
	if err := waltPatchesFit(Lock{Version: "1", Assets: map[string]Asset{}}); err == nil {
		t.Fatal("patches fit an empty lock")
	}
	locks, err := LoadLocks()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := locks["walt"]; !ok {
		t.Fatal("the pinned wallet doesn't fit walt_patches.json: rerun scripts/walt-fixtures.mjs after a capture")
	}
}

func TestStickerMediaNeedsSignature(t *testing.T) {
	mux := serveApp(t, "stickers", map[string]string{"index.html": "<html></html>"})
	link := stickerURL(42)
	if !strings.HasPrefix(link, "/__miniapps/stickers/media/42?s=") {
		t.Fatalf("link %s", link)
	}
	for _, p := range []string{"/__miniapps/stickers/media/42", "/__miniapps/stickers/media/42?s=00", "/__miniapps/stickers/media/43" + link[len("/__miniapps/stickers/media/42"):]} {
		if w := get(mux, p); w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}

// Files made from captured pages come from the bundle's greengram/static.
func TestStaticFromBundle(t *testing.T) {
	dir := t.TempDir()
	s := &Server{Dir: dir}
	mux := http.NewServeMux()
	s.Register(mux)
	if w := get(mux, "/__greengram/stickers-emoji.json"); w.Code != 404 {
		t.Fatalf("without the bundle: %d", w.Code)
	}
	os.MkdirAll(filepath.Join(dir, ExtrasDir, "static"), 0o755)
	os.WriteFile(filepath.Join(dir, ExtrasDir, "static", "stickers-emoji.json"), []byte(`[]`), 0o644)
	os.WriteFile(filepath.Join(dir, ExtrasDir, "secret.txt"), []byte(`no`), 0o644)
	if w := get(mux, "/__greengram/stickers-emoji.json"); w.Code != 200 || w.Body.String() != "[]" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("from the bundle: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	for _, p := range []string{"/__greengram/..%2Fsecret.txt", "/__greengram/.hidden"} {
		if w := get(mux, p); w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}

// A mini app with no files on the server says so in its frame.
func TestMissingAppPage(t *testing.T) {
	mux := serveApp(t, "fragment", map[string]string{})
	w := get(mux, "/fragment")
	if w.Code != 503 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") || !strings.Contains(w.Body.String(), "ggminiapps install") {
		t.Fatalf("%d %q %s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
}
