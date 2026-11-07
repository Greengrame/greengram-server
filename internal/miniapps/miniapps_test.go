package miniapps

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocksLoad(t *testing.T) {
	locks, err := LoadLocks()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range Apps {
		if _, ok := locks[name].Assets["index.html"]; !ok {
			t.Errorf("%s lock has no index.html", name)
		}
	}
}

func pin(data string) Asset {
	sum := sha256.Sum256([]byte(data))
	return Asset{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
}

// serve sets up a botfather app with the given files on disk and pins.
func serve(t *testing.T, disk map[string]string, pins map[string]string) *http.ServeMux {
	t.Helper()
	dir := t.TempDir()
	for k, v := range disk {
		p := filepath.Join(dir, "botfather", filepath.FromSlash(k))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(v), 0o644)
	}
	l := Lock{Name: "botfather", Assets: map[string]Asset{}}
	for k, v := range pins {
		l.Assets[k] = pin(v)
	}
	s := &Server{Dir: dir}
	s.once.Do(func() { s.locks = map[string]Lock{"botfather": l} })
	mux := http.NewServeMux()
	s.Register(mux)
	return mux
}

func get(mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func TestPinnedFiles(t *testing.T) {
	index := `<html><head><title>BotFather</title><script>alert(1)</script><link href="/css/a.css?1" rel="stylesheet">` +
		`<link href="//telegram.org/css/b.css" rel="stylesheet"></head><body><script src="/js/app.js"></script></body></html>`
	css := `body{background:url(/img/bg.png)}`
	files := map[string]string{"index.html": index, "css/a.css": css, "js/app.js": "1", "_external/telegram.org/css/b.css": ""}
	mux := serve(t, files, files)

	w := get(mux, "/botfather")
	body := w.Body.String()
	if w.Code != 200 || strings.Contains(body, "alert(1)") || !strings.Contains(body, `src="/__miniapps/botfather/js/app.js"`) ||
		!strings.Contains(body, `href="/__miniapps/botfather/css/a.css?1"`) || !strings.Contains(body, `/__miniapps/botfather/_external/telegram.org/css/b.css`) ||
		!strings.Contains(body, `id="greengram-miniapp-config"`) {
		t.Fatalf("document %d %s", w.Code, body)
	}
	if w := get(mux, "/botfather/bot/1/settings"); w.Code != 200 {
		t.Fatalf("subpage %d", w.Code)
	}
	if w := get(mux, "/__miniapps/botfather/css/a.css"); w.Code != 200 || w.Body.String() != `body{background:url(/__miniapps/botfather/img/bg.png)}` {
		t.Fatalf("css %d %s", w.Code, w.Body.String())
	}
	if w := get(mux, "/__miniapps/botfather/css/b.css"); w.Code != 200 {
		t.Fatalf("telegram.org fallback %d", w.Code)
	}
	for _, p := range []string{"/__miniapps/botfather/index.html", "/__miniapps/botfather/js/missing.js", "/__miniapps/botfather/.git/config", "/__miniapps/nope/js/app.js"} {
		if w := get(mux, p); w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}

func TestChangedFileRefused(t *testing.T) {
	mux := serve(t, map[string]string{"index.html": "<html><head></head></html>", "js/app.js": "evil"},
		map[string]string{"index.html": "<html><head></head><body></body></html>", "js/app.js": "good"})
	if w := get(mux, "/botfather"); w.Code != 503 {
		t.Fatalf("changed document: %d", w.Code)
	}
	if w := get(mux, "/__miniapps/botfather/js/app.js"); w.Code != 503 {
		t.Fatalf("changed asset: %d", w.Code)
	}
}
