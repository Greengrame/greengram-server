package messages

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/ids"
)

// Telegram's premium limits: there's no premium here, so everyone gets them.
const (
	MaxFilters     = 20
	MaxFilterPeers = 200
	// folder ids are the client's to pick; the official apps use 2..255
	minFilterID = 2
	maxFilterID = 255
)

const (
	FolderMain    = 0
	FolderArchive = 1
)

func filterPeers(f tg.DialogFilterClass) (pinned, include, exclude []tg.InputPeerClass) {
	switch v := f.(type) {
	case *tg.DialogFilter:
		return v.PinnedPeers, v.IncludePeers, v.ExcludePeers
	case *tg.DialogFilterChatlist:
		return v.PinnedPeers, v.IncludePeers, nil
	}
	return nil, nil, nil
}

func checkFilter(f tg.DialogFilterClass) error {
	var title string
	switch v := f.(type) {
	case *tg.DialogFilter:
		title = v.Title.Text
		any := v.Contacts || v.NonContacts || v.Groups || v.Broadcasts || v.Bots
		if !any && len(v.IncludePeers) == 0 && len(v.PinnedPeers) == 0 {
			return tgerr.New(400, "FILTER_INCLUDE_EMPTY")
		}
	case *tg.DialogFilterChatlist:
		title = v.Title.Text
	default:
		return tgerr.New(400, "FILTER_ID_INVALID")
	}
	if strings.TrimSpace(title) == "" {
		return tgerr.New(400, "FILTER_TITLE_EMPTY")
	}
	if utf8.RuneCountInString(title) > 64 {
		return tgerr.New(400, "LIMIT_INVALID")
	}
	pinned, include, exclude := filterPeers(f)
	// pinned chats count against the include limit in the apps too
	if len(pinned)+len(include) > MaxFilterPeers || len(exclude) > MaxFilterPeers {
		return tgerr.New(400, "LIMIT_INVALID")
	}
	return nil
}

// Filters are the user's folders in their order, with DialogFilterDefault
// ("All chats") where the user put it, first if they never moved it.
func (s *Service) Filters(ctx context.Context, owner int64) ([]tg.DialogFilterClass, bool, error) {
	var tags bool
	if err := s.db.QueryRow(ctx, "SELECT coalesce((SELECT enabled FROM dialog_filter_tags WHERE owner_id=$1), false)", owner).Scan(&tags); err != nil {
		return nil, false, err
	}
	rows, err := s.db.Query(ctx, "SELECT id, body FROM dialog_filters WHERE owner_id=$1 ORDER BY pos, id", owner)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []tg.DialogFilterClass{}
	placed := false
	for rows.Next() {
		var id int
		var body []byte
		if err := rows.Scan(&id, &body); err != nil {
			return nil, false, err
		}
		if id == 0 {
			out = append(out, &tg.DialogFilterDefault{})
			placed = true
			continue
		}
		f, err := tg.DecodeDialogFilter(&bin.Buffer{Buf: body})
		if err != nil {
			return nil, false, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if !placed {
		out = append([]tg.DialogFilterClass{&tg.DialogFilterDefault{}}, out...)
	}
	return out, tags, nil
}

// SaveFilter creates or replaces folder id; f nil deletes it. The stored
// copy carries id, whatever the client put inside.
func (s *Service) SaveFilter(ctx context.Context, owner int64, id int, f tg.DialogFilterClass) error {
	if id < minFilterID || id > maxFilterID {
		return tgerr.New(400, "FILTER_ID_INVALID")
	}
	if f == nil {
		_, err := s.db.Exec(ctx, "DELETE FROM dialog_filters WHERE owner_id=$1 AND id=$2", owner, id)
		return err
	}
	if err := checkFilter(f); err != nil {
		return err
	}
	switch v := f.(type) {
	case *tg.DialogFilter:
		c := *v
		c.ID = id
		f = &c
	case *tg.DialogFilterChatlist:
		c := *v
		c.ID = id
		f = &c
	}
	var b bin.Buffer
	if err := f.Encode(&b); err != nil {
		return err
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		// serializes one user's folder writes, so the count holds
		if err := lockBoxes(ctx, tx, owner); err != nil {
			return err
		}
		var n int
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT count(*) FILTER (WHERE id <> 0), coalesce(bool_or(id = $2), false) FROM dialog_filters WHERE owner_id=$1", owner, id).Scan(&n, &exists); err != nil {
			return err
		}
		if !exists && n >= MaxFilters {
			return tgerr.New(400, "DIALOG_FILTERS_TOO_MUCH")
		}
		// a new folder goes last
		_, err := tx.Exec(ctx, `INSERT INTO dialog_filters(owner_id, id, pos, body)
			VALUES ($1, $2, (SELECT coalesce(max(pos), 0) + 1 FROM dialog_filters WHERE owner_id=$1), $3)
			ON CONFLICT (owner_id, id) DO UPDATE SET body = excluded.body`, owner, id, b.Buf)
		return err
	})
}

