package miniapps

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/media"
)

// The Stickers app: the captured bot-stickers.js pages, with markup this
// server writes to the shape of the real pages (those are per person, so they
// aren't in vendor/), and the script's API methods over the media service.

var (
	mediaKeyOnce sync.Once
	mediaKey     []byte
)

// stickerURL is a preview link for one sticker. Images load without the
// launch data, so the link carries a signature instead: only pages and
// answers already allowed to show a sticker hand one out.
func stickerURL(id int64) string {
	return signedURL("/__miniapps/stickers/media/", id)
}

func signedURL(prefix string, id int64) string {
	mediaKeyOnce.Do(func() {
		mediaKey = make([]byte, 32)
		rand.Read(mediaKey)
	})
	m := hmac.New(sha256.New, mediaKey)
	fmt.Fprint(m, prefix, id)
	return prefix + strconv.FormatInt(id, 10) + "?s=" + hex.EncodeToString(m.Sum(nil)[:12])
}

func (s *Server) stickerMedia(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("doc"), 10, 64)
	if err != nil || !hmac.Equal([]byte(stickerURL(id)), []byte(r.URL.Path+"?s="+r.URL.Query().Get("s"))) {
		http.NotFound(w, r)
		return
	}
	d, data, err := s.Media.StickerFile(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", d.Mime)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Write(data)
}

func stickerSource(src string) string {
	return `<source type="application/x-tgsticker" srcset="` + src + `">`
}

// stickerThumb is a sticker in a row or at the top of a page. Telegram serves
// a still picture for all of them; here animated ones get their first frame,
// drawn by the adapter (js-lottie-thumb) or the video element.
func stickerThumb(d media.Document, classes string) string {
	src := html.EscapeString(stickerURL(d.ID))
	switch d.Mime {
	case "application/x-tgsticker":
		return `<picture class="` + classes + ` js-lottie-thumb">` + stickerSource(src) + `</picture>`
	case "video/webm":
		return `<video class="` + classes + `" src="` + src + `" muted playsinline preload="metadata"></video>`
	}
	return `<img class="` + classes + `" src="` + src + `">`
}

// stickerGridItem has the shapes NewPack.addFile gives each kind of sticker.
func stickerGridItem(it media.Item) string {
	src := html.EscapeString(stickerURL(it.Doc.ID))
	attrs := ` data-emoji="` + html.EscapeString(it.Emoji) + `" data-raw="` + strconv.FormatInt(it.Doc.ID, 10) + `"`
	switch it.Doc.Mime {
	case "application/x-tgsticker":
		return `<picture class="tm-grid-item tm-grid-item-sticker tm-grid-item-sticker-animated js-lottie-thumb"` + attrs + `>` + stickerSource(src) + `</picture>`
	case "video/webm":
		return `<div class="tm-grid-item tm-grid-item-sticker tm-grid-item-sticker-animated"` + attrs + `><video muted playsinline preload="metadata" src="` + src + `"></video></div>`
	}
	return `<div class="tm-grid-item tm-grid-item-sticker"` + attrs + `><img src="` + src + `"></div>`
}

// stickerPreview is the big sticker in the bottom sheet, as in stickerIndex.
func stickerPreview(d media.Document) string {
	src := html.EscapeString(stickerURL(d.ID))
	switch d.Mime {
	case "application/x-tgsticker":
		return `<div class="sticker-wrap"><picture class="sticker animated"><div style="padding-top:100%"></div>` + stickerSource(src) + `</picture></div>`
	case "video/webm":
		return `<div class="sticker-wrap"><div class="sticker video"><div style="padding-top:100%"></div><video src="` + src + `" width="100%" height="100%" preload muted autoplay loop playsinline disablepictureinpicture></video></div></div>`
	}
	w, h := media.StickerSize(d)
	return `<div class="sticker-wrap"><i class="sticker image" style="background-image:url('` + src + `');" data-webp="` + src + `"><div style="padding-top:` + strconv.FormatFloat(float64(h)/float64(max(w, 1))*100, 'f', 4, 64) + `%"></div></i></div>`
}

func (s *Server) shareLink(r *http.Request, set media.Set) string {
	kind := "/addstickers/"
	if set.Kind == media.SetEmoji {
		kind = "/addemoji/"
	}
	scheme := "https"
	if r.TLS == nil && (strings.HasPrefix(r.Host, "127.0.0.1") || strings.HasPrefix(r.Host, "localhost")) {
		scheme = "http"
	}
	return scheme + "://" + r.Host + kind + set.Name
}

// The copy of the two kinds of pack. Only the sticker pages were captured; the
// emoji ones follow them word for word, the way the list's Emojis tab does.
type packWords struct {
	Set, NamePlaceholder, Upper, Lower, One, Icon, ListHeader, AddHint string
}

func packWording(emoji bool) packWords {
	if emoji {
		return packWords{"Emoji Set", "Emoji Pack Name", "Emojis", "emojis", "emoji", "--image-url-emoji-add", "Emoji List", "Enter matching emoji for each custom emoji."}
	}
	return packWords{"Sticker Set", "Sticker Pack Name", "Stickers", "stickers", "sticker", "--image-url-sticker-add", "Sticker List", "Enter matching emoji for each sticker."}
}

const (
	stickersAppName  = "Greengram"
	stickerDocsLink  = "https://core.telegram.org/stickers"
	stickerIntroWide = ` style="max-width: 312px;"`
)

