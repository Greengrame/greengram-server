package miniapps

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Without the bundle the page says it isn't installed; the script and the
// API don't depend on it.
func TestBugsPlatformWithoutBundle(t *testing.T) {
	var mux = http.NewServeMux()
	var server = &Server{Dir: t.TempDir()}
	var response *httptest.ResponseRecorder
	server.Register(mux)
	response = get(mux, "/bugs")
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "Bugs and Suggestions isn't available here") {
		t.Fatalf("document without the bundle: %d %s", response.Code, response.Body.String())
	}
	response = get(mux, "/__bugs/platform.js")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "bugs-cards") {
		t.Fatalf("platform.js: %d", response.Code)
	}
	if response = get(mux, "/__bugs/css/bugtracker.css"); response.Code != 404 {
		t.Fatalf("stylesheet without the bundle: %d", response.Code)
	}
}

func TestBugsPlatformServesBundle(t *testing.T) {
	var dir = t.TempDir()
	var mux = http.NewServeMux()
	var server = &Server{Dir: dir}
	var response *httptest.ResponseRecorder
	var bugs = filepath.Join(dir, ExtrasDir, "bugs")
	var p string
	if err := os.MkdirAll(filepath.Join(bugs, "css"), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, body := range map[string]string{"index.html": `<div class="bt-container"></div>`, "css/bugtracker.css": "body{}", "css/.hidden": "x"} {
		if err := os.WriteFile(filepath.Join(bugs, filepath.FromSlash(p)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ExtrasDir, "secret.css"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	server.Register(mux)
	for _, p = range []string{"/bugs", "/bugs/c/42"} {
		if response = get(mux, p); response.Code != 200 || !strings.Contains(response.Body.String(), "bt-container") {
			t.Fatalf("%s: %d", p, response.Code)
		}
	}
	if response = get(mux, "/__bugs/css/bugtracker.css"); response.Code != 200 || response.Body.String() != "body{}" {
		t.Fatalf("stylesheet: %d", response.Code)
	}
	for _, p = range []string{"/__bugs/index.html", "/__bugs/css/.hidden", "/__bugs/../secret.css", "/__bugs/css//bugtracker.css"} {
		if response = get(mux, p); response.Code == 200 {
			t.Fatalf("%s served", p)
		}
	}
}

// TestBugsPlatformOriginalAssets checks an installed bundle's Bugs page
// against the capture's manifest.
func TestBugsPlatformOriginalAssets(t *testing.T) {
	var dir = os.Getenv("GREENGRAM_TEST_MINIAPPS_DIR")
	if dir == "" {
		t.Skip("GREENGRAM_TEST_MINIAPPS_DIR is not set: no captured Bugs page to test against")
	}
	var mux = http.NewServeMux()
	var server = &Server{Dir: dir}
	var response *httptest.ResponseRecorder
	var raw []byte
	var err error
	var manifest struct {
		Assets map[string]struct {
			SHA256 string `json:"sha256"`
		} `json:"assets"`
	}
	var sum [32]byte
	var document string
	server.Register(mux)
	response = get(mux, "/bugs")
	if response.Code != 200 {
		t.Fatalf("document: %d", response.Code)
	}
	document = response.Body.String()
	if !strings.Contains(document, "bt-container") || !strings.Contains(document, "/__bugs/css/bugtracker.css") || !strings.Contains(document, "bugs-card-template") {
		t.Fatal("the original page layout is missing")
	}
	if strings.Contains(document, "Telegram.Login") || strings.Contains(document, "telegram-login.js") || strings.Contains(document, "<style") {
		t.Fatal("external login or custom styles in the platform")
	}
	raw, err = os.ReadFile(filepath.Join(dir, ExtrasDir, "bugs", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Assets) < 100 {
		t.Fatal("original assets are missing")
	}
	for key, entry := range manifest.Assets {
		response = get(mux, "/__bugs/"+key)
		if response.Code != 200 {
			t.Fatalf("%s: %d", key, response.Code)
		}
		sum = sha256.Sum256(response.Body.Bytes())
		if hex.EncodeToString(sum[:]) != entry.SHA256 {
			t.Fatalf("asset differs from the capture: %s", key)
		}
	}
	response = get(mux, "/bugs/c/42")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "layer-popup-container") {
		t.Fatal("card permalinks do not serve the platform")
	}
}

func TestBugsPlatformRequiresAuthenticationForWrites(t *testing.T) {
	var mux = http.NewServeMux()
	var server = &Server{}
	var request = httptest.NewRequest(http.MethodPost, "/bugs/api", strings.NewReader(`{"action":"create","kind":"bug","title":"Example","text":"Example"}`))
	var response = httptest.NewRecorder()
	server.Register(mux)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Greengram-Bugs", "1")
	mux.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatalf("unauthenticated write: %d %s", response.Code, response.Body.String())
	}
}

func TestBugsAttachmentsRejectInvalidUploads(t *testing.T) {
	var server = &Server{}
	var request = httptest.NewRequest(http.MethodPost, "/bugs/api", nil)
	var cases = [][]bugsAttachment{
		{{Name: "empty.txt"}},
		{{Name: "large.bin", Data: make([]byte, (1<<20)+1)}},
		{{Name: "bad\r\nname.txt", Data: []byte("test")}},
		make([]bugsAttachment, 6),
	}
	var files []bugsAttachment
	var err error
	for _, files = range cases {
		_, err = server.prepareBugUploads(request, 1, files)
		if err == nil {
			t.Fatal("invalid attachments reached the media service")
		}
	}
}
