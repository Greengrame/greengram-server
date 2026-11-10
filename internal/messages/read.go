package messages

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// HistoryQuery is messages.getHistory's paging, same meaning as Telegram's:
// start at OffsetID (or before OffsetDate), skip AddOffset (negative reaches
// newer messages), keep MinID < id < MaxID.
type HistoryQuery struct {
	Topic      int
	OffsetID   int
	OffsetDate int
	AddOffset  int
	Limit      int
	MaxID      int
	MinID      int
	Filter     Filter
}

// Filter keeps only some messages of a history page; channels use it too.
type Filter int

const (
	Everything      Filter = iota
	UnreadReactions        // the viewer's own, with reactions they haven't seen
	Pinned
	UnreadMentions // groups only
)

func (f Filter) sql() string {
	switch f {
	case Pinned:
		return " AND pinned"
	case UnreadMentions:
		return " AND false"
	case UnreadReactions:
		return " AND out AND EXISTS (SELECT 1 FROM message_reactions r WHERE r.shared_id = messages.shared_id AND r.author_id = messages.owner_id AND r.unread)"
	}
	return ""
}

func clampLimit(n, max int) int {
	if n <= 0 || n > max {
		return max
	}
	return n
}

// History is one page of a dialog, newest first, and how many messages the
// dialog has in total.
func (s *Service) History(ctx context.Context, owner, peer int64, q HistoryQuery) ([]Msg, int, error) {
	return s.history(ctx, "peer_id", owner, peer, q)
}

// SavedHistory is History for one saved dialog of Saved Messages.
func (s *Service) SavedHistory(ctx context.Context, owner, savedPeer int64, q HistoryQuery) ([]Msg, int, error) {
	return s.history(ctx, "saved_peer", owner, savedPeer, q)
}

// history pages the messages of owner's box where col = peer.
func (s *Service) history(ctx context.Context, col string, owner, peer int64, q HistoryQuery) ([]Msg, int, error) {
	limit := clampLimit(q.Limit, 100)
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM messages WHERE owner_id=$1 AND "+col+"=$2"+q.sql(), owner, peer).Scan(&total); err != nil {
		return nil, 0, err
	}
	bound := "TRUE" + q.sql()
	args := []any{owner, peer}
	if q.MaxID > 0 {
		args = append(args, q.MaxID)
		bound += " AND id < $3"
	}
	if q.MinID > 0 {
		args = append(args, q.MinID)
		bound += " AND id > $" + itoa(len(args))
	}
	base := "SELECT " + msgCols + " FROM messages WHERE owner_id=$1 AND " + col + "=$2 AND " + bound

	// the anchor: messages older than this id are "before" the offset
	anchor := q.OffsetID
	if anchor == 0 && q.OffsetDate > 0 {
		err := s.db.QueryRow(ctx, "SELECT coalesce(max(id), 0) + 1 FROM messages WHERE owner_id=$1 AND "+col+"=$2 AND date < $3", owner, peer, q.OffsetDate).Scan(&anchor)
		if err != nil {
			return nil, 0, err
		}
	}
	if anchor == 0 {
		anchor = 1 << 30
	}
	var out []Msg
	if q.AddOffset < 0 {
		newer := min(-q.AddOffset, limit)
		rows, err := s.db.Query(ctx, base+" AND id >= $"+itoa(len(args)+1)+" ORDER BY id ASC LIMIT $"+itoa(len(args)+2), append(args, anchor, newer)...)
		if err != nil {
			return nil, 0, err
		}
		got, err := collect(rows)
		if err != nil {
			return nil, 0, err
		}
		slices.Reverse(got)
		out = got
		limit -= len(got)
		q.AddOffset = 0
	}
	if limit > 0 {
		rows, err := s.db.Query(ctx, base+" AND id < $"+itoa(len(args)+1)+" ORDER BY id DESC OFFSET $"+itoa(len(args)+2)+" LIMIT $"+itoa(len(args)+3),
			append(args, anchor, q.AddOffset, limit)...)
		if err != nil {
			return nil, 0, err
		}
		got, err := collect(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, got...)
	}
	return out, total, nil
}

func itoa(n int) string { return strconv.Itoa(n) }

