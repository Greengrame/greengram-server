package media

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"math"
	"strings"
	"testing"
)

const realLottie = `{"v":"5.5.2","fr":60,"ip":0,"op":60,"w":512,"h":512,"layers":[{"ty":4}]}`

func ebmlEl(id []byte, body []byte) []byte {
	size := make([]byte, 8)
	binary.BigEndian.PutUint64(size, uint64(len(body))|1<<56)
	return append(append(append([]byte{}, id...), size...), body...)
}

func webmOf(w, h int, codec string, seconds float64, audio bool) []byte {
	join := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	u16 := func(v int) []byte { return []byte{byte(v >> 8), byte(v)} }
	dur := make([]byte, 8)
	binary.BigEndian.PutUint64(dur, math.Float64bits(seconds*1000))
	track := join(ebmlEl([]byte{0x83}, []byte{1}), ebmlEl([]byte{0x86}, []byte(codec)),
		ebmlEl([]byte{0xE0}, join(ebmlEl([]byte{0xB0}, u16(w)), ebmlEl([]byte{0xBA}, u16(h)))))
	tracks := ebmlEl([]byte{0xAE}, track)
	if audio {
		tracks = join(tracks, ebmlEl([]byte{0xAE}, ebmlEl([]byte{0x83}, []byte{2})))
	}
	segment := join(
		ebmlEl([]byte{0x15, 0x49, 0xA9, 0x66}, join(ebmlEl([]byte{0x2A, 0xD7, 0xB1}, []byte{0x0F, 0x42, 0x40}), ebmlEl([]byte{0x44, 0x89}, dur))),
		ebmlEl([]byte{0x16, 0x54, 0xAE, 0x6B}, tracks),
		ebmlEl([]byte{0x1F, 0x43, 0xB6, 0x75}, []byte{1, 2, 3}))
	return join(ebmlEl([]byte{0x1A, 0x45, 0xDF, 0xA3}, ebmlEl([]byte{0x42, 0x82}, []byte("webm"))), ebmlEl([]byte{0x18, 0x53, 0x80, 0x67}, segment))
}

func zipOf(t *testing.T, files ...any) []*zip.File {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for i := 0; i+1 < len(files); i += 2 {
		f, err := w.Create(files[i].(string))
		if err != nil {
			t.Fatal(err)
		}
		f.Write(files[i+1].([]byte))
	}
	w.Close()
	r, err := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return r.File
}

func TestCheckStickerFile(t *testing.T) {
	notGzip := append([]byte{0x1f, 0x8b}, "nope"...)
	for _, c := range []struct {
		name  string
		data  []byte
		emoji bool
		mime  string
		w, h  int
		why   string
	}{
		{"png", pngOf(100, 50), false, "image/png", 100, 50, ""},
		{"big png shrinks", pngOf(1024, 512), false, "image/png", 512, 256, ""},
		{"emoji png shrinks", pngOf(512, 512), true, "image/png", 100, 100, ""},
		{"huge png", pngOf(2049, 4), false, "", 0, 0, "over 2048"},
		{"png magic only", []byte("\x89PNG\r\n\x1a\nrubbish rubbish rubbish"), false, "", 0, 0, "broken"},
		{"tgs", tgs(realLottie), true, "application/x-tgsticker", 512, 512, ""},
		{"gzip of something else", tgs(`{"hello":1}`), false, "", 0, 0, "real TGS"},
		{"tgs without layers", tgs(`{"v":"5","fr":60,"ip":0,"op":60,"w":512,"h":512,"layers":[]}`), false, "", 0, 0, "real TGS"},
		{"tgs bomb", tgs(`{"v":"` + strings.Repeat("a", 5<<20) + `"}`), false, "", 0, 0, "real TGS"},
		{"not gzip", notGzip, false, "", 0, 0, "real TGS"},
		{"webm", webmOf(512, 400, "V_VP9", 2.5, false), false, "video/webm", 512, 400, ""},
		{"emoji webm", webmOf(100, 100, "V_VP9", 3, false), true, "video/webm", 100, 100, ""},
		{"emoji webm too big", webmOf(512, 512, "V_VP9", 3, false), true, "", 0, 0, "too big"},
		{"vp8", webmOf(512, 512, "V_VP8", 1, false), false, "", 0, 0, "VP9"},
		{"long webm", webmOf(512, 512, "V_VP9", 4, false), false, "", 0, 0, "3 seconds"},
		{"webm with sound", webmOf(512, 512, "V_VP9", 1, true), false, "", 0, 0, "sound"},
		{"webm magic only", []byte{0x1a, 0x45, 0xdf, 0xa3, 0x80}, false, "", 0, 0, "real WEBM"},
		{"exe", []byte("MZ\x90\x00"), false, "", 0, 0, "inside"},
		{"empty", nil, false, "", 0, 0, "inside"},
	} {
		f, why := checkStickerFile(c.data, c.emoji)
		if c.why != "" {
			if !strings.Contains(why, c.why) {
				t.Errorf("%s: want %q, got %q", c.name, c.why, why)
			}
			continue
		}
		if why != "" || f.mime != c.mime || f.w != c.w || f.h != c.h {
			t.Errorf("%s: %q %d×%d %q", c.name, f.mime, f.w, f.h, why)
		}
	}
	// scaling has to produce a picture of the size it reports
	f, _ := checkStickerFile(pngOf(1024, 512), false)
	img, err := png.Decode(bytes.NewReader(f.data))
	if err != nil || img.Bounds() != image.Rect(0, 0, 512, 256) {
		t.Fatalf("scaled: %v %v", err, img)
	}
}

