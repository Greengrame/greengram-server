package channels

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Handover is what account deletion did to one chat the account was in.
// Successor is set when the account owned the chat and an admin took it
// over; Ownerless when it owned the chat and nobody could.
type Handover struct {
	Channel   int64  `json:"channel"`
	Title     string `json:"title"`
	Role      string `json:"role"`
	Successor int64  `json:"successor,omitempty"`
	Ownerless bool   `json:"ownerless,omitempty"`
}

// EraseMember takes a deleted account out of every chat it is in, inside
// the caller's transaction. A chat it owned passes to its longest-serving
// admin who is a live person; with none, the chat stays ownerless, like a
// Telegram group whose owner deleted their account: members keep talking,
// nobody can change its settings, and the admin panel can still delete it.
// Messages stay. The leave is silent: no service message from a deleted
// account.
func EraseMember(ctx context.Context, tx pgx.Tx, user int64) ([]Handover, []Event, error) {
	rows, err := tx.Query(ctx, `SELECT c.id, c.title, m.status, c.creator_id = $1 FROM channel_members m JOIN channels c ON c.id = m.channel_id
		WHERE m.user_id=$1 AND m.status IN ('creator', 'admin', 'member', 'restricted') AND NOT c.deleted ORDER BY c.id FOR UPDATE OF c`, user)
	if err != nil {
		return nil, nil, err
	}
	type held struct {
		id     int64
		title  string
		status string
		owner  bool
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (held, error) {
		var h held
		return h, r.Scan(&h.id, &h.title, &h.status, &h.owner)
	})
	if err != nil {
		return nil, nil, err
	}
	var out []Handover
	var events []Event
	for _, h := range list {
		ho := Handover{Channel: h.id, Title: h.title, Role: h.status}
		if h.owner {
			var next int64
			err := tx.QueryRow(ctx, `SELECT m.user_id FROM channel_members m JOIN users u ON u.id = m.user_id
				WHERE m.channel_id=$1 AND m.status='admin' AND m.user_id<>$2 AND NOT u.disabled AND NOT u.is_bot AND NOT u.is_system
				ORDER BY m.date, m.user_id LIMIT 1`, h.id, user).Scan(&next)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, nil, err
			}
			if next != 0 {
				if _, err := tx.Exec(ctx, "UPDATE channels SET creator_id=$2 WHERE id=$1", h.id, next); err != nil {
					return nil, nil, err
				}
				if _, err := tx.Exec(ctx, "UPDATE channel_members SET status='creator', admin_rights=$3, promoted_by=NULL, banned_rights=NULL WHERE channel_id=$1 AND user_id=$2",
					h.id, next, encodeTL(ptr(fullAdmin()))); err != nil {
					return nil, nil, err
				}
				ho.Successor = next
			} else {
				ho.Ownerless = true
			}
		}
		if _, err := tx.Exec(ctx, "UPDATE channel_members SET status='left', admin_rights=NULL, rank='' WHERE channel_id=$1 AND user_id=$2", h.id, user); err != nil {
			return nil, nil, err
		}
		ev, err := toMembers(ctx, tx, h.id, updChannel(h.id))
		if err != nil {
			return nil, nil, err
		}
		events = append(events, ev)
		out = append(out, ho)
	}
	if _, err := tx.Exec(ctx, "UPDATE channel_invites SET revoked=true WHERE creator_id=$1 AND NOT revoked", user); err != nil {
		return nil, nil, err
	}
	return out, events, nil
}