type Dialog struct {
	Peer          int64
	TopID         int
	TopDate       int
	ReadInboxMax  int
	ReadOutboxMax int
	Unread        int
	Pinned        bool
	Folder        int // FolderMain or FolderArchive
	// own messages with reactions the owner hasn't seen
	UnreadReactions int
}

const dialogCols = `d.peer_id, d.top_id, d.top_date, d.read_inbox_max, d.read_outbox_max, d.pinned_at IS NOT NULL, d.folder_id,
	(SELECT count(*) FROM messages m WHERE m.owner_id = d.owner_id AND m.peer_id = d.peer_id AND NOT m.out AND m.id > d.read_inbox_max),
	(SELECT count(DISTINCT r.shared_id) FROM message_reactions r JOIN messages m ON m.owner_id = r.author_id AND m.shared_id = r.shared_id
		WHERE r.author_id = d.owner_id AND r.unread AND m.peer_id = d.peer_id)`

func scanDialogs(rows pgx.Rows) ([]Dialog, error) {
	defer rows.Close()
	var out []Dialog
	for rows.Next() {
		var d Dialog
		if err := rows.Scan(&d.Peer, &d.TopID, &d.TopDate, &d.ReadInboxMax, &d.ReadOutboxMax, &d.Pinned, &d.Folder, &d.Unread, &d.UnreadReactions); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Dialogs is one page of the chat list, newest first, after the given
// position (offsetDate 0 = from the top). Pinned dialogs are left out when
// excludePinned is set.
func (s *Service) Dialogs(ctx context.Context, owner int64, offsetDate, offsetID int, limit int, excludePinned bool) ([]Dialog, []Msg, int, error) {
	limit = clampLimit(limit, 100)
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM dialogs WHERE owner_id=$1", owner).Scan(&total); err != nil {
		return nil, nil, 0, err
	}
	where := "d.owner_id=$1"
	args := []any{owner}
	if offsetDate > 0 {
		args = append(args, offsetDate, offsetID)
		where += " AND (d.top_date, d.top_id) < ($2, $3)"
	}
	if excludePinned {
		where += " AND d.pinned_at IS NULL"
	}
	args = append(args, limit)
	order := " ORDER BY d.top_date DESC, d.top_id DESC"
	if !excludePinned && offsetDate == 0 {
		order = " ORDER BY d.pinned_at DESC NULLS LAST, d.top_date DESC, d.top_id DESC"
	}
	rows, err := s.db.Query(ctx, "SELECT "+dialogCols+" FROM dialogs d WHERE "+where+order+" LIMIT $"+itoa(len(args)), args...)
	if err != nil {
		return nil, nil, 0, err
	}
	list, err := scanDialogs(rows)
	if err != nil {
		return nil, nil, 0, err
	}
	tops, err := s.tops(ctx, owner, list)
	return list, tops, total, err
}

// PeerDialogs are the dialogs with the given peers that exist.
func (s *Service) PeerDialogs(ctx context.Context, owner int64, peers []int64) ([]Dialog, []Msg, error) {
	rows, err := s.db.Query(ctx, "SELECT "+dialogCols+" FROM dialogs d WHERE d.owner_id=$1 AND d.peer_id = ANY($2)", owner, peers)
	if err != nil {
		return nil, nil, err
	}
	list, err := scanDialogs(rows)
	if err != nil {
		return nil, nil, err
	}
	tops, err := s.tops(ctx, owner, list)
	return list, tops, err
}

// PinnedDialogs in pin order.
func (s *Service) PinnedDialogs(ctx context.Context, owner int64) ([]Dialog, []Msg, error) {
	rows, err := s.db.Query(ctx, "SELECT "+dialogCols+" FROM dialogs d WHERE d.owner_id=$1 AND d.pinned_at IS NOT NULL ORDER BY d.pinned_at DESC", owner)
	if err != nil {
		return nil, nil, err
	}
	list, err := scanDialogs(rows)
	if err != nil {
		return nil, nil, err
	}
	tops, err := s.tops(ctx, owner, list)
	return list, tops, err
}

// Pin pins or unpins a dialog; false if there is no such dialog.
func (s *Service) Pin(ctx context.Context, owner, peer int64, pinned bool) (bool, error) {
	q := "UPDATE dialogs SET pinned_at = NULL WHERE owner_id=$1 AND peer_id=$2"
	if pinned {
		q = "UPDATE dialogs SET pinned_at = extract(epoch FROM clock_timestamp())::int WHERE owner_id=$1 AND peer_id=$2"
	}
	tag, err := s.db.Exec(ctx, q, owner, peer)
	return tag.RowsAffected() == 1, err
}

func (s *Service) tops(ctx context.Context, owner int64, list []Dialog) ([]Msg, error) {
	if len(list) == 0 {
		return nil, nil
	}
	want := make([]int, len(list))
	for i, d := range list {
		want[i] = d.TopID
	}
	return s.Get(ctx, owner, want)
}

// Search finds messages in one dialog (peer != 0) or the whole box by text
// and/or media kind. Newest first, below offsetID when given.
func (s *Service) Search(ctx context.Context, owner, peer int64, text string, offsetID, limit int, kinds ...string) ([]Msg, int, error) {
	limit = clampLimit(limit, 100)
	pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(text) + "%"
	where := "owner_id=$1 AND action IS NULL AND text ILIKE $2"
	args := []any{owner, pattern}
	if cond, arg := KindsSQL(kinds, ""); cond != "" {
		if arg != nil {
			args = append(args, arg)
			cond = strings.ReplaceAll(cond, "$K", "$"+itoa(len(args)))
		}
		where += " AND " + cond
	}
	if peer != 0 {
		args = append(args, peer)
		where += " AND peer_id=$" + itoa(len(args))
	}
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM messages WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if offsetID > 0 {
		args = append(args, offsetID)
		where += " AND id < $" + itoa(len(args))
	}
	args = append(args, limit)
	rows, err := s.db.Query(ctx, "SELECT "+msgCols+" FROM messages WHERE "+where+" ORDER BY id DESC LIMIT $"+itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	list, err := collect(rows)
	return list, total, err
}

// Contact is one entry of a user's address book.
type Contact struct {
	UserID    int64
	FirstName string
	LastName  string
}

func (s *Service) Contacts(ctx context.Context, owner int64) ([]Contact, error) {
	rows, err := s.db.Query(ctx, "SELECT user_id, first_name, last_name FROM contacts WHERE owner_id=$1 ORDER BY created_at", owner)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Contact, error) {
		var c Contact
		err := r.Scan(&c.UserID, &c.FirstName, &c.LastName)
		return c, err
	})
}

