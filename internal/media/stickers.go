package media

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
)

var ErrSetInvalid = tgerr.New(400, "STICKERSET_INVALID")

// Set kinds, as stored in sticker_sets.kind.
const (
	SetStickers = "stickers"
	SetEmoji    = "emoji"
)

// Set is a sticker set as one user sees it.
type Set struct {
	ID, AccessHash int64
	Name, Title    string
	Kind           string
	Official       bool
	TextColor      bool
	Default        bool
	Builtin        string // inputStickerSet* type name it answers for
	ThumbDoc       int64
	Hash           int
	Count          int
	Installed      int   // when the user installed it; 0 = not installed
	Owner          int64 // who made it in the Stickers app; 0 = seeded
	Draft          bool  // made in the Stickers app, not published yet
}

// Item is one sticker of a set.
type Item struct {
	Doc   Document
	Emoji string
}

// $1 is always the viewing user. A default set the user never touched
// counts as installed on the day it was seeded.
const setFrom = `sticker_sets s LEFT JOIN user_sticker_sets u ON u.set_id = s.id AND u.user_id = $1`

const setCols = `s.id, s.access_hash, s.short_name, s.title, s.kind, s.official, s.text_color, s.is_default, coalesce(s.builtin, ''),
	coalesce(s.thumb_document_id, 0), s.hash, (SELECT count(*) FROM sticker_set_items i WHERE i.set_id = s.id),
	CASE WHEN u.installed THEN u.date WHEN u.user_id IS NULL AND s.is_default THEN s.created_at ELSE 0 END AS installed_date, coalesce(s.owner_id, 0), s.draft`

func scanSet(row pgx.Row) (Set, error) {
	var v Set
	err := row.Scan(&v.ID, &v.AccessHash, &v.Name, &v.Title, &v.Kind, &v.Official, &v.TextColor, &v.Default, &v.Builtin,
		&v.ThumbDoc, &v.Hash, &v.Count, &v.Installed, &v.Owner, &v.Draft)
	return v, err
}

