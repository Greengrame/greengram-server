package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
)

// DefaultReactions is Telegram's standard reaction list, in its order.
// The emoji-pack fallback uses it when no artwork was imported.
var DefaultReactions = []string{
	"👍", "👎", "❤", "🔥", "🥰", "👏", "😁", "🤔", "🤯", "😱", "🤬", "😢", "🎉", "🤩", "🤮", "💩", "🙏", "👌", "🕊", "🤡",
	"🥱", "🥴", "😍", "🐳", "❤‍🔥", "🌚", "🌭", "💯", "🤣", "⚡", "🍌", "🏆", "💔", "🤨", "😐", "🍓", "🍾", "💋", "🖕", "😈",
	"😴", "😭", "🤓", "👻", "👨‍💻", "👀", "🎃", "🙈", "😇", "😨", "🤝", "✍", "🤗", "🫡", "🎅", "🎄", "☃", "💅", "🤪", "🗿",
	"🆒", "💘", "🙉", "🦄", "😘", "💊", "🙊", "😎", "👾", "🤷‍♂", "🤷", "🤷‍♀", "😡",
}

// Reactions export, the layout gramsrv's stickerfetch writes from
// messages.getAvailableReactions: global_json/available_reactions_raw.json
// and reactions/<document id>.<ext>.
type exportReaction struct {
	Reaction          string     `json:"reaction"`
	Title             string     `json:"title"`
	Inactive          bool       `json:"inactive"`
	Premium           bool       `json:"premium"`
	StaticIcon        *exportDoc `json:"static_icon"`
	AppearAnimation   *exportDoc `json:"appear_animation"`
	SelectAnimation   *exportDoc `json:"select_animation"`
	ActivateAnimation *exportDoc `json:"activate_animation"`
	EffectAnimation   *exportDoc `json:"effect_animation"`
	AroundAnimation   *exportDoc `json:"around_animation"`
	CenterIcon        *exportDoc `json:"center_icon"`
}

type exportDoc struct {
	ID         int64        `json:"id"`
	MimeType   string       `json:"mime_type"`
	Size       int64        `json:"size"`
	Attributes []exportAttr `json:"attributes"`
}

type exportAttr struct {
	Type     string  `json:"_"`
	W        int     `json:"w"`
	H        int     `json:"h"`
	Alt      string  `json:"alt"`
	FileName string  `json:"file_name"`
	Duration float64 `json:"duration"`
}

func (d exportDoc) attrs() []tg.DocumentAttributeClass {
	var out []tg.DocumentAttributeClass
	for _, a := range d.Attributes {
		switch a.Type {
		case "DocumentAttributeImageSize":
			out = append(out, &tg.DocumentAttributeImageSize{W: a.W, H: a.H})
		case "DocumentAttributeAnimated":
			out = append(out, &tg.DocumentAttributeAnimated{})
		case "DocumentAttributeSticker":
			// the set it points to is Telegram's, not ours
			out = append(out, &tg.DocumentAttributeSticker{Alt: a.Alt, Stickerset: &tg.InputStickerSetEmpty{}})
		case "DocumentAttributeVideo":
			out = append(out, &tg.DocumentAttributeVideo{W: a.W, H: a.H, Duration: a.Duration})
		case "DocumentAttributeFilename":
			out = append(out, &tg.DocumentAttributeFilename{FileName: a.FileName})
		}
	}
	return out
}

