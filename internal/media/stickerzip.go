package media

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"math"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// Whole packs as a ZIP in the Stickers app. Anyone can send anything here, so
// nothing about the archive is believed: sizes are enforced while reading,
// files are only ever read in memory (a name never reaches the filesystem),
// and each one has to parse as what it claims to be before it is stored.
const (
	// MaxPackStickers and MaxPackEmoji are Telegram's pack sizes.
	MaxPackStickers = 120
	MaxPackEmoji    = 200

	// MaxZipBytes is the upload limit for one archive.
	MaxZipBytes     = 64 << 20
	maxZipEntries   = 2000
	maxZipUnpacked  = 256 << 20
	maxZipImageSide = 2048
	// Uploads not in a pack yet, per person per day. Imports store before the
	// pack is saved, like single uploads, and nothing prunes those yet.
	maxOpenUploads = 1000
	maxWebmSeconds = 3.05
)

// ZipSticker is one stored file, ready to go in a pack.
type ZipSticker struct {
	Doc   Document
	Emoji string // from the file's name, "" if it has none
	Name  string
}

// ZipSkip says why a file of the archive isn't in the result.
type ZipSkip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// ZipReport is what an import did.
type ZipReport struct {
	Stickers []ZipSticker
	Skipped  []ZipSkip
	LeftOut  int // usable-looking files past the pack's limit
}

// One sticker of the archive: a name stem in a folder, with the formats it
// came in, best first. foo/foo.tgs next to foo/foo.png is one sticker.
type zipGroup struct {
	name  string
	files []zipCandidate
}

type zipCandidate struct {
	f    *zip.File
	name string
	rank int
}

var zipFormats = map[string]int{".tgs": 0, ".webm": 1, ".webp": 2, ".png": 3}

// What packs ship next to the stickers; not worth a line in the report.
var zipIgnored = map[string]bool{".json": true, ".txt": true, ".md": true, ".html": true, ".htm": true, ".url": true, ".nfo": true, ".ini": true, ".db": true, ".xml": true, ".yaml": true, ".yml": true}

var zipPreviewStems = map[string]bool{"thumb": true, "thumbnail": true, "preview": true, "cover": true, "icon": true, "logo": true}