// StickerSet resolves any inputStickerSet and loads its stickers.
func (s *Service) StickerSet(ctx context.Context, user int64, in tg.InputStickerSetClass) (Set, []Item, bool, error) {
	var (
		where string
		args  = []any{user}
	)
	switch v := in.(type) {
	case *tg.InputStickerSetEmpty:
		return Set{}, nil, false, nil
	case *tg.InputStickerSetID:
		where, args = "s.id=$2 AND s.access_hash=$3", append(args, v.ID, v.AccessHash)
	case *tg.InputStickerSetShortName:
		// a draft's link is its maker's; a sticker already sent still names its set by id
		where, args = "lower(s.short_name)=lower($2) AND (NOT s.draft OR s.owner_id = $1)", append(args, v.ShortName)
	default:
		where, args = "s.builtin=$2", append(args, in.TypeName())
	}
	// an unpublished pack is gone for everyone but its maker
	set, err := scanSet(s.db.QueryRow(ctx, "SELECT "+setCols+" FROM "+setFrom+" WHERE "+where+" AND (s.enabled OR s.owner_id = $1)", args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return set, nil, false, nil
	}
	if err != nil {
		return set, nil, false, err
	}
	items, err := s.items(ctx, set.ID)
	return set, items, true, err
}

func (s *Service) items(ctx context.Context, set int64) ([]Item, error) {
	rows, err := s.db.Query(ctx, "SELECT document_id, emoji FROM sticker_set_items WHERE set_id=$1 ORDER BY position", set)
	if err != nil {
		return nil, err
	}
	return s.collectItems(ctx, rows)
}

// DefaultStatusSet answers inputStickerSetEmojiDefaultStatuses: the cover of
// every default pack, so it follows whatever packs the instance has. Web A
// shows the clear-status star and the recents only when this has stickers.
func (s *Service) DefaultStatusSet(ctx context.Context) (Set, []Item, error) {
	rows, err := s.db.Query(ctx, `SELECT i.document_id, i.emoji FROM sticker_sets s
		JOIN sticker_set_items i ON i.set_id = s.id AND i.document_id = s.thumb_document_id
		WHERE s.kind = $1 AND s.is_default AND s.enabled AND s.builtin IS NULL ORDER BY lower(s.short_name)`, SetEmoji)
	if err != nil {
		return Set{}, nil, err
	}
	items, err := s.collectItems(ctx, rows)
	if err != nil {
		return Set{}, nil, err
	}
	sum := crc32.NewIEEE()
	for _, it := range items {
		binary.Write(sum, binary.LittleEndian, it.Doc.ID)
	}
	return Set{ID: seedID("set:default-statuses"), AccessHash: seedID("set-hash:default-statuses"), Name: "DefaultStatuses", Title: "Default Statuses",
		Kind: SetEmoji, Builtin: "inputStickerSetEmojiDefaultStatuses", Hash: int(int32(sum.Sum32())), Count: len(items)}, items, nil
}

// IsStatusEmoji says whether a document can be worn as a status: a custom
// emoji in a pack the instance still has.
func (s *Service) IsStatusEmoji(ctx context.Context, doc int64) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sticker_set_items i JOIN documents d ON d.id = i.document_id
		JOIN sticker_sets st ON st.id = i.set_id WHERE i.document_id = $1 AND d.kind = $2 AND st.enabled)`, doc, KindEmoji).Scan(&ok)
	return ok, err
}

func (s *Service) collectItems(ctx context.Context, rows pgx.Rows) ([]Item, error) {
	type row struct {
		id    int64
		emoji string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var v row
		err := r.Scan(&v.id, &v.emoji)
		return v, err
	})
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(list))
	for i, v := range list {
		ids[i] = v.id
	}
	docs, err := s.Documents(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]Document, len(docs))
	for _, d := range docs {
		byID[d.ID] = d
	}
	out := make([]Item, 0, len(list))
	for _, v := range list {
		if d, ok := byID[v.id]; ok {
			out = append(out, Item{Doc: d, Emoji: v.emoji})
		}
	}
	return out, nil
}

// premiumAccess: a Premium-only pack (admin panel) needs Premium, or being its maker. $1 is the user.
const premiumAccess = `NOT s.premium_only OR s.owner_id = $1 OR EXISTS(SELECT 1 FROM users pu WHERE pu.id = $1 AND pu.premium_until > extract(epoch FROM now()))`

// Installed are the user's sets of one kind, newest install first.
func (s *Service) Installed(ctx context.Context, user int64, kind string) ([]Set, error) {
	rows, err := s.db.Query(ctx, "SELECT * FROM (SELECT "+setCols+" FROM "+setFrom+" WHERE s.kind=$2 AND s.builtin IS NULL AND s.enabled AND ("+premiumAccess+")) x WHERE x.installed_date > 0 ORDER BY x.installed_date DESC, x.id", user, kind)
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

// Install adds the set to the user's panel or, with on=false, takes it out.
func (s *Service) Install(ctx context.Context, user, set int64, on bool) error {
	if on {
		var allowed bool
		err := s.db.QueryRow(ctx, "SELECT "+premiumAccess+" FROM sticker_sets s WHERE s.id=$2", user, set).Scan(&allowed)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil && !allowed {
			return tgerr.New(400, "PREMIUM_ACCOUNT_REQUIRED")
		}
	}
	_, err := s.db.Exec(ctx, `INSERT INTO user_sticker_sets(user_id, set_id, installed, date) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, set_id) DO UPDATE SET installed = excluded.installed, date = excluded.date`, user, set, on, s.unix())
	return err
}

// CustomEmoji are the emoji documents among ids; a custom emoji's id is
// its document id.
func (s *Service) CustomEmoji(ctx context.Context, ids []int64) ([]Document, error) {
	docs, err := s.Documents(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := docs[:0]
	for _, d := range docs {
		if d.Kind == KindEmoji {
			out = append(out, d)
		}
	}
	return out, nil
}

func (s *Service) SetTL(v Set) tg.StickerSet {
	out := tg.StickerSet{ID: v.ID, AccessHash: v.AccessHash, Title: v.Title, ShortName: v.Name, Count: v.Count, Hash: v.Hash,
		Official: v.Official, Emojis: v.Kind == SetEmoji, TextColor: v.TextColor}
	if v.Installed > 0 {
		out.SetInstalledDate(v.Installed)
	}
	if v.ThumbDoc != 0 {
		out.SetThumbDocumentID(v.ThumbDoc)
	}
	return out
}

// StickerSetTL is messages.stickerSet: the set, its stickers and their
// emoji packs in set order.
func (s *Service) StickerSetTL(v Set, items []Item) *tg.MessagesStickerSet {
	out := &tg.MessagesStickerSet{Set: s.SetTL(v), Packs: []tg.StickerPack{}, Keywords: []tg.StickerKeyword{}, Documents: []tg.DocumentClass{}}
	pack := map[string]int{}
	for _, it := range items {
		out.Documents = append(out.Documents, s.DocumentTL(it.Doc))
		if it.Emoji == "" {
			continue
		}
		i, ok := pack[it.Emoji]
		if !ok {
			i = len(out.Packs)
			pack[it.Emoji] = i
			out.Packs = append(out.Packs, tg.StickerPack{Emoticon: it.Emoji})
		}
		out.Packs[i].Documents = append(out.Packs[i].Documents, it.Doc.ID)
	}
	return out
}

// ListHash is Telegram's hash over a list (core.telegram.org/api/offsets),
// the same the apps compute over their cached sets.
func ListHash(vals []int64) int64 {
	var h uint64
	for _, v := range vals {
		h ^= h >> 21
		h ^= h << 35
		h ^= h >> 4
		h += uint64(v)
	}
	return int64(h)
}