func TestPlanZipRefusesTraversal(t *testing.T) {
	for _, name := range []string{"../evil.tgs", "a/../../evil.tgs", "/etc/evil.tgs", `..\evil.tgs`, `C:\evil.tgs`, "a/\x00.tgs"} {
		if _, _, err := planZip(zipOf(t, "ok.tgs", tgs(realLottie), name, tgs(realLottie))); err == nil {
			t.Errorf("%q: passed", name)
		}
	}
}

func TestPlanZipLayouts(t *testing.T) {
	groups, skipped, err := planZip(zipOf(t,
		"happy/happy.tgs", tgs(realLottie),
		"happy/happy.png", pngOf(8, 8), // the same sticker, as a still
		"sad/sad.webm", []byte("x"),
		"more/deep/down/s10.webp", []byte("x"),
		"more/deep/down/s2.webp", []byte("x"),
		"more/deep/down/preview.png", []byte("x"),
		"flat.PNG", []byte("x"),
		"__MACOSX/happy/._happy.tgs", []byte("x"),
		".hidden/x.tgs", []byte("x"),
		"pack.json", []byte("{}"),
		"notes.exe", []byte("MZ"),
		"folder/", []byte{},
	))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, g := range groups {
		names = append(names, g.name)
	}
	if got := strings.Join(names, " "); got != "flat.PNG happy/happy.tgs more/deep/down/s2.webp more/deep/down/s10.webp sad/sad.webm" {
		t.Fatalf("stickers: %s", got)
	}
	if len(groups[1].files) != 2 || groups[1].files[0].rank != 0 {
		t.Fatalf("animated first: %+v", groups[1].files)
	}
	if len(skipped) != 2 || skipped[0].Name != "more/deep/down/preview.png" && skipped[1].Name != "more/deep/down/preview.png" {
		t.Fatalf("skipped: %+v", skipped)
	}
}

func TestZipHeaderCount(t *testing.T) {
	var files []any
	for i := range 5 {
		files = append(files, "f"+string(rune('a'+i))+".tgs", []byte("x"))
	}
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for i := 0; i < len(files); i += 2 {
		f, _ := w.Create(files[i].(string))
		f.Write(files[i+1].([]byte))
	}
	w.Close()
	if n, err := zipHeaderCount(bytes.NewReader(b.Bytes()), int64(b.Len())); err != nil || n != 5 {
		t.Fatalf("%d %v", n, err)
	}
	// a signature split over the read buffer's edge still counts
	big := make([]byte, 1<<20+10)
	copy(big[1<<20-2:], "PK\x01\x02")
	if n, _ := zipHeaderCount(bytes.NewReader(big), int64(len(big))); n != 1 {
		t.Fatalf("split signature: %d", n)
	}
}

func TestEmojiInName(t *testing.T) {
	for name, want := range map[string]string{
		"😀.tgs": "😀", "pack/12_🔥.webp": "🔥", "x/random_name.tgs": "", "🇺🇦 flag.png": "🇺🇦", "👩‍💻.tgs": "👩‍💻", "❤️.webm": "❤️", "👍🏽_ok.tgs": "👍🏽", "😀/noname.tgs": "",
	} {
		if got := emojiInName(name); got != want {
			t.Errorf("%q: %q", name, got)
		}
	}
}

func TestNaturalLess(t *testing.T) {
	for _, c := range [][2]string{{"s2", "s10"}, {"A", "b"}, {"a/1", "a/01x"}, {"x", "x1"}} {
		if !naturalLess(c[0], c[1]) || naturalLess(c[1], c[0]) {
			t.Errorf("%q < %q", c[0], c[1])
		}
	}
}

