package media

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"strconv"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
)

// offline is a Service without a database: enough for parts, blobs and
// image work. swept is set so SavePart never starts the sweeper.
func offline(t *testing.T) *Service {
	t.Helper()
	d, err := OpenDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(nil, Config{Blobs: d, Parts: t.TempDir(), DC: 2})
	s.swept = time.Now()
	return s
}

func TestCheckRange(t *testing.T) {
	for _, c := range []struct {
		offset  int64
		limit   int
		precise bool
		want    string
	}{
		{0, 512 << 10, false, ""},
		{512 << 10, 512 << 10, false, ""},
		{4096, 4096, false, ""},
		{0, 1 << 20, false, ""},
		{0, 1000, false, "LIMIT_INVALID"},
		{0, 12288, false, "LIMIT_INVALID"}, // 4 KB aligned but doesn't divide 1 MB
		{2048, 4096, false, "OFFSET_INVALID"},
		{1<<20 - 4096, 8192, false, "LIMIT_INVALID"}, // crosses a 1 MB boundary
		{0, 2 << 20, false, "LIMIT_INVALID"},
		{-4096, 4096, false, "OFFSET_INVALID"},
		{1024, 3072, true, ""},
		{1000, 1024, true, "OFFSET_INVALID"},
		{1024, 1000, true, "LIMIT_INVALID"},
	} {
		err := checkRange(c.offset, c.limit, c.precise)
		got := ""
		if e, ok := tgerr.As(err); ok {
			got = e.Message
		}
		if got != c.want {
			t.Errorf("checkRange(%d, %d, %v) = %v, want %q", c.offset, c.limit, c.precise, err, c.want)
		}
	}
}

func TestDisk(t *testing.T) {
	ctx := context.Background()
	d, err := OpenDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("greengram"), 1000)
	k1, n, err := d.Put(ctx, bytes.NewReader(data))
	if err != nil || n != int64(len(data)) {
		t.Fatal(err, n)
	}
	k2, _, err := d.Put(ctx, bytes.NewReader(data))
	if err != nil || k1 != k2 {
		t.Fatalf("same bytes, keys %s %s (%v)", k1, k2, err)
	}
	got, err := d.Read(ctx, k1, 9, 9)
	if err != nil || string(got) != "greengram" {
		t.Fatalf("read %q %v", got, err)
	}
	tail, err := d.Read(ctx, k1, int64(len(data))-4, 100)
	if err != nil || string(tail) != "gram" {
		t.Fatalf("tail %q %v", tail, err)
	}
	if _, err := d.Read(ctx, "../../etc/passwd", 0, 1); err == nil {
		t.Fatal("bad key read")
	}
}

func TestAssemble(t *testing.T) {
	ctx := context.Background()
	s := offline(t)
	part := bytes.Repeat([]byte{1}, 4096)
	last := []byte("end")
	for i, p := range [][]byte{part, part, last} {
		if err := s.SavePart(ctx, 1, 77, i, 3, p); err != nil {
			t.Fatal(err)
		}
	}
	whole := append(append(append([]byte{}, part...), part...), last...)
	sum := md5.Sum(whole)
	dir := s.partDir(1, 77)
	if _, _, err := s.assemble(ctx, dir, 2, "", true); !tgerr.Is(err, "FILE_PARTS_INVALID") {
		t.Fatalf("part count differs from saveBigFilePart: %v", err)
	}
	// tgerr splits FILE_PART_3_MISSING into a type and an argument
	if _, _, err := s.assemble(ctx, dir, 4, "", false); !tgerr.Is(err, "FILE_PART_MISSING") || err.(*tgerr.Error).Message != "FILE_PART_3_MISSING" {
		t.Fatalf("missing part: %v", err)
	}
	if _, _, err := s.assemble(ctx, dir, 3, "00", false); !tgerr.Is(err, "MD5_CHECKSUM_INVALID") {
		t.Fatalf("md5: %v", err)
	}
	key, size, err := s.assemble(ctx, dir, 3, hex.EncodeToString(sum[:]), true)
	if err != nil || size != int64(len(whole)) {
		t.Fatal(err, size)
	}
	got, _ := s.blobs.Read(ctx, key, 0, len(whole))
	if !bytes.Equal(got, whole) {
		t.Fatal("assembled bytes differ")
	}

	// parts other than the last must all be the same size
	s.SavePart(ctx, 1, 78, 0, 0, part)
	s.SavePart(ctx, 1, 78, 1, 0, last)
	s.SavePart(ctx, 1, 78, 2, 0, part)
	if _, _, err := s.assemble(ctx, s.partDir(1, 78), 3, "", false); !tgerr.Is(err, "FILE_PART_SIZE_CHANGED") {
		t.Fatalf("uneven parts: %v", err)
	}
	for _, c := range []struct {
		part, total int
		data        []byte
		want        string
	}{
		{-1, 0, part, "FILE_PART_INVALID"},
		{0, 0, nil, "FILE_PART_EMPTY"},
		{0, 0, make([]byte, MaxPartSize+1), "FILE_PART_TOO_BIG"},
		{5, 3, part, "FILE_PARTS_INVALID"},
	} {
		if err := s.SavePart(ctx, 1, 79, c.part, c.total, c.data); !tgerr.Is(err, c.want) {
			t.Errorf("SavePart(%d, %d): %v, want %s", c.part, c.total, err, c.want)
		}
	}
}

