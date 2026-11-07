package messages

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/ids"
)

// Reaction is one person's reaction to a message. Key is the emoticon, or
// '#' and the document id for a custom emoji.
type Reaction struct {
	User   int64
	Key    string
	Pos    int // order among the user's own, from 1
	Big    bool
	Unread bool // the author hasn't seen it
	Date   int
}

// MsgReactions is the reaction set of one message and who wrote it (0 for
// a channel post, which has nobody to tell).
type MsgReactions struct {
	Author int64
	List   []Reaction
	Paid   []PaidReaction
}

// PaidReaction is what one person has given a message in star reactions.
type PaidReaction struct {
	User  int64
	Count int
	Anon  bool
	Date  int
}

// topReactors is how many givers a message shows, as Telegram does.
const topReactors = 3

// TL is the reactions block as viewer sees it, star reactions included.
func (m MsgReactions) TL(viewer int64, canSeeList bool) tg.MessageReactions {
	out := ReactionsTL(m.List, viewer, m.Author, canSeeList)
	if len(m.Paid) == 0 {
		return out
	}
	total := 0
	mine := false
	for _, p := range m.Paid {
		total += p.Count
		mine = mine || viewer != 0 && p.User == viewer
	}
	rc := tg.ReactionCount{Reaction: &tg.ReactionPaid{}, Count: total}
	if mine {
		rc.SetChosenOrder(1)
	}
	out.Results = append([]tg.ReactionCount{rc}, out.Results...)
	top := slices.Clone(m.Paid)
	sort.SliceStable(top, func(i, j int) bool { return top[i].Count > top[j].Count })
	reactors := make([]tg.MessageReactor, 0, topReactors+1)
	for i, p := range top {
		my := viewer != 0 && p.User == viewer
		if i >= topReactors && !my {
			continue
		}
		r := tg.MessageReactor{Top: i < topReactors, My: my, Anonymous: p.Anon, Count: p.Count}
		if !p.Anon {
			r.SetPeerID(&tg.PeerUser{UserID: ids.Client(p.User)})
		}
		reactors = append(reactors, r)
	}
	out.SetTopReactors(reactors)
	return out
}

// Users are the people the block names.
func (m MsgReactions) Users() []int64 {
	out := Reactors(m.List)
	for _, p := range m.Paid {
		if !p.Anon && !slices.Contains(out, p.User) {
			out = append(out, p.User)
		}
	}
	return out
}

// ScanPaid adds star reaction rows (id, user, count, anonymous, date) to sets.
func ScanPaid(rows pgx.Rows, sets map[int]MsgReactions) error {
	defer rows.Close()
	for rows.Next() {
		var id int
		var p PaidReaction
		if err := rows.Scan(&id, &p.User, &p.Count, &p.Anon, &p.Date); err != nil {
			return err
		}
		m := sets[id]
		m.Paid = append(m.Paid, p)
		sets[id] = m
	}
	return rows.Err()
}

// PaidTarget finds the message user wants to give Stars for in their dialog
// with peer, and who wrote it.
func (s *Service) PaidTarget(ctx context.Context, user, peer int64, id int) (shared, author int64, err error) {
	err = s.db.QueryRow(ctx, "SELECT shared_id, from_id FROM messages WHERE owner_id=$1 AND peer_id=$2 AND id=$3 AND action IS NULL",
		user, peer, id).Scan(&shared, &author)
	if errors.Is(err, pgx.ErrNoRows) {
		err = errMessageInvalid
	}
	return shared, author, err
}

// AddPaid adds to user's star reaction total, in the transaction moving the
// Stars.
func (s *Service) AddPaid(ctx context.Context, tx pgx.Tx, shared, user int64, count int, anon bool) error {
	_, err := tx.Exec(ctx, `INSERT INTO message_paid_reactions(shared_id, user_id, count, anonymous, date) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (shared_id, user_id) DO UPDATE SET count = message_paid_reactions.count + excluded.count, anonymous = excluded.anonymous, date = excluded.date`,
		shared, user, count, anon, s.unix())
	return err
}

// SetPaidPrivacy shows or hides who gave the Stars.
func (s *Service) SetPaidPrivacy(ctx context.Context, shared, user int64, anon bool) error {
	_, err := s.db.Exec(ctx, "UPDATE message_paid_reactions SET anonymous=$3 WHERE shared_id=$1 AND user_id=$2", shared, user, anon)
	return err
}

// BoxIDs is the id of a shared message in each box that still has it.
func (s *Service) BoxIDs(ctx context.Context, shared int64) (map[int64]int, error) {
	rows, err := s.db.Query(ctx, "SELECT owner_id, id FROM messages WHERE shared_id=$1", shared)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var owner int64
		var id int
		if err := rows.Scan(&owner, &id); err != nil {
			return nil, err
		}
		out[owner] = id
	}
	return out, rows.Err()
}

