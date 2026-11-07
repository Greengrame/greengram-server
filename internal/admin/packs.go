package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/media"
)

// The panel's Stickers & Emoji page, admin-stickers.js's routes. Set and
// document ids are 63-bit, so they cross JSON as strings. Built-in sets
// (topic icons and such) aren't listed: the apps need them.

const packPageSize = 20

const packCols = `s.id, s.title, s.short_name, s.kind, coalesce(s.owner_id, 0), s.official, s.premium_only, s.is_default, s.enabled,
	(SELECT count(*) FROM sticker_set_items i WHERE i.set_id = s.id),
	coalesce(cover.id, 0), coalesce(cover.mime, '')`

const packFrom = `sticker_sets s LEFT JOIN LATERAL (
	SELECT d.id, d.mime FROM sticker_set_items i JOIN documents d ON d.id = i.document_id
	WHERE i.set_id = s.id ORDER BY i.document_id = s.thumb_document_id DESC, i.position LIMIT 1) cover ON true`

func scanPack(r pgx.Row) (map[string]any, error) {
	var id, owner, count, cover int64
	var title, name, kind, mime string
	var official, premium, isDefault, enabled bool
	if err := r.Scan(&id, &title, &name, &kind, &owner, &official, &premium, &isDefault, &enabled, &count, &cover, &mime); err != nil {
		return nil, err
	}
	if kind == media.SetStickers {
		kind = "sticker"
	}
	out := map[string]any{"id": strconv.FormatInt(id, 10), "title": title, "shortName": name, "kind": kind, "ownerId": nilIfZero(owner),
		"count": count, "isOfficial": official, "premiumOnly": premium, "isDefault": isDefault, "enabled": enabled}
	if cover != 0 {
		out["cover"] = map[string]any{"url": "/api/sticker-media/" + strconv.FormatInt(cover, 10), "mime": mime}
	}
	return out, nil
}

func nilIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func (a *API) stickerPacks(q *req) (any, error) {
	v := q.r.URL.Query()
	kind := v.Get("kind")
	switch kind {
	case "":
	case "sticker":
		kind = media.SetStickers
	case "emoji":
		kind = media.SetEmoji
	default:
		return nil, fail(400, "Choose a valid pack type and page.")
	}
	page := 1
	if s := v.Get("page"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return nil, fail(400, "Choose a valid pack type and page.")
		}
		page = n
	}
	search := v.Get("q")
	if len(search) > 100 {
		search = search[:100]
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(search) + "%"
	const where = ` WHERE s.builtin IS NULL AND ($1 = '' OR s.kind = $1) AND (s.title ILIKE $2 OR s.short_name ILIKE $2)`
	var total int
	if err := a.DB.QueryRow(q.ctx, "SELECT count(*) FROM sticker_sets s"+where, kind, like).Scan(&total); err != nil {
		return nil, err
	}
	if last := max(1, (total+packPageSize-1)/packPageSize); page > last {
		page = last
	}
	rows, err := a.DB.Query(q.ctx, "SELECT "+packCols+" FROM "+packFrom+where+" ORDER BY s.created_at DESC, s.id DESC LIMIT $3 OFFSET $4",
		kind, like, packPageSize, (page-1)*packPageSize)
	if err != nil {
		return nil, err
	}
	packs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) { return scanPack(r) })
	if err != nil {
		return nil, err
	}
	return map[string]any{"packs": packs, "total": total, "page": page, "pageSize": packPageSize}, nil
}

// stickerPackItems is one pack's pictures, for picking a gift's custom emoji.
func (a *API) stickerPackItems(q *req) (any, error) {
	id, err := q.packID()
	if err != nil {
		return nil, err
	}
	var found bool
	if err := a.DB.QueryRow(q.ctx, "SELECT EXISTS(SELECT 1 FROM sticker_sets WHERE id=$1 AND builtin IS NULL)", id).Scan(&found); err != nil {
		return nil, err
	}
	if !found {
		return nil, fail(404, "Sticker or emoji pack not found.")
	}
	rows, err := a.DB.Query(q.ctx, `SELECT d.id, i.emoji, d.mime FROM sticker_set_items i JOIN documents d ON d.id = i.document_id
		WHERE i.set_id = $1 ORDER BY i.position`, id)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) {
		var doc int64
		var emoji, mime string
		if err := r.Scan(&doc, &emoji, &mime); err != nil {
			return nil, err
		}
		ref := strconv.FormatInt(doc, 10)
		return map[string]any{"id": ref, "emoji": emoji, "mime": mime, "url": "/api/sticker-media/" + ref}, nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items}, nil
}

