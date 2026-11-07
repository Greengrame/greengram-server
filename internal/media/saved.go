package media

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
)

// Saved document lists, as stored in saved_documents.list, with how many
// each keeps (Telegram's saved_gifs_limit, stickers_recent_limit and
// stickers_faved_limit; premium doubles the last).
const (
	ListGIFs   = "gifs"
	ListRecent = "recent"
	ListFaved  = "faved"
)

var listLimit = map[string]int{ListGIFs: 200, ListRecent: 20, ListFaved: 5}

// Saved is one entry of a list.
type Saved struct {
	Doc  Document
	Date int
}

// SavedDocs is a user's list, newest first.
func (s *Service) SavedDocs(ctx context.Context, user int64, list string) ([]Saved, error) {
	rows, err := s.db.Query(ctx, "SELECT "+savedDocCols+", v.date FROM saved_documents v JOIN documents d ON d.id = v.document_id"+
		" WHERE v.user_id=$1 AND v.list=$2 ORDER BY v.pos DESC", user, list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Saved
	for rows.Next() {
		var (
			v      Saved
			attrs  []byte
			tkey   *string
			tw, th *int
			tsize  *int
		)
		d := &v.Doc
		if err := rows.Scan(&d.ID, &d.AccessHash, &d.Owner, &d.Date, &d.Mime, &d.Kind, &d.Key, &d.Size, &attrs, &tkey, &tw, &th, &tsize, &v.Date); err != nil {
			return nil, err
		}
		if tkey != nil {
			d.Thumb = &Size{Type: "m", Key: *tkey, W: deref(tw), H: deref(th), Bytes: deref(tsize)}
		}
		if d.Attrs, err = decodeAttrs(attrs); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}


const savedDocCols = "d.id, d.access_hash, coalesce(d.owner_id, 0), d.date, d.mime, d.kind, d.key, d.size, d.attributes, d.thumb_key, d.thumb_w, d.thumb_h, d.thumb_size"

// Save puts a document at the front of a list, or takes it out. The list
// keeps its newest `limit` entries (0 = the list's own limit). false when
// nothing changed.
func (s *Service) Save(ctx context.Context, user int64, list string, doc int64, on bool, limit int) (bool, error) {
	if limit <= 0 {
		limit = listLimit[list]
	}
	changed := false
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if !on {
			tag, err := tx.Exec(ctx, "DELETE FROM saved_documents WHERE user_id=$1 AND list=$2 AND document_id=$3", user, list, doc)
			changed = tag.RowsAffected() > 0
			return err
		}
		// microseconds keep two saves in one second apart
		tag, err := tx.Exec(ctx, `INSERT INTO saved_documents(user_id, list, document_id, date, pos)
			VALUES ($1, $2, $3, $4, (extract(epoch FROM clock_timestamp()) * 1000000)::bigint)
			ON CONFLICT (user_id, list, document_id) DO UPDATE SET date = excluded.date, pos = excluded.pos`, user, list, doc, s.unix())
		if err != nil {
			return err
		}
		changed = tag.RowsAffected() > 0
		_, err = tx.Exec(ctx, `DELETE FROM saved_documents WHERE user_id=$1 AND list=$2 AND document_id IN
			(SELECT document_id FROM saved_documents WHERE user_id=$1 AND list=$2 ORDER BY pos DESC OFFSET $3)`, user, list, limit)
		return err
	})
	return changed, err
}

// ClearSaved empties a list.
func (s *Service) ClearSaved(ctx context.Context, user int64, list string) error {
	_, err := s.db.Exec(ctx, "DELETE FROM saved_documents WHERE user_id=$1 AND list=$2", user, list)
	return err
}

// SavedHash is the list's hash for the *NotModified answers: the
// document ids in order, as Telegram computes it.
func SavedHash(list []Saved) int64 {
	vals := make([]int64, 0, len(list))
	for _, v := range list {
		vals = append(vals, v.Doc.ID)
	}
	return ListHash(vals)
}

// StickerPacks groups stickers by the emoji they stand for, the packs
// field of the recent and faved lists.
func StickerPacks(list []Saved) []tg.StickerPack {
	out := []tg.StickerPack{}
	at := map[string]int{}
	for _, v := range list {
		for _, a := range v.Doc.Attrs {
			st, ok := a.(*tg.DocumentAttributeSticker)
			if !ok || st.Alt == "" {
				continue
			}
			i, ok := at[st.Alt]
			if !ok {
				i = len(out)
				at[st.Alt] = i
				out = append(out, tg.StickerPack{Emoticon: st.Alt})
			}
			out[i].Documents = append(out[i].Documents, v.Doc.ID)
		}
	}
	return out
}