// A part re-sent while its upload is being put together waits for that to
// finish instead of changing a file under the reader.
func TestPartWaitsForAssembly(t *testing.T) {
	ctx := context.Background()
	s := offline(t)
	unlock := s.files.lock(1, 80)
	done := make(chan error, 1)
	go func() { done <- s.SavePart(ctx, 1, 80, 0, 0, []byte("late")) }()
	select {
	case err := <-done:
		t.Fatalf("part saved during assembly: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	// other files don't wait
	if err := s.SavePart(ctx, 1, 81, 0, 0, []byte("other")); err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	s.files.mu.Lock()
	left := len(s.files.m)
	s.files.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d file locks left behind", left)
	}
}

func pngOf(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.NRGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func TestPhotoSizes(t *testing.T) {
	ctx := context.Background()
	s := offline(t)
	for _, c := range []struct {
		w, h int
		want []string // type:WxH
	}{
		{3000, 1000, []string{"s:100x33", "m:320x107", "x:800x267", "y:1280x427", "w:2560x853"}},
		{600, 900, []string{"s:67x100", "m:213x320", "x:533x800", "y:600x900"}},
		{700, 500, []string{"s:100x71", "m:320x229", "x:700x500"}},
		{40, 30, []string{"s:40x30"}},
	} {
		img, err := decodeImage(pngOf(c.w, c.h))
		if err != nil {
			t.Fatal(err)
		}
		sizes, err := s.photoSizes(ctx, img)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, z := range sizes {
			got = append(got, z.Type+":"+strconv.Itoa(z.W)+"x"+strconv.Itoa(z.H))
			if raw, _ := s.blobs.Read(ctx, z.Key, 0, z.Bytes); len(raw) != z.Bytes || raw[0] != 0xff || raw[1] != 0xd8 {
				t.Errorf("%s is not a %d byte JPEG", z.Type, z.Bytes)
			}
		}
		if len(got) != len(c.want) {
			t.Fatalf("%dx%d: %v, want %v", c.w, c.h, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%dx%d: %v, want %v", c.w, c.h, got, c.want)
				break
			}
		}
	}
	if _, err := decodeImage([]byte("not a picture")); !tgerr.Is(err, "IMAGE_PROCESS_FAILED") {
		t.Fatalf("garbage: %v", err)
	}
	if _, err := decodeImage(pngOf(2100, 100)); !tgerr.Is(err, "PHOTO_INVALID_DIMENSIONS") {
		t.Fatalf("21:1: %v", err)
	}
}

func TestKindOf(t *testing.T) {
	video := &tg.DocumentAttributeVideo{W: 10, H: 10}
	round := &tg.DocumentAttributeVideo{RoundMessage: true}
	for _, c := range []struct {
		attrs []tg.DocumentAttributeClass
		force bool
		want  string
	}{
		{nil, false, KindFile},
		{[]tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "a.zip"}}, false, KindFile},
		{[]tg.DocumentAttributeClass{video}, false, KindVideo},
		{[]tg.DocumentAttributeClass{video}, true, KindFile},
		{[]tg.DocumentAttributeClass{round}, false, KindRound},
		{[]tg.DocumentAttributeClass{video, &tg.DocumentAttributeAnimated{}}, false, KindGIF},
		{[]tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Voice: true}}, false, KindVoice},
		{[]tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Title: "song"}}, false, KindAudio},
		{[]tg.DocumentAttributeClass{&tg.DocumentAttributeImageSize{}, &tg.DocumentAttributeSticker{}}, false, KindSticker},
	} {
		if got := kindOf(c.attrs, c.force); got != c.want {
			t.Errorf("kindOf(%v, %v) = %s, want %s", c.attrs, c.force, got, c.want)
		}
	}
}