func stickerSection(header, body string) string {
	return `<section class="tm-section"><div class="tm-section-header"><h2 class="tm-section-header-text">` + header + `</h2></div>` + body + `</section>`
}

func stickerNameField(placeholder, value string) string {
	return `<div class="tm-field" style="border-radius: 6px; margin-bottom: 1px;"><input type="text" maxlength="64" class="form-control tm-input" name="title" placeholder="` + placeholder + `" value="` + html.EscapeString(value) + `" autocomplete="off" spellcheck="false"></div>`
}

// packAddMenu is the page's one way of adding stickers; the ZIP import sits
// next to the single uploads.
func packAddMenu(upload string) string {
	return `<ul class="dropdown-menu"><li><a class="dropdown-menu-item js-upload-btn">` + upload + `</a></li><li><a class="dropdown-menu-item js-import-zip">Import ZIP</a></li><li><a class="dropdown-menu-item js-open-sticker-picker">Add Existing</a></li></ul>`
}

const stickerZipReport = `<div class="help-text js-zip-report"></div>`

var stickerRoute = regexp.MustCompile(`^/stickers/(pack|editpack|publishpack|stats)/([a-zA-Z0-9_]+)(?:/(edit|stats|\d+))?/?$`)

func (s *Server) stickerPage(r *http.Request, u account.User) (pageView, error) {
	ctx := r.Context()
	q := r.URL.Query()
	emoji := q.Get("emoji") == "1" || q.Get("type") == "emoji" || q.Get("type") == "emojis"
	switch r.URL.Path {
	case "/stickers", "/stickers/":
		return s.stickerList(r, u, emoji)
	case "/stickers/newpack", "/stickers/new":
		return stickerEditor(emoji), nil
	}
	m := stickerRoute.FindStringSubmatch(r.URL.Path)
	if m == nil {
		return pageView{}, &account.Error{Status: 404, Message: "Page not found."}
	}
	set, items, ok, err := s.Media.Pack(ctx, u.ID, m[2])
	if err != nil {
		return pageView{}, err
	}
	if !ok {
		return pageView{}, &account.Error{Status: 404, Message: "Sticker pack not found."}
	}
	own := set.Owner == u.ID && !set.Default && !set.Official
	edit, stats, publish := m[1] == "editpack" || m[3] == "edit", m[1] == "stats" || m[3] == "stats", m[1] == "publishpack"
	if (edit || stats || publish) && !own {
		return pageView{}, &account.Error{Status: 403, Message: "Only the pack owner can manage it."}
	}
	switch {
	case publish:
		return s.stickerPublishPage(r, set, items), nil
	case stats && m[3] != "" && m[3] != "stats":
		return s.stickerUsagePage(r, set, items, m[3])
	case stats:
		return s.stickerStatsPage(r, set, items)
	case edit:
		return s.stickerEditPage(r, set, items), nil
	}
	return s.stickerPackPage(r, set, items, own), nil
}

// packCover is what stands for a pack: its icon, which may be a picture of its
// own, else its first sticker.
func (s *Server) packCover(r *http.Request, set media.Set, items []media.Item) (media.Document, bool) {
	for _, it := range items {
		if it.Doc.ID == set.ThumbDoc {
			return it.Doc, true
		}
	}
	if docs, err := s.Media.Documents(r.Context(), []int64{set.ThumbDoc}); err == nil && len(docs) == 1 {
		return docs[0], true
	}
	if len(items) > 0 {
		return items[0].Doc, true
	}
	return media.Document{}, false
}

func stickerMainClass(set media.Set) string {
	class := "tm-main"
	if set.Kind == media.SetEmoji {
		class += " tm-mode-emoji"
	}
	if set.TextColor {
		class += " tm-text-color"
	}
	return class
}

