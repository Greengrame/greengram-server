package media

import (
	"context"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// What anyone may browse: published packs, plus the viewer's own drafts.
// $1 is the viewer.
const browsable = `s.builtin IS NULL AND s.enabled AND (NOT s.draft OR s.owner_id = $1) AND (` + premiumAccess + `)`

func (s *Service) sets(ctx context.Context, q string, args ...any) ([]Set, error) {
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Set
	for rows.Next() {
		v, err := scanSet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Featured are the instance's public packs of a kind, most installed first:
// messages.getFeaturedStickers' trending list.
func (s *Service) Featured(ctx context.Context, user int64, kind string, limit int) ([]Set, error) {
	return s.sets(ctx, "SELECT "+setCols+" FROM "+setFrom+" WHERE s.kind=$2 AND NOT s.draft AND "+browsable+`
		ORDER BY (SELECT count(*) FROM user_sticker_sets p WHERE p.set_id = s.id AND p.installed) DESC, s.id LIMIT $3`, user, kind, limit)
}

// SearchSets finds packs of a kind by title or short name, installed ones first.
func (s *Service) SearchSets(ctx context.Context, user int64, kind, q string, limit int) ([]Set, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil, nil
	}
	return s.sets(ctx, "SELECT * FROM (SELECT "+setCols+" FROM "+setFrom+" WHERE s.kind=$2 AND "+browsable+`
		AND (strpos(lower(s.title), $3) > 0 OR strpos(lower(s.short_name), $3) > 0)) x
		ORDER BY x.installed_date > 0 DESC, lower(x.title), x.id LIMIT $4`, user, kind, q, limit)
}

// Covers are the first n stickers of each set, for stickerSetMultiCovered.
func (s *Service) Covers(ctx context.Context, sets []int64, n int) (map[int64][]Document, error) {
	out := map[int64][]Document{}
	if len(sets) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT set_id, document_id FROM (SELECT set_id, document_id,
		row_number() OVER (PARTITION BY set_id ORDER BY position) AS n FROM sticker_set_items WHERE set_id = ANY($1)) x
		WHERE x.n <= $2 ORDER BY set_id, n`, sets, n)
	if err != nil {
		return nil, err
	}
	type pair struct{ set, doc int64 }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pair, error) {
		var p pair
		return p, r.Scan(&p.set, &p.doc)
	})
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(list))
	for i, p := range list {
		ids[i] = p.doc
	}
	docs, err := s.Documents(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]Document, len(docs))
	for _, d := range docs {
		byID[d.ID] = d
	}
	for _, p := range list {
		if d, ok := byID[p.doc]; ok {
			out[p.set] = append(out[p.set], d)
		}
	}
	return out, nil
}

// SearchStickers finds stickers (or custom emoji) whose emoji is one of
// emoji's, or that sit in a pack whose title has q. More is true when
// there are results past offset+limit.
func (s *Service) SearchStickers(ctx context.Context, user int64, kind, q, emoji string, offset, limit int) (items []Item, more bool, err error) {
	tokens := EmojiTokens(emoji + q)
	q = strings.ToLower(strings.TrimSpace(q))
	if len(tokens) == 0 && q == "" {
		return nil, false, nil
	}
	if tokens == nil {
		tokens = []string{}
	}
	rows, err := s.db.Query(ctx, `SELECT i.document_id, i.emoji FROM sticker_set_items i JOIN sticker_sets s ON s.id = i.set_id
		LEFT JOIN user_sticker_sets u ON u.set_id = s.id AND u.user_id = $1
		WHERE s.kind=$2 AND `+browsable+`
		AND (EXISTS (SELECT 1 FROM unnest($3::text[]) t WHERE strpos(replace(i.emoji, chr(65039), ''), t) > 0)
			OR ($4 <> '' AND strpos(lower(s.title), $4) > 0))
		ORDER BY (u.installed OR (u.user_id IS NULL AND s.is_default)) IS TRUE DESC, s.id, i.position OFFSET $5 LIMIT $6`,
		user, kind, tokens, q, offset, limit+1)
	if err != nil {
		return nil, false, err
	}
	items, err = s.collectItems(ctx, rows)
	if err != nil {
		return nil, false, err
	}
	if len(items) > limit {
		return items[:limit], true, nil
	}
	return items, false, nil
}

// Reorder puts the user's installed sets of a kind in order's order; the
// panel lists the newest install first, so it rewrites install dates.
// Ids that aren't installed are skipped, unlisted sets go after.
func (s *Service) Reorder(ctx context.Context, user int64, kind string, order []int64) error {
	have, err := s.Installed(ctx, user, kind)
	if err != nil {
		return err
	}
	installed := make(map[int64]bool, len(have))
	for _, v := range have {
		installed[v.ID] = true
	}
	now := s.unix()
	return s.tx(ctx, func(tx pgx.Tx) error {
		n := 0
		for _, id := range order {
			if !installed[id] {
				continue
			}
			delete(installed, id) // a repeated id keeps its first place
			if _, err := tx.Exec(ctx, `INSERT INTO user_sticker_sets(user_id, set_id, installed, date) VALUES ($1, $2, true, $3)
				ON CONFLICT (user_id, set_id) DO UPDATE SET installed = true, date = excluded.date`, user, id, now-n); err != nil {
				return err
			}
			n++
		}
		return nil
	})
}

// EmojiTokens splits text into its emoji, without variation selectors: a
// ZWJ sequence, a flag or a skin-toned emoji stays one token. Anything else
// is dropped.
func EmojiTokens(text string) []string {
	var out []string
	var cur []rune
	joined := false
	flush := func() {
		if len(cur) > 0 && isEmoji(cur[0]) {
			out = append(out, string(cur))
		}
		cur = cur[:0]
	}
	for _, r := range text {
		switch {
		case r == 0xFE0F:
			continue
		case r == 0x200D:
			if len(cur) > 0 {
				cur = append(cur, r)
				joined = true
			}
			continue
		case joined, r >= 0x1F3FB && r <= 0x1F3FF, r == 0x20E3, r >= 0xE0020 && r <= 0xE007F,
			isRegional(r) && len(cur) == 1 && isRegional(cur[0]):
			if len(cur) > 0 {
				cur = append(cur, r)
				joined = false
				continue
			}
		}
		flush()
		joined = false
		cur = append(cur, r)
	}
	flush()
	return out
}

func isRegional(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

func isEmoji(r rune) bool {
	return r >= 0x1F000 || unicode.Is(unicode.So, r) || r == 0x00A9 || r == 0x00AE || r >= 0x2190 && r <= 0x2BFF
}
