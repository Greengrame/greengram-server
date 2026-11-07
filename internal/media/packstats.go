package media

import (
	"context"
)

// What the Stickers app's statistics pages show. Sends are counted as they
// happen (sticker_uses). Installs come from user_sticker_sets, which holds one
// row per person with their last change, so a reinstall moves it to its new day
// instead of adding one.

// DayCounts is a count per UTC day number (unix time / 86400).
type DayCounts map[int]int

// StickerUses is one sticker's total sends.
type StickerUses struct {
	Doc   int64
	Emoji string
	Uses  int
}

// PackStats is one pack's numbers.
type PackStats struct {
	Uses, Installed, Removed int
	UsesByDay                DayCounts
	InstallsByDay            DayCounts
	RemovalsByDay            DayCounts
	Top                      []StickerUses
}

// NoteStickerUse counts one send of a sticker, today.
func (s *Service) NoteStickerUse(ctx context.Context, doc int64) error {
	_, err := s.db.Exec(ctx, `INSERT INTO sticker_uses(document_id, day, uses) VALUES ($1, $2, 1)
		ON CONFLICT (document_id, day) DO UPDATE SET uses = sticker_uses.uses + 1`, doc, s.unix()/86400)
	return err
}

// StickerUseDays is one sticker's sends per day.
func (s *Service) StickerUseDays(ctx context.Context, doc int64) (DayCounts, error) {
	return s.dayCounts(ctx, "SELECT day, uses FROM sticker_uses WHERE document_id=$1", doc)
}

func (s *Service) dayCounts(ctx context.Context, query string, id int64) (DayCounts, error) {
	rows, err := s.db.Query(ctx, query, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := DayCounts{}
	for rows.Next() {
		var day, n int
		if err := rows.Scan(&day, &n); err != nil {
			return nil, err
		}
		out[day] = n
	}
	return out, rows.Err()
}

// PackStatistics adds up a pack's sends and installs. top is how many of its
// most sent stickers to list.
func (s *Service) PackStatistics(ctx context.Context, set int64, top int) (PackStats, error) {
	var out PackStats
	var err error
	if out.UsesByDay, err = s.dayCounts(ctx, `SELECT u.day, sum(u.uses)::int FROM sticker_uses u
		JOIN sticker_set_items i ON i.document_id = u.document_id WHERE i.set_id=$1 GROUP BY u.day`, set); err != nil {
		return out, err
	}
	if out.InstallsByDay, err = s.dayCounts(ctx, `SELECT date/86400, count(*)::int FROM user_sticker_sets WHERE set_id=$1 AND installed GROUP BY 1`, set); err != nil {
		return out, err
	}
	if out.RemovalsByDay, err = s.dayCounts(ctx, `SELECT date/86400, count(*)::int FROM user_sticker_sets WHERE set_id=$1 AND NOT installed GROUP BY 1`, set); err != nil {
		return out, err
	}
	for _, n := range out.UsesByDay {
		out.Uses += n
	}
	for _, n := range out.InstallsByDay {
		out.Installed += n
	}
	for _, n := range out.RemovalsByDay {
		out.Removed += n
	}
	rows, err := s.db.Query(ctx, `SELECT i.document_id, i.emoji, sum(u.uses)::int FROM sticker_uses u
		JOIN sticker_set_items i ON i.document_id = u.document_id WHERE i.set_id=$1
		GROUP BY i.document_id, i.emoji ORDER BY 3 DESC, i.document_id LIMIT $2`, set, top)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v StickerUses
		if err := rows.Scan(&v.Doc, &v.Emoji, &v.Uses); err != nil {
			return out, err
		}
		out.Top = append(out.Top, v)
	}
	return out, rows.Err()
}