func (s *Server) stickerPackPage(r *http.Request, set media.Set, items []media.Item, own bool) pageView {
	isEmoji := set.Kind == media.SetEmoji
	words := packWording(isEmoji)
	id := strconv.FormatInt(set.ID, 10)
	link := s.shareLink(r, set)
	state := map[string]any{"page": "pack", "type": "stickers", "isEmojis": isEmoji, "packId": id, "shareLink": link, "own": own, "backButton": "/stickers"}
	if isEmoji {
		state["type"] = "emojis"
	}
	index := map[string]string{}
	var grid strings.Builder
	for _, it := range items {
		index["s"+strconv.FormatInt(it.Doc.ID, 10)] = stickerPreview(it.Doc)
		grid.WriteString(stickerGridItem(it))
	}
	state["stickerIndex"] = index
	var b strings.Builder
	b.WriteString(`<main class="` + stickerMainClass(set) + `"><section class="tm-main-intro">`)
	if cover, ok := s.packCover(r, set, items); ok {
		b.WriteString(stickerThumb(cover, "tm-main-intro-picture tm-main-intro-image"))
	}
	b.WriteString(`<h1 class="tm-main-intro-header">` + html.EscapeString(set.Title) + `</h1><p class="tm-main-intro-text js-share-link"` + stickerIntroWide + `>` + html.EscapeString(strings.TrimPrefix(strings.TrimPrefix(link, "https://"), "http://")) + `</p></section>`)
	if own {
		b.WriteString(`<section class="tm-section"><div class="tm-table-wrap tm-menu-items"><a class="tm-row tm-row-link" href="/stickers/editpack/` + id + `"><span class="tm-icon" style="--icon-s: var(--image-url-bot-info)"></span><span>Edit Info</span></a>` +
			`<a class="tm-row tm-row-link" href="/stickers/stats/` + id + `"><span class="tm-icon" style="--icon-s: var(--image-url-stats)"></span><span>Statistics</span></a></div></section>`)
	}
	b.WriteString(`<section class="tm-section"><div class="tm-table-wrap tm-menu-items"><a class="tm-row tm-row-add js-share-link"><span class="tm-icon" style="--icon-s: var(--image-url-share)"></span><span>Open in ` + stickersAppName + `</span></a></div></section>`)
	b.WriteString(`<section class="tm-section"><div class="tm-section-header"><h2 class="tm-section-header-text">` + words.Upper + `</h2>`)
	if own {
		b.WriteString(`<div class="grow-1"></div><a class="tm-section-action edit-button" href="/stickers/editpack/` + id + `">Edit</a>`)
	}
	b.WriteString(`</div><div class="tm-grid-area" dir="ltr">` + grid.String() + `</div>`)
	if own {
		b.WriteString(`<p class="help-text touchscreen-hide">Drag items to reorder. Click to preview.</p><p class="help-text touchscreen-only">Hold and drag items to reorder. Tap to preview.</p>`)
	}
	b.WriteString(`</section>`)
	sheet := ""
	if own {
		name := strings.ToUpper(words.One[:1]) + words.One[1:]
		publish := " hidden"
		if set.Draft {
			publish = ""
		}
		b.WriteString(stickerSection("Actions", `<div class="tm-table-wrap tm-menu-items"><a class="tm-row tm-row-add`+publish+`" href="/stickers/publishpack/`+id+`"><span class="tm-icon" style="--icon-s: var(--image-url-share)"></span><span>Publish `+name+` Pack</span></a>`+
			`<a class="tm-row tm-row-destructive js-delete-pack"><span class="tm-icon" style="--icon-s: var(--image-url-trash)"></span><span>Delete `+name+` Pack</span></a></div>`))
		sheet = `<div class="tm-bottom-sheet-item js-set-pack-icon"><div class="tm-icon" style="--icon-s: var(--image-url-sticker-placeholder)"></div>Set as Pack Icon</div>` +
			`<div class="tm-bottom-sheet-item js-edit-emoji"><div class="tm-icon" style="--icon-s: var(--image-url-emoji-placeholder)"></div>Edit Emoji</div>` +
			`<div class="tm-bottom-sheet-item js-show-stat"><div class="tm-icon" style="--icon-s: var(--image-url-stats)"></div>Show Statistics</div>` +
			`<div class="tm-bottom-sheet-item tm-row-destructive js-delete-sticker"><div class="tm-icon" style="--icon-s: var(--image-url-trash)"></div>Delete ` + name + `</div>`
	}
	b.WriteString(`<div class="tm-bottom-sheet-overlay hidden"><div class="tm-bottom-sheet-content"></div><div class="tm-bottom-sheet">` + sheet + `</div></div></main>`)
	return pageView{Title: "Stickers", Markup: b.String(), State: state}
}

func (s *Server) stickerEditPage(r *http.Request, set media.Set, items []media.Item) pageView {
	isEmoji := set.Kind == media.SetEmoji
	words := packWording(isEmoji)
	id := strconv.FormatInt(set.ID, 10)
	state := map[string]any{"page": "edit", "type": "stickers", "isEmojis": isEmoji, "packId": id, "editing": true, "backButton": "/stickers/pack/" + id}
	if isEmoji {
		state["type"] = "emojis"
	}
	var rows strings.Builder
	for _, it := range items {
		raw := strconv.FormatInt(it.Doc.ID, 10)
		rows.WriteString(`<div class="tm-row tm-sticker-row js-sticker-row tm-sticker-row-edit" data-raw="` + raw + `" data-doc-id="` + raw + `" id="s` + raw + `"><span class="tm-icon js-sortable-handle" style="--icon-s: var(--image-url-rearrange)"></span>` +
			stickerThumb(it.Doc, "tm-row-pic") + `<input type="text" class="form-control tm-input js-emoji-input" name="emoji" placeholder="Enter Emoji" autocomplete="off" value="` + html.EscapeString(it.Emoji) + `" spellcheck="false">` +
			`<span class="tm-icon tm-icon-end tm-toggle-emoji js-toggle-panel"></span><span class="tm-icon tm-icon-end tm-icon-close js-delete-sticker"></span></div>`)
	}
	// the original shows the placeholder until a pack has an icon of its own
	placeholder := "--image-url-sticker-placeholder"
	if isEmoji {
		placeholder = "--image-url-emoji-placeholder"
	}
	icon := `<div style="--icon: var(` + placeholder + `)" class="tm-main-intro-picture tm-main-intro-picture-round tm-placeholder-icon"></div>`
	if cover, ok := s.packCover(r, set, items); ok && len(items) > 0 && cover.ID != items[0].Doc.ID {
		icon = `<div style="--icon: var(` + placeholder + `)" class="tm-main-intro-picture tm-main-intro-picture-round tm-placeholder-icon">` + stickerThumb(cover, "") + `</div>`
	}
	markup := `<main class="` + stickerMainClass(set) + ` tm-stickers-edit-pack"><section class="tm-main-intro">` + icon + `<div class="set-photo-button js-upload-thumb">Set Pack Icon</div></section>` +
		stickerSection("Pack Name", stickerNameField(words.NamePlaceholder, set.Title)) +
		`<section class="tm-section"><div class="tm-section-header" style="padding-bottom: 0;"><h2 class="tm-section-header-text">` + words.ListHeader + `</h2></div>` +
		`<div class="tm-dropdown" style="margin-bottom: 8px; width: 100%"><div class="tm-row tm-row-add dropdown-toggle tm-dropdown-highlight" data-toggle="dropdown" style="border-radius: 8px;"><span class="tm-icon" style="--icon-s: var(` + words.Icon + `)"></span><span>Add ` + words.Upper + `</span></div>` + packAddMenu("Upload "+words.Upper) + `</div>` +
		`<div class="tm-table-wrap js-sortable-table">` + rows.String() + `</div>` + stickerZipReport + `<p class="help-text">` + words.AddHint + `</p></section></main>`
	return pageView{Title: "Stickers", Markup: markup, State: state}
}