// ImportReactions loads a reactions export, replacing the list. Like the
// packs it is files on disk, so sizes and types are checked again.
func (s *Service) ImportReactions(ctx context.Context, dir string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "global_json", "available_reactions_raw.json"))
	if err != nil {
		return 0, err
	}
	var ex struct {
		Result struct {
			Reactions []exportReaction `json:"reactions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &ex); err != nil {
		return 0, fmt.Errorf("media: reactions export: %w", err)
	}
	now := s.unix()
	docs := map[int64]Document{}
	load := func(d *exportDoc) (int64, error) {
		if d == nil || d.ID == 0 {
			return 0, nil
		}
		if _, ok := docs[d.ID]; ok {
			return d.ID, nil
		}
		ext, ok := stickerExt[d.MimeType]
		if !ok || d.Size < 1 || d.Size > maxStickerBytes {
			return 0, fmt.Errorf("media: reaction document %d: bad type or size", d.ID)
		}
		body, err := os.ReadFile(filepath.Join(dir, "reactions", strconv.FormatInt(d.ID, 10)+"."+ext))
		if err != nil {
			return 0, err
		}
		if int64(len(body)) != d.Size {
			return 0, fmt.Errorf("media: reaction document %d: size changed", d.ID)
		}
		key, size, err := s.blobs.Put(ctx, bytes.NewReader(body))
		if err != nil {
			return 0, err
		}
		docs[d.ID] = Document{ID: d.ID, AccessHash: seedID(fmt.Sprintf("doc-hash:%d", d.ID)), Date: now, Mime: d.MimeType, Kind: KindSticker,
			Key: key, Size: size, Attrs: d.attrs()}
		return d.ID, nil
	}
	type row struct {
		r                                          exportReaction
		static, appear, sel, activate, eff, around int64
		center                                     int64
	}
	var rows []row
	for _, r := range ex.Result.Reactions {
		if r.Reaction == "" || len(r.Reaction) > 32 {
			return 0, fmt.Errorf("media: bad reaction %q", r.Reaction)
		}
		var v row
		v.r = r
		for _, f := range []struct {
			dst *int64
			src *exportDoc
		}{{&v.static, r.StaticIcon}, {&v.appear, r.AppearAnimation}, {&v.sel, r.SelectAnimation}, {&v.activate, r.ActivateAnimation},
			{&v.eff, r.EffectAnimation}, {&v.around, r.AroundAnimation}, {&v.center, r.CenterIcon}} {
			if *f.dst, err = load(f.src); err != nil {
				return 0, err
			}
		}
		if v.static == 0 || v.appear == 0 || v.sel == 0 || v.activate == 0 || v.eff == 0 {
			return 0, fmt.Errorf("media: reaction %s: missing artwork", r.Reaction)
		}
		rows = append(rows, v)
	}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		for _, d := range docs {
			if err := s.insertDocument(ctx, tx, d); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, "DELETE FROM available_reactions"); err != nil {
			return err
		}
		for i, v := range rows {
			_, err := tx.Exec(ctx, `INSERT INTO available_reactions(reaction, position, title, inactive, premium, static_icon, appear_animation,
					select_animation, activate_animation, effect_animation, around_animation, center_icon)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, nullif($11::bigint, 0), nullif($12::bigint, 0))`,
				v.r.Reaction, i, v.r.Title, v.r.Inactive, v.r.Premium, v.static, v.appear, v.sel, v.activate, v.eff, v.around, v.center)
			if err != nil {
				return err
			}
		}
		return nil
	})
	return len(rows), err
}

// AvailableReactions is the reaction list and its hash.
func (s *Service) AvailableReactions(ctx context.Context) ([]tg.AvailableReaction, int, error) {
	type row struct {
		r              tg.AvailableReaction
		docs           [5]int64
		around, center int64
	}
	q, err := s.db.Query(ctx, `SELECT reaction, title, inactive, premium, static_icon, appear_animation, select_animation, activate_animation,
		effect_animation, coalesce(around_animation, 0), coalesce(center_icon, 0) FROM available_reactions ORDER BY position`)
	if err != nil {
		return nil, 0, err
	}
	rows, err := pgx.CollectRows(q, func(r pgx.CollectableRow) (row, error) {
		var v row
		err := r.Scan(&v.r.Reaction, &v.r.Title, &v.r.Inactive, &v.r.Premium, &v.docs[0], &v.docs[1], &v.docs[2], &v.docs[3], &v.docs[4], &v.around, &v.center)
		return v, err
	})
	if err != nil {
		return nil, 0, err
	}
	if len(rows) == 0 {
		return s.packReactions(ctx)
	}
	var want []int64
	for _, v := range rows {
		want = append(want, v.docs[:]...)
		want = append(want, v.around, v.center)
	}
	list, err := s.Documents(ctx, want)
	if err != nil {
		return nil, 0, err
	}
	byID := map[int64]*tg.Document{}
	for _, d := range list {
		byID[d.ID] = s.DocumentTL(d)
	}
	doc := func(id int64) tg.DocumentClass {
		if d, ok := byID[id]; ok {
			return d
		}
		return &tg.DocumentEmpty{ID: id}
	}
	out := make([]tg.AvailableReaction, 0, len(rows))
	sum := crc32.NewIEEE()
	for _, v := range rows {
		r := v.r
		r.StaticIcon, r.AppearAnimation, r.SelectAnimation, r.ActivateAnimation, r.EffectAnimation =
			doc(v.docs[0]), doc(v.docs[1]), doc(v.docs[2]), doc(v.docs[3]), doc(v.docs[4])
		// one flag for the pair: encoding fails with only one of them set
		if v.around != 0 && v.center != 0 {
			r.SetAroundAnimation(doc(v.around))
			r.SetCenterIcon(doc(v.center))
		}
		out = append(out, r)
		sum.Write([]byte(r.Reaction))
		binary.Write(sum, binary.LittleEndian, v.docs)
	}
	return out, int(int32(sum.Sum32())), nil
}

// packReactions makes the list out of the seeded emoji packs: one
// animated emoji does for every animation. Apps show the static icon as
// an image, which a tgs isn't, so this is a stand-in for the real export.
func (s *Service) packReactions(ctx context.Context) ([]tg.AvailableReaction, int, error) {
	strip := func(e string) string { return strings.ReplaceAll(e, "️", "") }
	want := make([]string, len(DefaultReactions))
	for i, e := range DefaultReactions {
		want[i] = strip(e)
	}
	// restrictedemoji is Telegram's animated set of the standard emoji
	q, err := s.db.Query(ctx, `SELECT DISTINCT ON (e) replace(i.emoji, U&'\FE0F', '') AS e, i.document_id
		FROM sticker_set_items i JOIN sticker_sets s ON s.id = i.set_id
		WHERE s.kind = 'emoji' AND replace(i.emoji, U&'\FE0F', '') = ANY($1)
		ORDER BY e, s.short_name <> 'restrictedemoji', s.id, i.position`, want)
	if err != nil {
		return nil, 0, err
	}
	type pick struct {
		emoji string
		doc   int64
	}
	picks, err := pgx.CollectRows(q, func(r pgx.CollectableRow) (pick, error) {
		var p pick
		err := r.Scan(&p.emoji, &p.doc)
		return p, err
	})
	if err != nil {
		return nil, 0, err
	}
	byEmoji := map[string]int64{}
	var ids []int64
	for _, p := range picks {
		byEmoji[p.emoji] = p.doc
		ids = append(ids, p.doc)
	}
	list, err := s.Documents(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	docs := map[int64]*tg.Document{}
	for _, d := range list {
		docs[d.ID] = s.DocumentTL(d)
	}
	out := []tg.AvailableReaction{}
	sum := crc32.NewIEEE()
	for i, e := range DefaultReactions {
		d, ok := docs[byEmoji[want[i]]]
		if !ok {
			continue
		}
		r := tg.AvailableReaction{Reaction: e, Title: e, StaticIcon: d, AppearAnimation: d, SelectAnimation: d, ActivateAnimation: d, EffectAnimation: d}
		// around_animation and center_icon are one flag: both or neither
		r.SetAroundAnimation(d)
		r.SetCenterIcon(d)
		out = append(out, r)
		sum.Write([]byte(e))
		binary.Write(sum, binary.LittleEndian, d.ID)
	}
	return out, int(int32(sum.Sum32())), nil
}