// OrderFilters puts the listed folders (0 = All chats) in that order.
// Unknown ids are skipped, folders left out keep their place after them.
func (s *Service) OrderFilters(ctx context.Context, owner int64, order []int) ([]int, error) {
	if len(order) > MaxFilters+1 {
		return nil, tgerr.New(400, "LIMIT_INVALID")
	}
	var clean []int
	seen := map[int]bool{}
	for _, id := range order {
		if (id == 0 || id >= minFilterID && id <= maxFilterID) && !seen[id] {
			seen[id] = true
			clean = append(clean, id)
		}
	}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := lockBoxes(ctx, tx, owner); err != nil {
			return err
		}
		// All chats gets a row of its own once it's ordered
		if seen[0] {
			if _, err := tx.Exec(ctx, "INSERT INTO dialog_filters(owner_id, id, pos) VALUES ($1, 0, 0) ON CONFLICT DO NOTHING", owner); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, "SELECT id FROM dialog_filters WHERE owner_id=$1 ORDER BY pos, id", owner)
		if err != nil {
			return err
		}
		have, err := pgx.CollectRows(rows, pgx.RowTo[int])
		if err != nil {
			return err
		}
		exists := map[int]bool{}
		for _, id := range have {
			exists[id] = true
		}
		var final []int
		for _, id := range clean {
			if exists[id] {
				final = append(final, id)
			}
		}
		for _, id := range have {
			if !seen[id] {
				final = append(final, id)
			}
		}
		_, err = tx.Exec(ctx, `UPDATE dialog_filters f SET pos = o.pos FROM unnest($2::int[]) WITH ORDINALITY AS o(id, pos)
			WHERE f.owner_id=$1 AND f.id = o.id`, owner, final)
		clean = final
		return err
	})
	return clean, err
}

func (s *Service) SetFilterTags(ctx context.Context, owner int64, on bool) error {
	_, err := s.db.Exec(ctx, `INSERT INTO dialog_filter_tags(owner_id, enabled) VALUES ($1, $2)
		ON CONFLICT (owner_id) DO UPDATE SET enabled = excluded.enabled`, owner, on)
	return err
}

// FolderMove is one chat going to the main list or the archive.
type FolderMove struct {
	User    int64 // a private chat, or
	Channel int64 // a group or channel
	Folder  int
}

// SetFolders moves chats between the main list and the archive and
// journals one updateFolderPeers for the moves that happened. Archiving
// unpins, like the apps do on their side. A chat the user doesn't have is
// skipped; nothing moved gives a nil update.
func (s *Service) SetFolders(ctx context.Context, owner int64, moves []FolderMove) (tg.UpdateClass, error) {
	var u tg.UpdateClass
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := lockBoxes(ctx, tx, owner); err != nil {
			return err
		}
		var done []tg.FolderPeer
		for _, mv := range moves {
			if mv.Folder != FolderMain && mv.Folder != FolderArchive {
				return tgerr.New(400, "FOLDER_ID_INVALID")
			}
			var (
				tag  interface{ RowsAffected() int64 }
				err  error
				peer tg.PeerClass
			)
			// channel_members belongs to internal/channels, but the move has
			// to share the box lock and the journal entry with the private
			// chats, so the one column is written from here
			if mv.Channel != 0 {
				tag, err = tx.Exec(ctx, `UPDATE channel_members SET folder_id=$3,
					pinned_at = CASE WHEN folder_id = $3 THEN pinned_at END
					WHERE channel_id=$2 AND user_id=$1 AND status IN ('creator', 'admin', 'member', 'restricted')`, owner, mv.Channel, mv.Folder)
				peer = &tg.PeerChannel{ChannelID: mv.Channel}
			} else {
				tag, err = tx.Exec(ctx, `UPDATE dialogs SET folder_id=$3,
					pinned_at = CASE WHEN folder_id = $3 THEN pinned_at END
					WHERE owner_id=$1 AND peer_id=$2`, owner, mv.User, mv.Folder)
				peer = &tg.PeerUser{UserID: ids.Client(mv.User)}
			}
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				done = append(done, tg.FolderPeer{Peer: peer, FolderID: mv.Folder})
			}
		}
		if len(done) == 0 {
			return nil
		}
		var err error
		u, err = s.journal(ctx, tx, owner, 1, func(pts int) tg.UpdateClass {
			return &tg.UpdateFolderPeers{FolderPeers: done, Pts: pts, PtsCount: 1}
		})
		return err
	})
	return u, err
}