// stickerPublishPage: the capture of this page redirected, the pack it was
// taken for being public already. Its fields and copy come from PublishPack in
// bot-stickers.js and the rest of the pages' parts.
func (s *Server) stickerPublishPage(r *http.Request, set media.Set, items []media.Item) pageView {
	if !set.Draft {
		return pageView{Title: "Stickers", Markup: `<main class="tm-main"></main>`, State: map[string]any{"page": "redirect", "redirect": "/stickers"}}
	}
	isEmoji := set.Kind == media.SetEmoji
	words := packWording(isEmoji)
	id := strconv.FormatInt(set.ID, 10)
	link := s.shareLink(r, set)
	base := strings.TrimSuffix(link, set.Name)
	cover := ""
	if d, ok := s.packCover(r, set, items); ok {
		cover = stickerThumb(d, "tm-main-intro-picture tm-main-intro-image")
	}
	name := strings.ToUpper(words.One[:1]) + words.One[1:]
	markup := `<main class="` + stickerMainClass(set) + `"><section class="tm-main-intro">` + cover + `<h1 class="tm-main-intro-header">Publish ` + name + ` Pack</h1><p class="tm-main-intro-text"` + stickerIntroWide + `>Choose a name and a link. Anyone with the link can add your pack.</p></section>` +
		stickerSection("Pack Name", stickerNameField(words.NamePlaceholder, set.Title)) +
		stickerSection("Link", `<div class="tm-field" style="border-radius: 6px; margin-bottom: 1px;"><input type="text" maxlength="64" class="form-control tm-input" name="short_name" placeholder="`+html.EscapeString(set.Name)+`" autocomplete="off" spellcheck="false"></div><div class="hint-text" data-for="short_name"></div><p class="help-text">`+html.EscapeString(base)+`… Leave it empty to keep the one above.</p>`) +
		`</main>`
	return pageView{Title: "Stickers", Markup: markup, State: map[string]any{"page": "publish", "type": "stickers", "isEmojis": isEmoji, "packId": id, "shortName": set.Name, "backButton": "/stickers/pack/" + id}}
}

// The graphs' days, like the captured ones: four months up to today with the
// last month in view.
const (
	statsDays = 126
	statsZoom = 30
)

type statsLine struct {
	Name, Color string
	Days        media.DayCounts
}

func statsGraph(lines ...statsLine) map[string]any {
	today := int(time.Now().Unix() / 86400)
	x := []any{"x"}
	for d := today - statsDays + 1; d <= today; d++ {
		x = append(x, int64(d)*86400000)
	}
	columns := []any{x}
	names, colors, types := map[string]any{}, map[string]any{}, map[string]any{"x": "x"}
	for i, l := range lines {
		key := "y" + strconv.Itoa(i)
		column := []any{key}
		for d := today - statsDays + 1; d <= today; d++ {
			column = append(column, l.Days[d])
		}
		columns = append(columns, column)
		names[key], colors[key], types[key] = l.Name, l.Color, "line"
	}
	// the formatters are functions the adapter fills in: the original sends them
	// as source for health.js to eval, which this CSP refuses
	return map[string]any{"title": "", "columns": columns, "types": types, "names": names, "colors": colors, "hidden": []any{},
		"subchart":    map[string]any{"show": true, "defaultZoom": []int64{int64(today-statsZoom) * 86400000, int64(today) * 86400000}},
		"strokeWidth": 2, "tooltipSort": nil, "tooltipOnHover": false}
}

func statsChart(id string) string {
	const layout = "2 January 2006"
	now := time.Now().UTC()
	dates := now.AddDate(0, 0, -statsZoom).Format(layout) + " - " + now.Format(layout)
	return `<div class="js-date-range" data-for="` + id + `">` + dates + `</div><div class="tm-row-chart-wrap"><div class="chart_wrap_outer"><div id="` + id + `" class="chart_wrap clearfix" style="min-height: 400px;"></div></div></div>`
}

