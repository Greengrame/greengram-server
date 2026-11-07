package miniapps

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/Greengrame/greengram-server/internal/media"
)

// Two imports at once at most, one per person: each holds an archive on disk
// and up to a few hundred decodes of the box's CPU.
var (
	zipSlots = make(chan struct{}, 2)
	zipBusy  sync.Map
)

// stickerZip takes a whole pack as a ZIP. The stickers are stored and handed
// back as if each had been uploaded; saving the pack stays with the page.
func (s *Server) stickerZip(w http.ResponseWriter, r *http.Request) {
	u, err := s.stickerUser(r)
	if err != nil {
		answer(w, "stickers import", nil, err)
		return
	}
	if r.ContentLength > media.MaxZipBytes {
		fail(w, 413, "Upload a ZIP of up to 64 MB.")
		return
	}
	if _, busy := zipBusy.LoadOrStore(u.ID, true); busy {
		fail(w, 429, "Another import of yours is still running.")
		return
	}
	defer zipBusy.Delete(u.ID)
	select {
	case zipSlots <- struct{}{}:
		defer func() { <-zipSlots }()
	default:
		fail(w, 429, "Too many imports right now. Try again in a minute.")
		return
	}
	// the server has no body timeout, and a slow sender would keep a slot
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Now().Add(3 * time.Minute))
	f, err := os.CreateTemp("", "gg-sticker-zip-*")
	if err != nil {
		slog.Error("miniapps: stickers import", "err", err)
		fail(w, 500, "Something went wrong. Try again.")
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	size, err := io.Copy(f, http.MaxBytesReader(w, r.Body, media.MaxZipBytes))
	if err != nil {
		fail(w, 413, "Upload a ZIP of up to 64 MB.")
		return
	}
	have, _ := strconv.Atoi(r.URL.Query().Get("have"))
	// not r.Context(): leaving the page mustn't strand files half way
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()
	rep, err := s.Media.ImportStickerZip(ctx, u.ID, r.URL.Query().Get("kind") == "emoji", have, f, size)
	if err != nil {
		answer(w, "stickers import", nil, err)
		return
	}
	items := make([]map[string]any, 0, len(rep.Stickers))
	for _, it := range rep.Stickers {
		id := strconv.FormatInt(it.Doc.ID, 10)
		items = append(items, map[string]any{"doc": id, "thumb": stickerURL(it.Doc.ID), "mime": it.Doc.Mime, "emoji": it.Emoji, "name": it.Name})
	}
	skipped := rep.Skipped
	more := 0
	if len(skipped) > 100 {
		more = len(skipped) - 100
		skipped = skipped[:100]
	}
	reply(w, 200, map[string]any{"ok": true, "items": items, "skipped": skipped, "skipped_more": more, "left_out": rep.LeftOut})
}
