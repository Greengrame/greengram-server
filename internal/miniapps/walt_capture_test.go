package miniapps

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The capture is not in this repository. With GREENGRAM_TEST_MINIAPPS_DIR set
// to an installed mini apps bundle the patches are checked against the pinned
// files; without it that test skips. testdata/walt-modules.json is an index of
// what the adapter names, written by scripts/walt-fixtures.mjs. When these
// fail after a capture: fix walt_patches.json and the module ids, then rerun
// the script.

func waltLock(t *testing.T) Lock {
	t.Helper()
	raw, err := lockFiles.ReadFile("locks/walt.lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var l Lock
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatal(err)
	}
	return l
}

// testBundle is the mini apps bundle the tests that need captured files use.
func testBundle(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("GREENGRAM_TEST_MINIAPPS_DIR")
	if dir == "" {
		t.Skip("GREENGRAM_TEST_MINIAPPS_DIR is not set: no captured mini apps to test against")
	}
	return dir
}

func waltFixture(t *testing.T, dir, key string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "walt", filepath.FromSlash(key)))
	if err != nil {
		t.Fatalf("%s is not in the bundle: %v", key, err)
	}
	return data
}

// Every patch against the exact bytes the lock pins. A new capture changes the
// names and hashes, so it fails here until the patches have been redone.
func TestWaltPatchesFitCapture(t *testing.T) {
	dir := testBundle(t)
	l := waltLock(t)
	if len(waltPatches) == 0 {
		t.Fatal("no wallet patches")
	}
	guarded := false
	for key, p := range waltPatches {
		a, ok := l.Assets[key]
		if !ok {
			t.Errorf("%s: %s is not in the lock", p.Name, key)
			continue
		}
		data := waltFixture(t, dir, key)
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != a.SHA256 {
			t.Errorf("%s: %s in the bundle isn't the pinned file", p.Name, key)
			continue
		}
		out, changed, err := adaptWallet(key, data)
		if err != nil || !changed {
			t.Errorf("%s: %s: changed %v, %v", p.Name, key, changed, err)
			continue
		}
		if bytes.Equal(out, data) {
			t.Errorf("%s: %s came out the same", p.Name, key)
		}
		for _, e := range p.Edits {
			if e.New != "" && !bytes.Contains(out, []byte(e.New)) {
				t.Errorf("%s: %q isn't in the result", p.Name, e.New)
			}
		}
		guarded = guarded || p.Guard && bytes.Contains(out, []byte("window.GreengramWalletGate"))
	}
	if !guarded {
		t.Error("no guard patch puts window.GreengramWalletGate around the app")
	}
}

// Same scan as scripts/walt-fixtures.mjs.
var (
	waltRequireRef = regexp.MustCompile(`requireModule\((\d+)\)(?:\.([\w$]+))?`)
	waltReplaceRef = regexp.MustCompile(`requireModule\.m\[(\d+)\]`)
	waltDecorate   = regexp.MustCompile(`decorateModule\((\d+),[^{;]*\{\s*(?:let [\w$]+ = )?(?:exports|descriptors)\.([\w$]+)`)
	waltDecorateID = regexp.MustCompile(`decorateModule\((\d+),`)
	waltChunkList  = regexp.MustCompile(`\[([\d, ]+)\]\.map\(id => requireModule\.e\(id\)\)`)
	waltGlobal     = regexp.MustCompile(`window\.(Greengram\w+)`)
)

func waltStatic(t *testing.T) map[string]string {
	t.Helper()
	entries, err := staticFiles.ReadDir("static")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ".js") && (strings.HasPrefix(n, "walt-") || strings.HasPrefix(n, "wallet-")) {
			raw, err := staticFiles.ReadFile("static/" + n)
			if err != nil {
				t.Fatal(err)
			}
			out[n] = string(raw)
		}
	}
	return out
}

// decorateModule does nothing for an id that left the build, so a stale one
// loses its override without any error.
func TestWaltModuleRefs(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "walt-modules.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		Version string              `json:"version"`
		Chunks  []string            `json:"chunks"`
		Modules map[string][]string `json:"modules"`
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	if l := waltLock(t); index.Version != l.Version {
		t.Fatalf("index is for %s, the lock pins %s: rerun scripts/walt-fixtures.mjs", index.Version, l.Version)
	}
	chunks := map[string]bool{}
	for _, c := range index.Chunks {
		chunks[c] = true
	}
	check := func(file, id, name string) {
		t.Helper()
		exports, ok := index.Modules[id]
		if !ok {
			t.Errorf("%s: module %s isn't in the index", file, id)
			return
		}
		if name == "" || exports == nil {
			return // null: a module with no export table, like React
		}
		for _, e := range exports {
			if e == name {
				return
			}
		}
		t.Errorf("%s: module %s has no export %s", file, id, name)
	}
	seen := 0
	for file, text := range waltStatic(t) {
		for _, m := range waltRequireRef.FindAllStringSubmatch(text, -1) {
			check(file, m[1], m[2])
			seen++
		}
		for _, m := range waltReplaceRef.FindAllStringSubmatch(text, -1) {
			check(file, m[1], "")
			seen++
		}
		for _, m := range waltDecorate.FindAllStringSubmatch(text, -1) {
			check(file, m[1], m[2])
			seen++
		}
		for _, m := range waltDecorateID.FindAllStringSubmatch(text, -1) {
			check(file, m[1], "")
			seen++
		}
		for _, m := range waltChunkList.FindAllStringSubmatch(text, -1) {
			for _, id := range strings.Split(m[1], ",") {
				if id = strings.TrimSpace(id); !chunks[id] {
					t.Errorf("%s: chunk %s isn't in the index", file, id)
				}
			}
		}
	}
	if seen < 100 {
		t.Errorf("only %d module references: the scan no longer matches how the adapter is written", seen)
	}
}

// A patch that calls a global nothing defines would break the page it fixes.
func TestWaltPatchGlobalsDefined(t *testing.T) {
	var defined strings.Builder
	for _, text := range waltStatic(t) {
		defined.WriteString(text)
	}
	for _, p := range waltPatches {
		for _, e := range p.Edits {
			for _, m := range waltGlobal.FindAllStringSubmatch(e.New, -1) {
				if !strings.Contains(defined.String(), "window."+m[1]+" = ") {
					t.Errorf("%s: nothing sets window.%s", p.Name, m[1])
				}
			}
		}
	}
}

// The adapter imports these by role; a renamed entry would stop it loading.
func TestWaltEntriesInLock(t *testing.T) {
	l := waltLock(t)
	for _, role := range waltEntries {
		found := false
		for key := range l.Assets {
			found = found || strings.HasPrefix(key, "static/js/"+role+".") && strings.HasSuffix(key, ".js")
		}
		if !found {
			t.Errorf("no %s entry in the lock", role)
		}
	}
}