func (s *Server) stickerStatsPage(r *http.Request, set media.Set, items []media.Item) (pageView, error) {
	isEmoji := set.Kind == media.SetEmoji
	words := packWording(isEmoji)
	id := strconv.FormatInt(set.ID, 10)
	st, err := s.Media.PackStatistics(r.Context(), set.ID, 10)
	if err != nil {
		return pageView{}, err
	}
	note := ""
	switch {
	case isEmoji:
		note = `<p class="help-text">Custom emoji sent in messages aren't counted yet, so Usage stays at 0 for emoji packs.</p>`
	case st.Uses == 0:
		note = `<p class="help-text">Usage is 0 because nobody has sent these stickers since sends started being counted.</p>`
	}
	installs := []statsLine{{"Installed", "#45AD59", st.InstallsByDay}}
	if st.Removed > 0 {
		installs = append(installs, statsLine{"Removed", "#E05356", st.RemovalsByDay})
	}
	byDoc := map[int64]media.Item{}
	for _, it := range items {
		byDoc[it.Doc.ID] = it
	}
	var top strings.Builder
	for _, t := range st.Top {
		it, ok := byDoc[t.Doc]
		if !ok {
			continue
		}
		uses := strconv.Itoa(t.Uses) + " uses"
		if t.Uses == 1 {
			uses = "1 use"
		}
		top.WriteString(`<a class="tm-row tm-row-link tm-sticker-row" href="/stickers/stats/` + id + `/` + strconv.FormatInt(t.Doc, 10) + `">` + stickerThumb(it.Doc, "tm-row-pic") + `<span>` + html.EscapeString(t.Emoji) + `</span><div class="grow-1"></div><span>` + uses + `</span></a>`)
	}
	topSection := ""
	if top.Len() > 0 {
		topSection = stickerSection("Top "+words.Upper, `<div class="tm-table-wrap tm-menu-items">`+top.String()+`</div>`)
	}
	markup := `<main class="` + stickerMainClass(set) + `"><section class="tm-main-intro"><div style="--icon: var(--image-url-stats)" class="tm-main-intro-picture tm-accent-icon"></div><h1 class="tm-main-intro-header">Statistics</h1><p class="tm-main-intro-text"` + stickerIntroWide + `>Get usage and installs statistics<br>for your ` + words.One + ` pack.</p></section>` +
		stickerSection("Overview", `<div class="tm-stat-summary"><div class="tm-stat-item"><div class="tm-stat-value">`+strconv.Itoa(st.Uses)+`</div><div class="tm-stat-description">Usage</div></div><div class="tm-stat-item"><div class="tm-stat-value">`+strconv.Itoa(st.Installed)+`</div><div class="tm-stat-description">Installed</div></div><div class="tm-stat-item"><div class="tm-stat-value">`+strconv.Itoa(st.Removed)+`</div><div class="tm-stat-description">Removed</div></div></div>`+note) +
		stickerSection("Usage", statsChart("chart_stickerset_use_wrap")) +
		stickerSection("Installed &amp; Removed", statsChart("chart_stickerset_installs_wrap")) +
		topSection + `</main>`
	state := map[string]any{"page": "stats", "isEmojis": isEmoji, "packId": id, "backButton": "/stickers/pack/" + id, "charts": []map[string]any{
		{"id": "chart_stickerset_use_wrap", "json": statsGraph(statsLine{"Uses", "#4CB2FF", st.UsesByDay})},
		{"id": "chart_stickerset_installs_wrap", "json": statsGraph(installs...)},
	}}
	return pageView{Title: "Stickers", Markup: markup, State: state}, nil
}

func (s *Server) stickerUsagePage(r *http.Request, set media.Set, items []media.Item, raw string) (pageView, error) {
	isEmoji := set.Kind == media.SetEmoji
	words := packWording(isEmoji)
	id := strconv.FormatInt(set.ID, 10)
	doc, _ := strconv.ParseInt(raw, 10, 64)
	var found *media.Document
	for i := range items {
		if items[i].Doc.ID == doc {
			found = &items[i].Doc
		}
	}
	if found == nil {
		return pageView{}, &account.Error{Status: 404, Message: "Sticker not found."}
	}
	days, err := s.Media.StickerUseDays(r.Context(), doc)
	if err != nil {
		return pageView{}, err
	}
	name := strings.ToUpper(words.One[:1]) + words.One[1:]
	markup := `<main class="` + stickerMainClass(set) + `"><section class="tm-main-intro">` + stickerThumb(*found, "tm-main-intro-picture") + `<h1 class="tm-main-intro-header">` + name + ` Statistics</h1><p class="tm-main-intro-text"` + stickerIntroWide + `>Get usage and installs statistics for ` + words.One + `.</p></section>` +
		stickerSection("Usage", statsChart("chart_sticker_send_wrap")) + `</main>`
	state := map[string]any{"page": "stats", "isEmojis": isEmoji, "packId": id, "backButton": "/stickers/stats/" + id, "charts": []map[string]any{
		{"id": "chart_sticker_send_wrap", "json": statsGraph(statsLine{"Uses", "#4CB2FF", days})},
	}}
	return pageView{Title: "Stickers", Markup: markup, State: state}, nil
}

