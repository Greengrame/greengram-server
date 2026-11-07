package media

import (
	"bytes"
	"compress/gzip"
	"testing"
)

func tgs(body string) []byte {
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	z.Write([]byte(body))
	z.Close()
	return b.Bytes()
}

func TestStickerInfo(t *testing.T) {
	for _, c := range []struct {
		name  string
		data  []byte
		emoji bool
		mime  string
		w     int
	}{
		{"png", pngOf(512, 300), false, "image/png", 512},
		{"big png", pngOf(513, 10), false, "", 0},
		{"emoji png", pngOf(100, 100), true, "image/png", 100},
		{"big emoji", pngOf(101, 100), true, "", 0},
		{"tgs", tgs(`{"v":"5.5.2","w":512,"h":512,"layers":[]}`), false, "application/x-tgsticker", 512},
		{"gzip not lottie", tgs(`{"hello":1}`), false, "", 0},
		{"webm", []byte{0x1a, 0x45, 0xdf, 0xa3, 0, 0}, true, "video/webm", 100},
		{"junk", []byte("GIF89a......"), false, "", 0},
	} {
		mime, w, _, err := stickerInfo(c.data, c.emoji)
		if mime != c.mime || w != c.w || (c.mime == "") != (err != nil) {
			t.Errorf("%s: %q %d %v", c.name, mime, w, err)
		}
	}
}

func TestValidEmoji(t *testing.T) {
	for v, want := range map[string]bool{"😀": true, "❤️": true, "⭐": true, "a": false, "": false, "🔥🔥": true, "hello world": false} {
		if validEmoji(v) != want {
			t.Errorf("%q: want %v", v, want)
		}
	}
}