var ErrReactionInvalid = tgerr.New(400, "REACTION_INVALID")

// ReactionKeys turns sendReaction's list into stored keys, without
// repeats. The newest choice is last; past max the oldest go, like
// Telegram does instead of refusing.
func ReactionKeys(list []tg.ReactionClass, max int) ([]string, error) {
	var out []string
	for _, r := range list {
		var k string
		switch v := r.(type) {
		case *tg.ReactionEmpty:
			continue
		case *tg.ReactionEmoji:
			k = strings.TrimSpace(v.Emoticon)
			if k == "" || len(k) > 32 || strings.HasPrefix(k, "#") {
				return nil, ErrReactionInvalid
			}
		case *tg.ReactionCustomEmoji:
			if v.DocumentID == 0 {
				return nil, ErrReactionInvalid
			}
			k = "#" + strconv.FormatInt(v.DocumentID, 10)
		default:
			return nil, ErrReactionInvalid
		}
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out, nil
}

// Custom is whether a key is a custom emoji.
func Custom(key string) bool { return strings.HasPrefix(key, "#") }

func ReactionTL(key string) tg.ReactionClass {
	if id, ok := strings.CutPrefix(key, "#"); ok {
		n, _ := strconv.ParseInt(id, 10, 64)
		return &tg.ReactionCustomEmoji{DocumentID: n}
	}
	return &tg.ReactionEmoji{Emoticon: key}
}

// recentLimit is how many of the newest reactors a message lists.
const recentLimit = 5

// ReactionsTL is the reactions block as viewer sees it: their own choice
// and, for the author, which reactions are new. Viewer 0 is the min
// version for everyone else; their apps keep their own choice from it.
func ReactionsTL(list []Reaction, viewer, author int64, canSeeList bool) tg.MessageReactions {
	out := tg.MessageReactions{Results: []tg.ReactionCount{}, Min: viewer == 0}
	out.CanSeeList = canSeeList
	type agg struct {
		key    string
		count  int
		first  int
		chosen int
	}
	byKey := map[string]*agg{}
	var order []*agg
	for _, r := range list {
		a := byKey[r.Key]
		if a == nil {
			a = &agg{key: r.Key, first: r.Date}
			byKey[r.Key] = a
			order = append(order, a)
		}
		a.count++
		a.first = min(a.first, r.Date)
		if viewer != 0 && r.User == viewer {
			a.chosen = r.Pos
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].count != order[j].count {
			return order[i].count > order[j].count
		}
		return order[i].first < order[j].first
	})
	for _, a := range order {
		rc := tg.ReactionCount{Reaction: ReactionTL(a.key), Count: a.count}
		if a.chosen != 0 {
			rc.SetChosenOrder(a.chosen)
		}
		out.Results = append(out.Results, rc)
	}
	if !canSeeList {
		return out
	}
	// newest first; the author also gets every unread one, their app
	// counts those for the badge
	recent := slices.Clone(list)
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].Date > recent[j].Date })
	var peers []tg.MessagePeerReaction
	for i, r := range recent {
		unread := r.Unread && viewer != 0 && viewer == author
		if i >= recentLimit && !unread {
			continue
		}
		pr := tg.MessagePeerReaction{PeerID: &tg.PeerUser{UserID: ids.Client(r.User)}, Date: r.Date, Reaction: ReactionTL(r.Key), Big: r.Big}
		pr.Unread = unread
		pr.My = viewer != 0 && r.User == viewer
		peers = append(peers, pr)
	}
	out.SetRecentReactions(peers)
	return out
}

// Reactors are the users a reaction set names.
func Reactors(list []Reaction) []int64 {
	var out []int64
	for _, r := range list {
		if !slices.Contains(out, r.User) {
			out = append(out, r.User)
		}
	}
	return out
}

func scanReactions(rows pgx.Rows) (map[int]MsgReactions, error) {
	defer rows.Close()
	out := map[int]MsgReactions{}
	for rows.Next() {
		var id int
		var author int64
		var r Reaction
		if err := rows.Scan(&id, &author, &r.User, &r.Key, &r.Pos, &r.Big, &r.Unread, &r.Date); err != nil {
			return nil, err
		}
		m := out[id]
		m.Author = author
		m.List = append(m.List, r)
		out[id] = m
	}
	return out, rows.Err()
}