func (s *Server) stickerList(r *http.Request, u account.User, emoji bool) (pageView, error) {
	packs, err := s.Media.OwnedPacks(r.Context(), u.ID)
	if err != nil {
		return pageView{}, err
	}
	selected := "stickers"
	if emoji {
		selected = "emojis"
	}
	var b strings.Builder
	b.WriteString(`<main class="tm-main"><section class="tm-main-intro"><div class="tm-main-intro-picture tm-main-intro-picture-round tm-stickers-bot-icon"></div><h1 class="tm-main-intro-header">Stickers &amp; Emojis</h1><p class="tm-main-intro-text"` + stickerIntroWide + `>Create ` + stickersAppName + ` Stickers and Emojis, and get usage stats for your stickers with this bot. <a href="` + stickerDocsLink + `" target="_blank" rel="noopener">Learn more</a></p></section>`)
	b.WriteString(`<form action="/" class="tm-section tm-main-search-form"><div class="tm-field-wrap"><div class="icon-before icon-search tm-field tm-search-field js-main-search-field"><input type="search" class="form-control tm-input tm-search-input" name="query" value="" placeholder="Search" autocomplete="off" spellcheck="false"><span class="icon-before icon-search-clear tm-search-clear js-form-clear"></span></div>` +
		`<div class="tm-field-buttons"><div data-scope="display" data-tab="list" class="js-tab-button active" style="--icon-s: var(--image-url-items-list)"></div><div data-scope="display" data-tab="grid" class="js-tab-button" style="--icon-s: var(--image-url-items-grid)"></div></div></div></form>`)
	b.WriteString(`<section class="tm-section"><div class="tm-section-header"><div class="tm-s-tabs" style="display: flex; gap: 12px">`)
	for _, t := range [][2]string{{"stickers", "Stickers"}, {"emojis", "Emojis"}} {
		active := ""
		if t[0] == selected {
			active = " active"
		}
		b.WriteString(`<h2 class="tm-section-header-text js-tab-button` + active + `" data-scope="type" data-tab="` + t[0] + `">` + t[1] + `</h2>`)
	}
	b.WriteString(`</div></div>`)
	for _, kind := range []string{"stickers", "emojis"} {
		hidden := ""
		if kind != selected {
			hidden = " hidden"
		}
		add, icon, setKind, path := "Create Sticker Pack", "--image-url-sticker-add", media.SetStickers, "/stickers/newpack"
		if kind == "emojis" {
			add, icon, setKind, path = "Create Emoji Pack", "--image-url-emoji-add", media.SetEmoji, "/stickers/newpack?emoji=1"
		}
		b.WriteString(`<div class="tm-table-wrap js-tab-content tm-pack-list` + hidden + `" data-tab="` + kind + `"><a class="tm-row tm-row-add" href="` + path + `"><span style="--icon-s: var(` + icon + `)" class="tm-icon"></span><span>` + add + `</span></a>`)
		for _, p := range packs {
			if p.Kind != setKind || p.Default || p.Official {
				continue
			}
			id := strconv.FormatInt(p.ID, 10)
			full := s.shareLink(r, p)
			short := strings.TrimPrefix(strings.TrimPrefix(full, "https://"), "http://")
			cover := `<span class="tm-row-pic tm-row-pic-cover"></span>`
			if docs, err := s.Media.Documents(r.Context(), []int64{p.ThumbDoc}); err == nil && len(docs) == 1 {
				cover = stickerThumb(docs[0], "tm-row-pic tm-row-pic-cover")
			}
			b.WriteString(`<div class="btn-group tm-dropdown"><a class="tm-row tm-dropdown-highlight" href="/stickers/pack/` + id + `" data-short-name="` + html.EscapeString(p.Name) + `">` + cover +
				`<div class="tm-row-info grow-1"><div class="tm-row-value">` + html.EscapeString(p.Title) + `</div><div class="tm-row-description">` + html.EscapeString(short) + `</div></div></a>` +
				`<div class="tm-icon tm-row-menu-toggle dropdown-toggle" data-toggle="dropdown"></div><ul class="dropdown-menu">` +
				`<li><a class="dropdown-menu-item icon-before icon-stats" href="/stickers/stats/` + id + `">Show Statistics</a></li>` +
				`<li><div class="dropdown-menu-item icon-before icon-copy js-link-copy" data-value="` + html.EscapeString(full) + `">Copy Link</div></li>` +
				`<li><a class="dropdown-menu-item icon-before icon-edit" href="/stickers/editpack/` + id + `">Edit</a></li>` +
				`<li><div class="dropdown-menu-item text-destructive icon-before icon-delete js-pack-delete" data-id="` + id + `">Delete</div></li></ul></div>`)
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</section><section class="tm-section"><div class="tm-table-wrap hidden js-results-empty"><div class="tm-row-container tm-row-results-empty"><img src="/__miniapps/stickers/img/no-result.png" width="56" height="56"><b>No Results</b><span class="js-results-empty-help"></span></div></div></section></main>`)
	state := map[string]any{"page": "list", "type": selected, "display": "list", "backButton": false}
	return pageView{Title: "Stickers", Markup: b.String(), State: state}, nil
}

func stickerEditor(emoji bool) pageView {
	words := packWording(emoji)
	mode, toggle := "", ""
	if emoji {
		mode = " tm-mode-emoji"
		toggle = `<div class="tm-table-wrap" style="margin-top: 8px;"><div class="tm-row tm-row-toggle"><span class="grow-1">Adaptive Color</span><div class="tm-toggle"><div class="tm-toggle-handle"></div></div></div></div><p class="help-text">The emoji takes on the color of the text around it.</p>`
	}
	markup := `<main class="tm-main` + mode + `"><section class="tm-main-intro"><div style="--icon: var(` + words.Icon + `)" class="tm-main-intro-picture tm-accent-icon"></div><h1 class="tm-main-intro-header">New ` + words.Set + `</h1><p class="tm-main-intro-text"` + stickerIntroWide + `>` + words.Upper + ` take many forms – from&nbsp;basic images to&nbsp;stunning&nbsp;vector animations. <a href="` + stickerDocsLink + `" target="_blank" rel="noopener">Read More</a></p></section>` +
		`<section class="tm-section">` + stickerNameField(words.NamePlaceholder, "") + `</section>` +
		`<section class="tm-section tm-newpack-upload"><div class="tm-section-header"><h2 class="tm-section-header-text">` + words.Upper + `</h2></div>` +
		`<div class="tm-sticker-drop-area js-drop-area js-dropdown-wrapper tm-newpack-empty"><span class="tm-icon" style="--icon-s: var(` + words.Icon + `)"></span><p>Upload your ` + words.Lower + ` by clicking or&nbsp;dragging one or more ` + words.Lower + ` into&nbsp;this area.</p>` +
		`<div class="tm-dropdown"><div class="set-photo-button dropdown-toggle tm-dropdown-highlight" data-toggle="dropdown">Add ` + words.Upper + `</div>` + packAddMenu("Upload "+words.Lower) + `</div></div>` +
		`<div class="tm-newpack-some"><div class="tm-dropdown"><div class="tm-row tm-row-add dropdown-toggle tm-dropdown-highlight" data-toggle="dropdown" style="margin-bottom: 8px; border-radius: 8px;"><span class="tm-icon" style="--icon-s: var(` + words.Icon + `)"></span><span>Add ` + words.Upper + `</span></div>` + packAddMenu("Upload "+words.Lower) + `</div>` +
		`<div class="tm-grid-area js-drop-area"></div></div>` + stickerZipReport +
		`<p class="help-text"><a href="` + stickerDocsLink + `" target="_blank" rel="noopener">Read more</a> about how to create ` + words.Lower + ` for ` + stickersAppName + `.</p>` + toggle + `</section>` +
		`<div class="tm-bottom-sheet-overlay hidden"><div class="tm-bottom-sheet-content"></div><div class="tm-bottom-sheet"><div class="tm-bottom-sheet-item tm-row-destructive js-delete-sticker"><div class="tm-icon" style="--icon-s: var(--image-url-trash)"></div>Delete ` + strings.ToUpper(words.One[:1]) + words.One[1:] + `</div></div></div></main>`
	state := map[string]any{"page": "editor", "type": "stickers", "isEmojis": emoji, "editing": false, "backButton": "/stickers"}
	if emoji {
		state["type"] = "emojis"
	}
	return pageView{Title: "Stickers", Markup: markup, State: state}
}

// stickerUser checks the Stickers launch data for the API and uploads.
func (s *Server) stickerUser(r *http.Request) (account.User, error) {
	l, err := s.Bots.Launched(r.Context(), bots.Stickers, r.Header.Get("X-Telegram-Init-Data"))
	if err != nil {
		return account.User{}, &account.Error{Status: 401, Message: "Session expired. Restart the app to continue."}
	}
	u, ok, err := s.Accounts.User(r.Context(), l.User)
	if err != nil {
		return account.User{}, err
	}
	if !ok || u.Disabled || u.IsSystem || u.IsBot {
		return account.User{}, &account.Error{Status: 401, Message: "Session expired. Restart the app to continue."}
	}
	if r.Method != http.MethodGet && u.Frozen() {
		return account.User{}, account.ErrFrozen
	}
	return u, nil
}

func answer(w http.ResponseWriter, what string, v any, err error) {
	var refusal *account.Error
	switch {
	case errors.As(err, &refusal):
		reply(w, refusal.Status, map[string]any{"ok": false, "error": refusal.Message})
	case err != nil:
		slog.Error("miniapps: "+what, "err", err)
		fail(w, 500, "Something went wrong. Try again.")
	default:
		reply(w, 200, v)
	}
}

func (s *Server) stickerUpload(w http.ResponseWriter, r *http.Request) {
	u, err := s.stickerUser(r)
	if err != nil {
		answer(w, "stickers upload", nil, err)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, media.MaxStickerBytes+1))
	if err != nil {
		fail(w, 400, "Can't read the upload.")
		return
	}
	d, err := s.Media.UploadSticker(r.Context(), u.ID, r.URL.Query().Get("kind") == "emoji", data)
	if err != nil {
		answer(w, "stickers upload", nil, err)
		return
	}
	id := strconv.FormatInt(d.ID, 10)
	reply(w, 200, map[string]any{"ok": true, "doc": id, "doc_id": id, "thumb": stickerURL(d.ID)})
}

func docID(v any) int64 {
	switch x := v.(type) {
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	case float64:
		return int64(x)
	}
	return 0
}

// stickersAPI is stickers.js manageStickerSet(), signed in by initData.
func (s *Server) stickersAPI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
	var data map[string]any
	if err != nil || len(raw) > 0 && json.Unmarshal(raw, &data) != nil {
		fail(w, 400, "Send a JSON object.")
		return
	}
	u, err := s.stickerUser(r)
	if err != nil {
		answer(w, "stickers", nil, err)
		return
	}
	str := func(k string) string { v, _ := data[k].(string); return v }
	method := str("method")
	switch method {
	case "auth":
		reply(w, 200, map[string]any{"ok": true})
	case "getEmojiKeywords":
		// from the bundle; without it the emoji search has no words to go on
		raw, err := s.extraStatic("stickers-emoji-keywords.json")
		if err != nil || !json.Valid(raw) {
			raw = []byte("{}")
		}
		answer(w, "stickers "+method, map[string]any{"keywords": json.RawMessage(raw)}, nil)
	case "checkShortName":
		if err := s.Media.CheckPackName(ctx, str("value")); err != nil {
			var refusal *account.Error
			if errors.As(err, &refusal) {
				reply(w, 200, map[string]any{"ok": false, "error": refusal.Message})
				return
			}
			answer(w, "stickers "+method, nil, err)
			return
		}
		reply(w, 200, map[string]any{"ok": true})
	case "getStickerPicker":
		out, err := s.stickerPicker(ctx, u, data["emojis"] == true || data["emojis"] == "1")
		answer(w, "stickers "+method, map[string]any{"ok": true, "html": out}, err)
	case "getStickerHtml":
		d, _, err := s.Media.StickerFile(ctx, docID(data["doc_id"]))
		if err == nil && d.Owner != u.ID {
			// not this person's upload; fine if it sits in a pack
			err = s.Media.InPack(ctx, d.ID)
		}
		answer(w, "stickers "+method, map[string]any{"ok": true, "html": stickerPreview(d)}, err)
	case "createStickerSet", "updateStickerSet":
		// the page asks for the link only when publishing; no name means a draft
		e := media.PackEdit{Title: str("title"), Name: str("short_name"), Draft: str("short_name") == "", Emoji: data["is_emoji"] == true || data["is_emoji"] == "1"}
		if method == "updateStickerSet" {
			if e.ID = docID(data["pack_id"]); e.ID == 0 {
				fail(w, 404, "Pack not found.")
				return
			}
		}
		list, _ := data["stickers"].([]any)
		for _, v := range list {
			m, _ := v.(map[string]any)
			id := docID(m["doc_id"])
			if id == 0 {
				id = docID(m["sticker_raw"])
			}
			emoji, _ := m["emoji"].(string)
			e.Stickers = append(e.Stickers, media.PackSticker{Doc: id, Emoji: emoji})
		}
		e.Thumb = docID(data["thumb"])
		switch v := data["text_color"].(type) {
		case bool:
			e.TextColor = &v
		case string:
			on := v == "1" || v == "true"
			if v != "" || method == "createStickerSet" {
				e.TextColor = &on
			}
		}
		set, err := s.Media.SavePack(ctx, u.ID, e)
		id := strconv.FormatInt(set.ID, 10)
		redirect := "/stickers/pack/" + id
		if method == "createStickerSet" && set.Draft {
			redirect = "/stickers/publishpack/" + id
		}
		answer(w, "stickers "+method, map[string]any{"ok": true, "redirect": redirect, "pack_id": id, "msg": "Pack saved."}, err)
	case "publishStickerSet":
		set, err := s.Media.PublishPack(ctx, u.ID, docID(data["pack_id"]), str("title"), str("short_name"))
		answer(w, "stickers "+method, map[string]any{"ok": true, "redirect": "/stickers/pack/" + strconv.FormatInt(set.ID, 10), "msg": "Pack published."}, err)
	case "deleteStickerSet":
		err := s.Media.DeletePack(ctx, u.ID, docID(data["pack_id"]))
		answer(w, "stickers "+method, map[string]any{"ok": true, "msg": "Pack deleted."}, err)
	default:
		fail(w, 501, "Stickers operation is not available: "+method)
	}
}

// stickerPicker lists the person's packs of one kind to pick from.
func (s *Server) stickerPicker(ctx context.Context, u account.User, emoji bool) (string, error) {
	kind := media.SetStickers
	if emoji {
		kind = media.SetEmoji
	}
	sets, err := s.Media.Installed(ctx, u.ID, kind)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i, set := range sets {
		if i == 30 {
			break
		}
		_, items, ok, err := s.Media.Pack(ctx, u.ID, strconv.FormatInt(set.ID, 10))
		if err != nil {
			return "", err
		}
		if !ok {
			continue
		}
		hidden := ""
		if i >= 3 {
			// bot-stickers.js uncovers the next ones as the list scrolls
			hidden = " hidden"
		}
		// a div, as in eSearchInput: the CSS makes a <section> a centered flex column
		b.WriteString(`<div class="tm-section tm-sticker-picker-pack` + hidden + `"><div class="tm-section-header"><h2 class="tm-section-header-text">` + html.EscapeString(set.Title) + `</h2></div><div class="tm-sticker-picker-grid">`)
		for _, it := range items {
			id := strconv.FormatInt(it.Doc.ID, 10)
			src := html.EscapeString(stickerURL(it.Doc.ID))
			pic := `<img class="tm-sticker-picker-item-img" src="` + src + `" loading="lazy">`
			switch it.Doc.Mime {
			case "application/x-tgsticker":
				pic = `<picture class="tm-sticker-picker-item-img js-lottie-thumb">` + stickerSource(src) + `</picture>`
			case "video/webm":
				pic = `<video class="tm-sticker-picker-item-img" src="` + src + `" muted playsinline preload="metadata"></video>`
			}
			b.WriteString(`<div class="tm-sticker-picker-item js-sticker-picker-item" data-raw="` + id + `" data-doc="` + id + `" data-emoji="` + html.EscapeString(it.Emoji) + `" data-thumb="` + src + `" data-mime="` + html.EscapeString(it.Doc.Mime) + `">` + pic + `</div>`)
		}
		b.WriteString(`</div></div>`)
	}
	// eSearchInput reads the list as a direct child of what it was handed
	return `<div class="popup-container tm-sticker-picker"><div class="tm-section"><div class="icon-before icon-search tm-field tm-search-field"><input type="search" class="form-control tm-input tm-search-input" name="query" placeholder="Search" autocomplete="off" spellcheck="false"></div></div><div class="js-sticker-picker-items">` +
		b.String() + `</div><div class="tm-sticker-picker-results-empty help-text">No installed packs found.</div></div>`, nil
}
