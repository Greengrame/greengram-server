package fragment

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"

	"github.com/jackc/pgx/v5"
)

const quoteTTL = 300

// Quote prices a listing for five minutes; Purchase pays it once.
func (s *Service) Quote(ctx context.Context, userID int64, itemID string) (map[string]any, error) {
	me, ok, err := s.person(ctx, s.db, userID, false)
	if err != nil || !ok {
		return nil, notFound(err)
	}
	it, found, err := itemBy(ctx, s.db, "id=$1", itemID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fail(404, "This listing is unavailable.")
	}
	if !it.Listed || it.Kind == "gift" || it.Owner == userID {
		return nil, fail(409, "This listing is not available for purchase.")
	}
	bal, err := tonBalance(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	if bal.Cmp(it.Units) < 0 {
		return nil, errNoGram
	}
	id := newID()
	if _, err := s.db.Exec(ctx, "INSERT INTO fragment_quotes(id, user_id, item_id, units, expires_at) VALUES ($1, $2, $3, $4::numeric, $5)",
		id, userID, it.ID, it.Units.String(), s.unix()+quoteTTL); err != nil {
		return nil, err
	}
	return map[string]any{
		"id": id, "itemId": it.ID, "title": it.Title, "countryCode": it.CountryCode, "displayTitle": it.displayTitle(),
		"isAssigned": it.Kind == "number" && me.Lookup != "" && me.Lookup == s.lookup(it.Title),
		"amount":     ton(it.Units), "asset": "GRAM", "expiresIn": quoteTTL,
	}, nil
}

func (s *Service) Purchase(ctx context.Context, userID int64, quoteID string) (map[string]any, error) {
	var out map[string]any
	var seller, released int64
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var itemID, units string
		var expires int
		var result []byte
		err := tx.QueryRow(ctx, "SELECT item_id, units::text, expires_at, result FROM fragment_quotes WHERE id=$1 AND user_id=$2 FOR UPDATE",
			quoteID, userID).Scan(&itemID, &units, &expires, &result)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(404, "Purchase quote not found.")
		}
		if err != nil {
			return err
		}
		if result != nil {
			return json.Unmarshal(result, &out)
		}
		if expires <= s.unix() {
			return fail(410, "This quote expired. Please choose the listing again.")
		}
		it, found, err := itemBy(ctx, tx, "id=$1 FOR UPDATE", itemID)
		if err != nil {
			return err
		}
		if !found || !it.Listed || it.Kind == "gift" || it.Owner == userID || it.Units.String() != units {
			return fail(409, "This listing is no longer available.")
		}
		// buyer and seller in id order, the same order every money path uses
		ids := []int64{userID}
		if it.Owner != 0 {
			ids = append(ids, it.Owner)
		}
		if _, err := tx.Exec(ctx, "SELECT 1 FROM users WHERE id = ANY($1) ORDER BY id FOR UPDATE", ids); err != nil {
			return err
		}
		if me, ok, err := s.person(ctx, tx, userID, false); err != nil {
			return err
		} else if !ok || !me.Active {
			return fail(403, "Account unavailable.")
		}
		if it.Owner != 0 {
			if seller, ok, err := s.person(ctx, tx, it.Owner, false); err != nil {
				return err
			} else if !ok || !seller.Active {
				return fail(409, "The seller is unavailable.")
			}
		}
		if it.Kind == "username" {
			// a channel's handle changes hands: only its creator can have put
			// it up, so one who has since lost the channel can't sell it, and
			// the channel goes private with the sale
			name := strings.ToLower(strings.TrimPrefix(it.Title, "@"))
			var id int64
			err := tx.QueryRow(ctx, "SELECT id FROM channels WHERE lower(username)=$1 AND NOT deleted FOR UPDATE", name).Scan(&id)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if id != 0 {
				mine, err := s.ownChannelHolds(ctx, tx, name, it.Owner)
				if err != nil {
					return err
				}
				if !mine {
					return fail(409, "This listing is no longer available.")
				}
				if _, err := tx.Exec(ctx, "UPDATE channels SET username=NULL WHERE id=$1", id); err != nil {
					return err
				}
				released = id
			}
		}
		now := s.unix()
		if err := moveTON(ctx, tx, userID, new(big.Int).Neg(it.Units)); err != nil {
			return err
		}
		if err := tonEvent(ctx, tx, "fragment:"+quoteID, userID, new(big.Int).Neg(it.Units), "fragment_purchase", 0, now); err != nil {
			return err
		}
		if it.Owner != 0 {
			if err := moveTON(ctx, tx, it.Owner, it.Units); err != nil {
				return err
			}
			if err := tonEvent(ctx, tx, "fragment:"+quoteID+":seller", it.Owner, it.Units, "fragment_sale", userID, now); err != nil {
				return err
			}
		}
		seller = it.Owner
		if _, err := tx.Exec(ctx, "UPDATE fragment_items SET owner_id=$2, listed=false, purchased_at=$3, active=false, position=0 WHERE id=$1", it.ID, userID, now); err != nil {
			return err
		}
		bal, err := tonBalance(ctx, tx, userID)
		if err != nil {
			return err
		}
		out = map[string]any{"ok": true, "itemId": it.ID, "title": it.Title, "balance": ton(bal)}
		raw, _ := json.Marshal(out)
		_, err = tx.Exec(ctx, "UPDATE fragment_quotes SET result=$2 WHERE id=$1", quoteID, raw)
		return err
	})
	if err != nil {
		return out, err
	}
	s.usernamesChanged(userID)
	if seller != 0 {
		s.usernamesChanged(seller)
	}
	if released != 0 && s.ChannelReleased != nil {
		s.ChannelReleased(released)
	}
	return out, nil
}