// ImportStickerZip stores the stickers of an archive for a pack that isn't
// saved yet. have is how many the pack holds already.
func (s *Service) ImportStickerZip(ctx context.Context, owner int64, emoji bool, have int, r io.ReaderAt, size int64) (ZipReport, error) {
	var rep ZipReport
	if size <= 0 || size > MaxZipBytes {
		return rep, refuse(413, "Upload a ZIP of up to 64 MB.")
	}
	// zip.NewReader allocates a struct per directory entry before anything can
	// count them, and its entry count field can't be trusted. Signatures in the
	// file are an upper bound on the entries however the directory lies.
	headers, err := zipHeaderCount(r, size)
	if err != nil {
		return rep, err
	}
	if headers > maxZipEntries {
		return rep, refuse(413, "This ZIP has too many files.")
	}
	var open int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM documents d WHERE d.owner_id=$1 AND d.kind = ANY($2) AND d.date > $3
		AND NOT EXISTS (SELECT 1 FROM sticker_set_items i WHERE i.document_id=d.id)`, owner, []string{KindSticker, KindEmoji}, s.unix()-86400).Scan(&open); err != nil {
		return rep, err
	}
	if open > maxOpenUploads {
		return rep, refuse(429, "Too many uploads waiting for a pack. Save a pack first, or try again tomorrow.")
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return rep, refuse(400, "That file isn't a ZIP this can read.")
	}
	groups, skipped, err := planZip(zr.File)
	if err != nil {
		return rep, err
	}
	rep.Skipped = skipped
	limit := MaxPackStickers
	kind := KindSticker
	if emoji {
		limit, kind = MaxPackEmoji, KindEmoji
	}
	limit -= max(have, 0)
	seen := map[[sha256.Size]byte]bool{}
	var read int64
	var docs []Document
	for i, g := range groups {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		// a header can lie about its size; read only counts what really came out
		if len(rep.Stickers) >= limit || read > maxZipUnpacked {
			rep.LeftOut = len(groups) - i
			break
		}
		var f stickerFile
		why := ""
		for _, c := range g.files {
			var data []byte
			if data, why = readZipEntry(c.f, &read); why == "" {
				f, why = checkStickerFile(data, emoji)
			}
			if why == "" {
				break
			}
		}
		if why != "" {
			rep.Skipped = append(rep.Skipped, ZipSkip{g.name, why})
			continue
		}
		sum := sha256.Sum256(f.data)
		if seen[sum] {
			rep.Skipped = append(rep.Skipped, ZipSkip{g.name, "same file as another one"})
			continue
		}
		seen[sum] = true
		key, stored, err := s.blobs.Put(ctx, bytes.NewReader(f.data))
		if err != nil {
			return rep, err
		}
		d := Document{ID: newID(), AccessHash: newID(), Owner: owner, Date: s.unix(), Mime: f.mime, Kind: kind, Key: key, Size: stored,
			Attrs: packAttrs(f.mime, f.w, f.h, emoji, "", &tg.InputStickerSetEmpty{}, false)}
		docs = append(docs, d)
		rep.Stickers = append(rep.Stickers, ZipSticker{Doc: d, Emoji: emojiInName(g.name), Name: g.name})
	}
	if len(docs) == 0 {
		return rep, nil
	}
	return rep, s.tx(ctx, func(tx pgx.Tx) error {
		for _, d := range docs {
			if err := s.insertDocument(ctx, tx, d); err != nil {
				return err
			}
		}
		return nil
	})
}

func zipHeaderCount(r io.ReaderAt, size int64) (int, error) {
	sig := []byte("PK\x01\x02")
	buf := make([]byte, 1<<20)
	sr := io.NewSectionReader(r, 0, size)
	n, keep := 0, 0
	for {
		m, err := io.ReadFull(sr, buf[keep:])
		b := buf[:keep+m]
		n += bytes.Count(b, sig)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return n, nil
		}
		if err != nil {
			return 0, err
		}
		// 3 bytes can't hold a whole signature, so nothing counts twice
		keep = copy(buf, b[len(b)-3:])
	}
}

// planZip checks the whole directory before any file is read, then sorts out
// which entries are stickers. No layout is assumed: stickers can sit at any
// depth, in folders of one or all together.
func planZip(files []*zip.File) ([]zipGroup, []ZipSkip, error) {
	var total uint64
	byKey := map[string]*zipGroup{}
	folderSize := map[string]int{}
	var skipped []ZipSkip
	var order []string
	for _, f := range files {
		name, ok := zipEntryName(f.Name)
		if !ok {
			return nil, nil, refuse(400, "This ZIP has unsafe file paths, like ../ or absolute ones.")
		}
		if f.UncompressedSize64 > maxZipUnpacked {
			return nil, nil, refuse(413, "This ZIP unpacks to too much data.")
		}
		if total += f.UncompressedSize64; total > maxZipUnpacked {
			return nil, nil, refuse(413, "This ZIP unpacks to too much data.")
		}
		if f.FileInfo().IsDir() || zipJunk(name) {
			continue
		}
		short := shortZipName(name)
		ext := strings.ToLower(path.Ext(name))
		rank, sticker := zipFormats[ext]
		switch {
		case !sticker && zipIgnored[ext]:
			continue
		case !sticker:
			skipped = append(skipped, ZipSkip{short, "not PNG, WEBP, TGS or WEBM"})
			continue
		case !f.Mode().IsRegular():
			skipped = append(skipped, ZipSkip{short, "not a regular file"})
			continue
		case f.Flags&1 != 0:
			skipped = append(skipped, ZipSkip{short, "password protected"})
			continue
		case f.UncompressedSize64 > MaxStickerBytes:
			skipped = append(skipped, ZipSkip{short, "over 1 MB"})
			continue
		}
		dir := path.Dir(name)
		key := dir + "\x00" + strings.ToLower(strings.TrimSuffix(path.Base(name), path.Ext(name)))
		g := byKey[key]
		if g == nil {
			g = &zipGroup{name: short}
			byKey[key] = g
			order = append(order, key)
			folderSize[dir]++
		}
		g.files = append(g.files, zipCandidate{f, short, rank})
	}
	out := make([]zipGroup, 0, len(order))
	for _, key := range order {
		g := byKey[key]
		sort.SliceStable(g.files, func(i, j int) bool { return g.files[i].rank < g.files[j].rank })
		dir, stem, _ := strings.Cut(key, "\x00")
		if folderSize[dir] > 1 && zipPreviewStems[stem] && g.files[0].rank >= zipFormats[".webp"] {
			skipped = append(skipped, ZipSkip{g.name, "looks like a preview"})
			continue
		}
		out = append(out, *g)
	}
	sort.SliceStable(out, func(i, j int) bool { return naturalLess(out[i].name, out[j].name) })
	return out, skipped, nil
}

// zipEntryName is the entry's path with / separators, or false if it would
// point outside the archive were anything to unpack it.
func zipEntryName(raw string) (string, bool) {
	name := strings.ReplaceAll(raw, "\\", "/")
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) || len(name) >= 2 && name[1] == ':' {
		return "", false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", false
		}
	}
	return strings.ToValidUTF8(path.Clean(name), "�"), true
}

// macOS's AppleDouble twins (__MACOSX/x/._x.tgs) have sticker extensions.
func zipJunk(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if part == "__MACOSX" || part != "." && strings.HasPrefix(part, ".") {
			return true
		}
	}
	base := strings.ToLower(path.Base(name))
	return base == "thumbs.db" || base == "desktop.ini"
}

func shortZipName(name string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	if r := []rune(clean); len(r) > 100 {
		return "…" + string(r[len(r)-99:])
	}
	return clean
}

func readZipEntry(f *zip.File, read *int64) ([]byte, string) {
	rc, err := f.Open()
	if err != nil {
		return nil, "can't be unpacked"
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, MaxStickerBytes+1))
	*read += int64(len(data))
	if len(data) > MaxStickerBytes {
		return nil, "over 1 MB"
	}
	if err != nil {
		return nil, "damaged"
	}
	return data, ""
}

// stickerFile is an archive file that passed, ready to store.
type stickerFile struct {
	data []byte
	mime string
	w, h int
}

// checkStickerFile says by the content, never the name, what a file is. It
// returns why not if it isn't a sticker. Pictures over the size limit get
// scaled down; video can't be, so it has to fit.
func checkStickerFile(data []byte, emoji bool) (stickerFile, string) {
	limit := 512
	if emoji {
		limit = 100
	}
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return checkImage(data, limit, png.DecodeConfig, png.Decode, "image/png")
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return checkImage(data, limit, webp.DecodeConfig, webp.Decode, "image/webp")
	case bytes.HasPrefix(data, []byte{0x1f, 0x8b}):
		return checkTGS(data)
	case bytes.HasPrefix(data, []byte{0x1a, 0x45, 0xdf, 0xa3}):
		return checkWebm(data, limit)
	}
	return stickerFile{}, "not a PNG, WEBP, TGS or WEBM inside"
}

func checkImage(data []byte, limit int, config func(io.Reader) (image.Config, error), decode func(io.Reader) (image.Image, error), mime string) (stickerFile, string) {
	cfg, err := config(bytes.NewReader(data))
	if err != nil {
		return stickerFile{}, "broken image"
	}
	w, h := cfg.Width, cfg.Height
	if w < 1 || h < 1 || w > maxZipImageSide || h > maxZipImageSide {
		return stickerFile{}, "image is over 2048 pixels"
	}
	// the size is checked first: this is what the header's size costs in memory.
	// Animated WEBP has no decoder here and fails too, as it should.
	img, err := decode(bytes.NewReader(data))
	if err != nil {
		return stickerFile{}, "broken image"
	}
	if w <= limit && h <= limit {
		return stickerFile{data, mime, w, h}, ""
	}
	nw, nh := limit, limit
	if w >= h {
		nh = max(h*limit/w, 1)
	} else {
		nw = max(w*limit/h, 1)
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.BiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, dst); err != nil || out.Len() > MaxStickerBytes {
		return stickerFile{}, "over 1 MB after scaling down"
	}
	return stickerFile{out.Bytes(), "image/png", nw, nh}, ""
}

func checkTGS(data []byte) (stickerFile, string) {
	const bad = "not a real TGS"
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return stickerFile{}, bad
	}
	// a gzip bomb stops here: longer than this doesn't parse
	raw, err := io.ReadAll(io.LimitReader(zr, 4<<20))
	if err != nil {
		return stickerFile{}, bad
	}
	var lottie struct {
		V      string            `json:"v"`
		FR     float64           `json:"fr"`
		W      float64           `json:"w"`
		H      float64           `json:"h"`
		IP     *float64          `json:"ip"`
		OP     *float64          `json:"op"`
		Layers []json.RawMessage `json:"layers"`
	}
	if json.Unmarshal(raw, &lottie) != nil || lottie.V == "" || lottie.FR <= 0 || lottie.IP == nil || lottie.OP == nil || *lottie.OP <= *lottie.IP || len(lottie.Layers) == 0 ||
		lottie.W < 1 || lottie.H < 1 || lottie.W > 4096 || lottie.H > 4096 {
		return stickerFile{}, bad
	}
	return stickerFile{data, "application/x-tgsticker", int(lottie.W), int(lottie.H)}, ""
}

func checkWebm(data []byte, limit int) (stickerFile, string) {
	info, ok := parseWebm(data)
	switch {
	case !ok || info.w < 1 || info.h < 1 || !info.frames:
		return stickerFile{}, "not a real WEBM"
	case !info.vp9:
		return stickerFile{}, "video isn't VP9"
	case info.audio:
		return stickerFile{}, "video has sound"
	case info.w > limit || info.h > limit:
		return stickerFile{}, "video is too big, can't scale it down"
	case info.seconds > maxWebmSeconds:
		return stickerFile{}, "video is over 3 seconds"
	}
	return stickerFile{data, "video/webm", info.w, info.h}, ""
}

type webmInfo struct {
	w, h    int
	seconds float64
	vp9     bool
	audio   bool
	frames  bool
}

const (
	ebmlHeader    = 0x1A45DFA3
	ebmlDocType   = 0x4282
	ebmlSegment   = 0x18538067
	ebmlInfo      = 0x1549A966
	ebmlTimescale = 0x2AD7B1
	ebmlDuration  = 0x4489
	ebmlTracks    = 0x1654AE6B
	ebmlTrack     = 0xAE
	ebmlTrackType = 0x83
	ebmlCodec     = 0x86
	ebmlVideo     = 0xE0
	ebmlPixelW    = 0xB0
	ebmlPixelH    = 0xBA
	ebmlCluster   = 0x1F43B675
)

// parseWebm walks the Matroska headers: the document type, the tracks and the
// duration. false means the structure doesn't hold together.
func parseWebm(data []byte) (webmInfo, bool) {
	var info webmInfo
	header, rest, ok := ebmlFind(data, ebmlHeader)
	if !ok {
		return info, false
	}
	doc, _, ok := ebmlFind(header, ebmlDocType)
	if !ok || strings.TrimRight(string(doc), "\x00") != "webm" {
		return info, false
	}
	segment, _, ok := ebmlFind(rest, ebmlSegment)
	if !ok {
		return info, false
	}
	scale, duration := uint64(1000000), 0.0
	for len(segment) > 0 {
		id, body, next, ok := ebmlNext(segment)
		if !ok {
			return info, false
		}
		segment = next
		switch id {
		case ebmlInfo:
			if v, _, ok := ebmlFind(body, ebmlTimescale); ok {
				if n := ebmlUint(v); n > 0 {
					scale = n
				}
			}
			if v, _, ok := ebmlFind(body, ebmlDuration); ok {
				duration = ebmlFloat(v)
			}
		case ebmlTracks:
			for len(body) > 0 {
				tid, track, more, ok := ebmlNext(body)
				if !ok {
					return info, false
				}
				body = more
				if tid == ebmlTrack {
					readWebmTrack(track, &info)
				}
			}
		case ebmlCluster:
			info.frames = len(body) > 0
			info.seconds = duration * float64(scale) / 1e9
			return info, true
		}
	}
	return info, false
}

func readWebmTrack(track []byte, info *webmInfo) {
	kind, _, _ := ebmlFind(track, ebmlTrackType)
	switch ebmlUint(kind) {
	case 2:
		info.audio = true
	case 1:
		codec, _, _ := ebmlFind(track, ebmlCodec)
		info.vp9 = strings.TrimRight(string(codec), "\x00") == "V_VP9"
		video, _, _ := ebmlFind(track, ebmlVideo)
		w, _, _ := ebmlFind(video, ebmlPixelW)
		h, _, _ := ebmlFind(video, ebmlPixelH)
		info.w, info.h = int(min(ebmlUint(w), 1<<16)), int(min(ebmlUint(h), 1<<16))
	}
}

// ebmlFind returns the first child with this id and what follows it.
func ebmlFind(b []byte, want uint32) (body, rest []byte, ok bool) {
	for len(b) > 0 {
		id, body, next, ok := ebmlNext(b)
		if !ok {
			return nil, nil, false
		}
		if id == want {
			return body, next, true
		}
		b = next
	}
	return nil, nil, false
}

// ebmlNext splits off one element. An unknown size, which live writers use for
// segments and clusters, takes the rest of the buffer.
func ebmlNext(b []byte) (id uint32, body, rest []byte, ok bool) {
	idLen := ebmlLen(b)
	if idLen == 0 || idLen > 4 || idLen >= len(b) {
		return 0, nil, nil, false
	}
	for _, c := range b[:idLen] {
		id = id<<8 | uint32(c)
	}
	b = b[idLen:]
	n := ebmlLen(b)
	if n == 0 || n > len(b) {
		return 0, nil, nil, false
	}
	size := uint64(b[0]) &^ (0x80 >> (n - 1))
	unknown := size == 0xFF>>n
	for _, c := range b[1:n] {
		size = size<<8 | uint64(c)
		unknown = unknown && c == 0xFF
	}
	b = b[n:]
	if unknown {
		return id, b, nil, true
	}
	if size > uint64(len(b)) {
		return 0, nil, nil, false
	}
	return id, b[:size], b[size:], true
}

func ebmlLen(b []byte) int {
	if len(b) == 0 || b[0] == 0 {
		return 0
	}
	n := 1
	for c := b[0]; c&0x80 == 0; c <<= 1 {
		n++
	}
	return n
}

func ebmlUint(b []byte) uint64 {
	var n uint64
	for _, c := range b {
		n = n<<8 | uint64(c)
	}
	return n
}

func ebmlFloat(b []byte) float64 {
	switch len(b) {
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b)))
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b))
	}
	return 0
}

// emojiInName finds an emoji in a file's name (😀.tgs, 12_🔥.webp), the only
// place an archive has one. "" leaves it to the person.
func emojiInName(name string) string {
	r := []rune(path.Base(name))
	for i := 0; i < len(r); i++ {
		if !emojiStart(r[i]) {
			continue
		}
		j := i + 1
		if r[i] >= 0x1F1E6 && r[i] <= 0x1F1FF {
			if j < len(r) && r[j] >= 0x1F1E6 && r[j] <= 0x1F1FF {
				j++
			}
		} else {
			for j < len(r) {
				if r[j] == 0xFE0F || r[j] >= 0x1F3FB && r[j] <= 0x1F3FF {
					j++
				} else if r[j] == 0x200D && j+1 < len(r) {
					j += 2
				} else {
					break
				}
			}
		}
		if v := string(r[i:j]); validEmoji(v) {
			return v
		}
		return ""
	}
	return ""
}

func emojiStart(r rune) bool {
	return r >= 0x1F000 && r <= 0x1FAFF || r >= 0x2600 && r <= 0x27BF || r >= 0x2B00 && r <= 0x2BFF
}

// naturalLess sorts sticker2 before sticker10, ignoring case.
func naturalLess(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	for a != "" && b != "" {
		if a[0] >= '0' && a[0] <= '9' && b[0] >= '0' && b[0] <= '9' {
			x, y := digitRun(a), digitRun(b)
			tx, ty := strings.TrimLeft(a[:x], "0"), strings.TrimLeft(b[:y], "0")
			if len(tx) != len(ty) {
				return len(tx) < len(ty)
			}
			if tx != ty {
				return tx < ty
			}
			a, b = a[x:], b[y:]
			continue
		}
		ra, na := utf8.DecodeRuneInString(a)
		rb, nb := utf8.DecodeRuneInString(b)
		if ra != rb {
			return ra < rb
		}
		a, b = a[na:], b[nb:]
	}
	return len(a) < len(b)
}

func digitRun(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}