// Reactions are the reaction sets of messages in owner's box, by id;
// messages nobody reacted to are left out.
func (s *Service) Reactions(ctx context.Context, owner int64, list []int) (map[int]MsgReactions, error) {
	if len(list) == 0 {
		return map[int]MsgReactions{}, nil
	}
	rows, err := s.db.Query(ctx, `SELECT m.id, m.from_id, r.user_id, r.reaction, r.position, r.big, r.unread, r.date
		FROM messages m JOIN message_reactions r ON r.shared_id = m.shared_id
		WHERE m.owner_id=$1 AND m.id = ANY($2) ORDER BY r.date, r.user_id, r.position`, owner, list)
	if err != nil {
		return nil, err
	}
	sets, err := scanReactions(rows)
	if err != nil {
		return nil, err
	}
	rows, err = s.db.Query(ctx, `SELECT m.id, p.user_id, p.count, p.anonymous, p.date
		FROM messages m JOIN message_paid_reactions p ON p.shared_id = m.shared_id
		WHERE m.owner_id=$1 AND m.id = ANY($2) ORDER BY p.date, p.user_id`, owner, list)
	if err != nil {
		return nil, err
	}
	if err := ScanPaid(rows, sets); err != nil {
		return nil, err
	}
	return sets, nil
}

// Reacted is a message's reaction set after a change and the message's
// id in each box that still has it.
type Reacted struct {
	MsgReactions
	IDs map[int64]int
}

// React replaces user's reactions on a message of their dialog with peer;
// no keys takes them back.
func (s *Service) React(ctx context.Context, user, peer int64, id int, keys []string, big bool) (Reacted, error) {
	out := Reacted{IDs: map[int64]int{}}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// the row lock keeps two changes by the same user in order
		var shared int64
		err := tx.QueryRow(ctx, "SELECT shared_id, from_id FROM messages WHERE owner_id=$1 AND peer_id=$2 AND id=$3 AND action IS NULL FOR UPDATE",
			user, peer, id).Scan(&shared, &out.Author)
		if errors.Is(err, pgx.ErrNoRows) {
			return errMessageInvalid
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM message_reactions WHERE shared_id=$1 AND user_id=$2", shared, user); err != nil {
			return err
		}
		now := s.unix()
		for i, k := range keys {
			_, err := tx.Exec(ctx, `INSERT INTO message_reactions(shared_id, user_id, reaction, position, big, author_id, unread, date)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, shared, user, k, i+1, big, out.Author, out.Author != user, now)
			if err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, "SELECT owner_id, id FROM messages WHERE shared_id=$1", shared)
		if err != nil {
			return err
		}
		for rows.Next() {
			var owner int64
			var mid int
			if err := rows.Scan(&owner, &mid); err != nil {
				rows.Close()
				return err
			}
			out.IDs[owner] = mid
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT 0, author_id, user_id, reaction, position, big, unread, date FROM message_reactions
			WHERE shared_id=$1 ORDER BY date, user_id, position`, shared)
		if err != nil {
			return err
		}
		all, err := scanReactions(rows)
		if err != nil {
			return err
		}
		out.List = all[0].List
		rows, err = tx.Query(ctx, "SELECT 0, user_id, count, anonymous, date FROM message_paid_reactions WHERE shared_id=$1 ORDER BY date, user_id", shared)
		if err != nil {
			return err
		}
		if err := ScanPaid(rows, all); err != nil {
			return err
		}
		out.Paid = all[0].Paid
		return nil
	})
	return out, err
}

// ReadReactions marks the reactions to owner's messages with peer seen.
func (s *Service) ReadReactions(ctx context.Context, owner, peer int64) error {
	_, err := s.db.Exec(ctx, `UPDATE message_reactions r SET unread = false FROM messages m
		WHERE r.author_id=$1 AND r.unread AND m.owner_id=$1 AND m.shared_id = r.shared_id AND m.peer_id=$2`, owner, peer)
	return err
}

// ReactionList is who reacted to a message in owner's box, newest first,
// optionally with one reaction only; offset counts rows already shown.
func (s *Service) ReactionList(ctx context.Context, owner int64, id int, key string, offset, limit int) ([]Reaction, int, error) {
	limit = clampLimit(limit, 100)
	where := "m.owner_id=$1 AND m.id=$2"
	args := []any{owner, id}
	if key != "" {
		args = append(args, key)
		where += " AND r.reaction=$3"
	}
	from := " FROM messages m JOIN message_reactions r ON r.shared_id = m.shared_id WHERE " + where
	var total int
	if err := s.db.QueryRow(ctx, "SELECT count(*)"+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(ctx, "SELECT m.id, m.from_id, r.user_id, r.reaction, r.position, r.big, r.unread, r.date"+from+
		" ORDER BY r.date DESC, r.user_id, r.position OFFSET $"+itoa(len(args)+1)+" LIMIT $"+itoa(len(args)+2), append(args, offset, limit)...)
	if err != nil {
		return nil, 0, err
	}
	got, err := scanReactions(rows)
	return got[id].List, total, err
}