func (q *req) packID() (int64, error) {
	id, err := strconv.ParseInt(q.r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, fail(404, "Sticker or emoji pack not found.")
	}
	return id, nil
}

func (a *API) editStickerPack(q *req) (any, error) {
	id, err := q.packID()
	if err != nil {
		return nil, err
	}
	columns := map[string]string{"premiumOnly": "premium_only", "isDefault": "is_default", "enabled": "enabled"}
	if len(q.body) == 0 {
		return nil, fail(400, "Choose valid pack settings.")
	}
	for k := range q.body {
		if _, isBool := q.boolean(k); !isBool || columns[k] == "" {
			return nil, fail(400, "Choose valid pack settings.")
		}
	}
	var out map[string]any
	err = a.tx(q.ctx, func(tx pgx.Tx) error {
		for k, col := range columns {
			if !q.has(k) {
				continue
			}
			v, _ := q.boolean(k)
			tag, err := tx.Exec(q.ctx, "UPDATE sticker_sets SET "+col+"=$2 WHERE id=$1 AND builtin IS NULL", id, v)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return fail(404, "Sticker or emoji pack not found.")
			}
		}
		var err error
		if out, err = scanPack(tx.QueryRow(q.ctx, "SELECT "+packCols+" FROM "+packFrom+" WHERE s.id=$1", id)); err != nil {
			return err
		}
		return audit(q.ctx, tx, q.me.ID, "update_sticker_pack", id, q.body)
	})
	return out, err
}

// deleteStickerPack takes the pack from every account. Documents stay, sent
// stickers keep working; nobody keeps wearing its emoji as a status.
func (a *API) deleteStickerPack(q *req) (any, error) {
	id, err := q.packID()
	if err != nil {
		return nil, err
	}
	err = a.tx(q.ctx, func(tx pgx.Tx) error {
		var title, name, kind string
		err := tx.QueryRow(q.ctx, "SELECT title, short_name, kind FROM sticker_sets WHERE id=$1 AND builtin IS NULL FOR UPDATE", id).Scan(&title, &name, &kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(404, "Sticker or emoji pack not found.")
		}
		if err != nil {
			return err
		}
		if kind == media.SetEmoji {
			const docs = "(SELECT document_id FROM sticker_set_items WHERE set_id=$1)"
			if _, err := tx.Exec(q.ctx, "UPDATE users SET emoji_status_id=NULL, emoji_status_until=NULL WHERE emoji_status_id IN "+docs, id); err != nil {
				return err
			}
			if _, err := tx.Exec(q.ctx, "DELETE FROM recent_emoji_statuses WHERE document_id IN "+docs, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(q.ctx, "DELETE FROM sticker_sets WHERE id=$1", id); err != nil {
			return err
		}
		return audit(q.ctx, tx, q.me.ID, "delete_sticker_pack", id, map[string]any{"title": title, "shortName": name, "kind": kind})
	})
	if err != nil {
		return nil, err
	}
	return ok(), nil
}

var panelImages = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true, "image/svg+xml": true}

// the gift form's picker draws these too; only stickers and emoji get here
var panelStickers = map[string]bool{"application/x-tgsticker": true, "video/webm": true}

func (a *API) stickerFile(q *req) ([]byte, string, error) {
	id, err := strconv.ParseInt(q.r.PathValue("id"), 10, 64)
	if err != nil {
		return nil, "", fail(404, "Sticker not found.")
	}
	doc, data, err := a.Media.StickerFile(q.ctx, id)
	if err != nil {
		return nil, "", err
	}
	if !panelImages[doc.Mime] && !panelStickers[doc.Mime] {
		return nil, "", fail(404, "Sticker not found.")
	}
	return data, doc.Mime, nil
}

// photo is a member's or bot's picture for the panel's lists.
func (a *API) photo(q *req) ([]byte, string, error) {
	id, err := strconv.ParseInt(q.r.PathValue("id"), 10, 64)
	if err != nil {
		return nil, "", fail(404, "Photo not found.")
	}
	data, err := a.Media.PhotoFile(q.ctx, id, 160)
	if err != nil {
		return nil, "", err
	}
	mime := http.DetectContentType(data)
	if !panelImages[mime] {
		return nil, "", fail(404, "Photo not found.")
	}
	return data, mime, nil
}

