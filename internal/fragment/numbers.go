package fragment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/password"
)

// asset is something the user owns: a bought item, their own username, or
// a number they sign in with that never was a listing.
type asset struct {
	item
	fromItem bool
	primary  bool
	assigned bool
	// a public username on a channel this user created, and that channel
	channel      int64
	channelTitle string
}

func (a asset) json() map[string]any {
	var out map[string]any
	if a.fromItem {
		out = map[string]any{
			"id": a.ID, "kind": a.Kind, "title": a.Title, "displayTitle": a.displayTitle(), "countryCode": a.CountryCode,
			"amount": ton(a.Units), "listed": a.Listed, "ownerId": a.Owner, "isOwned": true,
			"isPrimary": a.primary, "isAssigned": a.assigned,
		}
	} else {
		out = map[string]any{"id": a.ID, "kind": a.Kind, "title": a.Title, "amount": "", "isOwned": true, "isPrimary": a.primary, "listed": false}
		if a.Kind == "number" {
			out["isAssigned"] = a.assigned
		}
	}
	if a.channel != 0 {
		out["channelId"], out["channelTitle"] = a.channel, a.channelTitle
	}
	return out
}

// owned is ownedFragmentAssets: bought items, the primary username, and the
// current, pre-assignment and aliased numbers.
func (s *Service) owned(ctx context.Context, q querier, me person) ([]asset, error) {
	list, err := s.items(ctx, q, "WHERE owner_id=$1 ORDER BY id", me.ID)
	if err != nil {
		return nil, err
	}
	var out []asset
	hasUsername := false
	for _, it := range list {
		if it.Kind != "username" && it.Kind != "number" {
			continue
		}
		a := asset{item: it, fromItem: true}
		if it.Kind == "username" {
			a.primary = it.Title == "@"+me.Username
			hasUsername = hasUsername || a.primary
		} else {
			a.primary = it.Title == me.Phone
			a.assigned = a.primary
		}
		out = append(out, a)
	}
	if !hasUsername {
		out = append([]asset{{item: item{ID: "primary:username:" + me.Username, Kind: "username", Title: "@" + me.Username}, primary: true}}, out...)
	}
	numbers := []string{me.Phone}
	var prev *string
	err = q.QueryRow(ctx, "SELECT previous_phone FROM fragment_assignments WHERE user_id=$1", me.ID).Scan(&prev)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if prev != nil && *prev != "" {
		p, err := s.key.DecryptPhone(*prev, fmt.Sprintf("fragment-phone:%d", me.ID))
		if err != nil {
			return nil, err
		}
		numbers = append(numbers, p)
	}
	aliases, err := s.aliases(ctx, q, me.ID)
	if err != nil {
		return nil, err
	}
	numbers = append(numbers, aliases...)
	seen := map[string]bool{}
	for _, n := range numbers {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		have := false
		for _, a := range out {
			have = have || a.Kind == "number" && a.Title == n
		}
		if !have {
			primary := s.lookup(n) == me.Lookup
			out = append(out, asset{item: item{ID: "retained:number:" + n[1:], Kind: "number", Title: n}, primary: primary, assigned: primary})
		}
	}
	return s.withChannels(ctx, q, me, out)
}

// withChannels adds the public usernames of the channels me created.
func (s *Service) withChannels(ctx context.Context, q querier, me person, out []asset) ([]asset, error) {
	type held struct {
		id          int64
		title, name string
	}
	rows, err := q.Query(ctx, `SELECT c.id, c.title, lower(c.username) FROM channels c JOIN channel_members m ON m.channel_id = c.id
		WHERE m.user_id=$1 AND m.status='creator' AND NOT c.deleted AND c.username IS NOT NULL ORDER BY c.id`, me.ID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (held, error) {
		var h held
		return h, r.Scan(&h.id, &h.title, &h.name)
	})
	if err != nil {
		return nil, err
	}
	for _, h := range list {
		found := false
		for i := range out {
			if out[i].Kind == "username" && out[i].Title == "@"+h.name {
				out[i].channel, out[i].channelTitle, found = h.id, h.title, true
			}
		}
		if !found {
			out = append(out, asset{item: item{ID: "channel:username:" + h.name, Kind: "username", Title: "@" + h.name}, channel: h.id, channelTitle: h.title})
		}
	}
	return out, nil
}