func TestRefRoundTrip(t *testing.T) {
	s := offline(t)
	p := Photo{ID: 5, AccessHash: 6, Date: 7, Sizes: []Size{{Type: "m", W: 320, H: 200, Bytes: 999}}}
	ref := s.PhotoRef(p, true)
	raw, err := ref.Encode()
	if err != nil {
		t.Fatal(err)
	}
	kind, id := ref.Kind, ref.ID
	back, err := Load(raw, &kind, &id)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := back.TL.(*tg.MessageMediaPhoto)
	ph, _ := m.GetPhoto()
	if !ok || !m.Spoiler || back.Kind != KindPhoto || back.ID != 5 || ph.(*tg.Photo).DCID != 2 {
		t.Fatalf("round trip %+v", back)
	}
}

// The Android app is on layer 231 and needs a non-empty thumbs list on a .tgs
// (see blankOutline). Checked on the wire: photoPathSize#d8214d41 "j".
func TestTGSHasThumbOnLayer231(t *testing.T) {
	s := offline(t)
	set := &tg.InputStickerSetID{ID: 1, AccessHash: 2}
	d := Document{ID: 7, AccessHash: 8, Date: 9, Mime: "application/x-tgsticker", Kind: KindEmoji, Size: 10, Attrs: []tg.DocumentAttributeClass{
		&tg.DocumentAttributeImageSize{W: 512, H: 512},
		&tg.DocumentAttributeCustomEmoji{Alt: "x", Stickerset: set},
	}}
	var wire bin.Buffer
	if err := tlprofile.EncodeObject(tlprofile.Profile231, s.DocumentTL(d), &wire); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(nil), wire.Raw()...)
	// id, then one TL string for the type and one for the two path bytes
	want := []byte{0x41, 0x4d, 0x21, 0xd8, 1, 'j', 0, 0, 2, 0x00, 0x80, 0}
	if !bytes.Contains(raw, want) {
		t.Fatalf("no blank path thumb in % x", raw)
	}
	got, err := tlprofile.DecodeObject(tlprofile.Profile231, &wire, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if thumbs := got.(*tg.Document).Thumbs; len(thumbs) != 1 {
		t.Fatalf("thumbs after decoding: %+v", thumbs)
	}

	// a real thumbnail wins, and other files stay as they were
	d.Thumb = &Size{Type: "m", W: 128, H: 128, Bytes: 5}
	if thumbs := s.DocumentTL(d).Thumbs; len(thumbs) != 1 || thumbs[0].(*tg.PhotoSize).Type != "m" {
		t.Fatalf("real thumb lost: %+v", thumbs)
	}
	d.Thumb, d.Mime = nil, "video/webm"
	if thumbs := s.DocumentTL(d).Thumbs; len(thumbs) != 0 {
		t.Fatalf("webm got a thumb: %+v", thumbs)
	}
	d.Mime, d.Kind = "application/x-tgsticker", KindFile
	if thumbs := s.DocumentTL(d).Thumbs; len(thumbs) != 0 {
		t.Fatalf("a plain .tgs upload got a thumb: %+v", thumbs)
	}
}
