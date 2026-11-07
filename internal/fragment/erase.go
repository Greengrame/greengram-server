package fragment

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Released is one collectible a deleted account gave up. To is who holds it
// now: 0 for the instance's own reserve, else the person who took over the
// channel that uses the name.
type Released struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Listed bool   `json:"listed"`
	To     int64  `json:"to,omitempty"`
}

// EraseOwner takes every username and number off a deleted account, inside
// the caller's transaction and after its chats were handed over (see
// channels.EraseMember). Listings are cancelled and open quotes expire. A
// name one of its channels still uses goes with the channel to its new
// owner; everything else goes back to the instance unlisted, where the
// Fragment page can put it up again. The numbers it signed in with stop
// working.
func EraseOwner(ctx context.Context, tx pgx.Tx, user int64) ([]Released, error) {
	rows, err := tx.Query(ctx, `SELECT f.id, f.title, f.listed, coalesce((SELECT c.creator_id FROM channels c
			WHERE f.kind='username' AND lower(c.username)=lower(substr(f.title, 2)) AND NOT c.deleted AND c.creator_id<>$1), 0)
		FROM fragment_items f WHERE f.owner_id=$1 ORDER BY f.id FOR UPDATE OF f`, user)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Released, error) {
		var it Released
		return it, r.Scan(&it.ID, &it.Title, &it.Listed, &it.To)
	})
	if err != nil {
		return nil, err
	}
	for _, it := range list {
		var to *int64
		if it.To != 0 {
			to = &it.To
		}
		if _, err := tx.Exec(ctx, "UPDATE fragment_items SET owner_id=$2, listed=false, active=false, position=0 WHERE id=$1", it.ID, to); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, "UPDATE fragment_quotes SET expires_at=0 WHERE item_id=$1 AND result IS NULL", it.ID); err != nil {
			return nil, err
		}
	}
	for _, q := range []string{
		"UPDATE fragment_quotes SET expires_at=0 WHERE user_id=$1 AND result IS NULL",
		"DELETE FROM fragment_assignments WHERE user_id=$1",
		"DELETE FROM phone_aliases WHERE user_id=$1",
	} {
		if _, err := tx.Exec(ctx, q, user); err != nil {
			return nil, err
		}
	}
	return list, nil
}