func (s *Service) IsContact(ctx context.Context, owner, user int64) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM contacts WHERE owner_id=$1 AND user_id=$2)", owner, user).Scan(&ok)
	return ok, err
}

func (s *Service) AddContact(ctx context.Context, owner int64, c Contact) error {
	if c.UserID == owner {
		return errPeerInvalid
	}
	_, err := s.db.Exec(ctx, `INSERT INTO contacts(owner_id, user_id, first_name, last_name) VALUES ($1, $2, $3, $4)
		ON CONFLICT (owner_id, user_id) DO UPDATE SET first_name = excluded.first_name, last_name = excluded.last_name`,
		owner, c.UserID, c.FirstName, c.LastName)
	return err
}

func (s *Service) DeleteContacts(ctx context.Context, owner int64, users []int64) error {
	_, err := s.db.Exec(ctx, "DELETE FROM contacts WHERE owner_id=$1 AND user_id = ANY($2)", owner, users)
	return err
}

// HasDialog tells whether owner already talks with peer.
func (s *Service) HasDialog(ctx context.Context, owner, peer int64) (bool, error) {
	var x int
	err := s.db.QueryRow(ctx, "SELECT 1 FROM dialogs WHERE owner_id=$1 AND peer_id=$2", owner, peer).Scan(&x)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// AllDialogs is the whole private chat list, for merging with channels.
func (s *Service) AllDialogs(ctx context.Context, owner int64) ([]Dialog, []Msg, error) {
	rows, err := s.db.Query(ctx, "SELECT "+dialogCols+" FROM dialogs d WHERE d.owner_id=$1 ORDER BY d.top_date DESC, d.top_id DESC", owner)
	if err != nil {
		return nil, nil, err
	}
	list, err := scanDialogs(rows)
	if err != nil {
		return nil, nil, err
	}
	tops, err := s.tops(ctx, owner, list)
	return list, tops, err
}

func (q HistoryQuery) sql() string {
	if q.Topic > 0 {
		return q.Filter.sql() + " AND bot_topic_id=" + strconv.Itoa(q.Topic)
	}
	return q.Filter.sql()
}