func (s *Service) aliases(ctx context.Context, q querier, user int64) ([]string, error) {
	rows, err := q.Query(ctx, "SELECT lookup, phone FROM phone_aliases WHERE user_id=$1 ORDER BY lookup", user)
	if err != nil {
		return nil, err
	}
	type alias struct{ lookup, phone string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (alias, error) {
		var a alias
		return a, r.Scan(&a.lookup, &a.phone)
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list))
	for _, a := range list {
		p, err := s.key.DecryptPhone(a.phone, fmt.Sprintf("phone-alias:%d:%s", user, a.lookup))
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// remember keeps a number the account stops showing as primary usable for
// its sign-in, unless it is a collectible (then ownership decides).
func (s *Service) remember(ctx context.Context, tx pgx.Tx, user int64, number string) error {
	if number == "" {
		return nil
	}
	lookup := s.lookup(number)
	it, found, err := itemBy(ctx, tx, "kind='number' AND title=$1", number)
	if err != nil {
		return err
	}
	if found {
		if it.Owner != user || it.Listed {
			return fail(409, "This number belongs to a different collectible owner.")
		}
		return nil
	}
	var owner int64
	err = tx.QueryRow(ctx, "SELECT user_id FROM phone_aliases WHERE lookup=$1", lookup).Scan(&owner)
	if err == nil {
		if owner != user {
			return fail(409, "This number belongs to another account.")
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	enc, err := s.key.EncryptPhone(number, fmt.Sprintf("phone-alias:%d:%s", user, lookup))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO phone_aliases(lookup, user_id, phone) VALUES ($1, $2, $3)", lookup, user, enc)
	return err
}

// phoneOwner is account's sign-in resolution (phoneAccount), by id; 0 is nobody.
func phoneOwner(ctx context.Context, q querier, number, lookup string) (int64, error) {
	it, isItem, err := itemBy(ctx, q, "kind='number' AND title=$1", number)
	if err != nil {
		return 0, err
	}
	var primary int64
	err = q.QueryRow(ctx, "SELECT id FROM users WHERE phone_lookup=$1", lookup).Scan(&primary)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	if primary != 0 && (!isItem || !it.Listed && it.Owner == primary) {
		return primary, nil
	}
	if isItem {
		if it.Listed {
			return 0, nil
		}
		return it.Owner, nil
	}
	for _, sql := range []string{
		"SELECT user_id FROM phone_aliases WHERE lookup=$1",
		"SELECT user_id FROM fragment_assignments WHERE previous_lookup=$1",
	} {
		var id int64
		err := q.QueryRow(ctx, sql, lookup).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, err
		}
	}
	return 0, nil
}

// checkPassword is the "enter your current sign-in password" step; only a
// real argon2 hash counts (accounts made without one can't confirm).
func checkPassword(pw, hash string) bool {
	if len(pw) > 256 || len(hash) == 0 || hash[0] != '$' {
		password.Check(pw, "")
		return false
	}
	return password.Check(pw, hash)
}

// Assign makes an owned, unlisted number the account's primary one (clear
// goes back to the number it had before). The calling session survives,
// the others are signed out.
func (s *Service) Assign(ctx context.Context, userID int64, sessionHash, itemID, pw string, clear bool) (map[string]any, error) {
	me, ok, err := s.person(ctx, s.db, userID, false)
	if err != nil || !ok {
		return nil, notFound(err)
	}
	if !checkPassword(pw, me.Password) {
		return nil, fail(400, "Enter your current sign-in password.")
	}
	err = s.tx(ctx, func(tx pgx.Tx) error {
		cur, ok, err := s.person(ctx, tx, userID, true)
		if err != nil {
			return err
		}
		if !ok || !cur.Active || cur.Password != me.Password {
			return fail(409, "Account settings changed. Please try again.")
		}
		assets, err := s.owned(ctx, tx, cur)
		if err != nil {
			return err
		}
		var it *asset
		for i := range assets {
			if a := assets[i]; a.ID == itemID && a.Kind == "number" && !a.Listed {
				it = &assets[i]
				break
			}
		}
		if it == nil {
			return fail(403, "Choose an unlisted number that you own.")
		}
		var prevPhone, prevLookup *string
		err = tx.QueryRow(ctx, "SELECT previous_phone, previous_lookup FROM fragment_assignments WHERE user_id=$1", userID).Scan(&prevPhone, &prevLookup)
		hasPrev := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		number := it.Title
		if clear {
			number = ""
			if prevPhone != nil && *prevPhone != "" {
				if number, err = s.key.DecryptPhone(*prevPhone, fmt.Sprintf("fragment-phone:%d", userID)); err != nil {
					return err
				}
			} else {
				for _, a := range assets {
					if a.Kind == "number" && a.Title != it.Title && !a.Listed {
						number = a.Title
						break
					}
				}
			}
			if number == "" {
				return fail(409, "Choose another owned number as the primary number.")
			}
		}
		lookup := s.lookup(number)
		occupied, err := phoneOwner(ctx, tx, number, lookup)
		if err != nil {
			return err
		}
		if occupied != 0 && occupied != userID {
			return fail(409, "The previous sign-in number is now in use. Assign another owned number instead.")
		}
		if err := s.remember(ctx, tx, userID, cur.Phone); err != nil {
			return err
		}
		_, isItem, err := itemBy(ctx, tx, "id=$1", it.ID)
		if err != nil {
			return err
		}
		if clear || !isItem {
			if _, err := tx.Exec(ctx, "DELETE FROM fragment_assignments WHERE user_id=$1", userID); err != nil {
				return err
			}
		} else {
			saved := prevPhone
			if (saved == nil || *saved == "") && cur.Phone != "" {
				enc, err := s.key.EncryptPhone(cur.Phone, fmt.Sprintf("fragment-phone:%d", userID))
				if err != nil {
					return err
				}
				saved = &enc
			}
			savedLookup := prevLookup
			if !hasPrev || savedLookup == nil || *savedLookup == "" {
				savedLookup = nil
				if cur.Lookup != "" {
					savedLookup = &cur.Lookup
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO fragment_assignments(user_id, item_id, previous_phone, previous_lookup) VALUES ($1, $2, $3, $4)
				ON CONFLICT (user_id) DO UPDATE SET item_id = excluded.item_id`, userID, it.ID, saved, savedLookup); err != nil {
				return err
			}
		}
		enc, err := s.key.EncryptPhone(number, fmt.Sprintf("user:%d", userID))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE users SET phone=$2, phone_lookup=$3 WHERE id=$1", userID, enc, lookup); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1 AND token_hash<>$2", userID, sessionHash)
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "assigned": !clear}, nil
}
