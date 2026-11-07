package miniapps

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
)

// Wallet chunks patched on the way out, for screens this instance drives
// differently. The anchors only fit the exact pinned file, so a new capture
// has to go through walt_patches.json again: see TestWaltPatchesFitCapture.

//go:embed walt_patches.json
var waltPatchData []byte

type waltEdit struct {
	Old   string `json:"old"`
	New   string `json:"new"`
	Count int    `json:"count"` // how often Old occurs; 0 means once
}

type waltPatch struct {
	Name string `json:"name"`
	File string `json:"file"`
	Why  string `json:"why"`
	// Guard patches carry the client side of a safety rule, the passcode
	// gate. Serving their file unpatched would lose the rule without a trace,
	// so a miss takes the wallet down instead.
	Guard bool       `json:"guard"`
	Edits []waltEdit `json:"edits"`
}

var waltPatches = mustWaltPatches()

func mustWaltPatches() map[string]waltPatch {
	var list []waltPatch
	if err := json.Unmarshal(waltPatchData, &list); err != nil {
		panic("miniapps: walt_patches.json: " + err.Error())
	}
	out := make(map[string]waltPatch, len(list))
	for _, p := range list {
		out[p.File] = p
	}
	return out
}

// apply returns the patched file, or the first edit whose anchor didn't fit.
func (p waltPatch) apply(data []byte) ([]byte, error) {
	for _, e := range p.Edits {
		want := e.Count
		if want == 0 {
			want = 1
		}
		if got := bytes.Count(data, []byte(e.Old)); got != want {
			return nil, fmt.Errorf("%s: %s: %d matches of %q, want %d", p.File, p.Name, got, e.Old, want)
		}
		data = bytes.ReplaceAll(data, []byte(e.Old), []byte(e.New))
	}
	return data, nil
}

// waltPatchesFit reports a patch whose file isn't in the lock: nothing would
// ever ask to patch it, so no anchor check would run either.
func waltPatchesFit(l Lock) error {
	for key, p := range waltPatches {
		if _, ok := l.Assets[key]; !ok {
			return fmt.Errorf("%s: %s is not in the wallet lock %s", p.Name, key, l.Version)
		}
	}
	return nil
}

// adaptWallet patches one wallet file. changed says it differs from the
// original; a missed anchor on a guard patch is an error, and on the others
// serves the original, because only the small fix is lost.
func adaptWallet(key string, data []byte) (out []byte, changed bool, err error) {
	p, ok := waltPatches[key]
	if !ok {
		return data, false, nil
	}
	out, err = p.apply(data)
	if err == nil {
		return out, true, nil
	}
	slog.Error("miniapps: wallet patch anchor missed", "err", err)
	if p.Guard {
		return nil, false, err
	}
	return data, false, nil
}
