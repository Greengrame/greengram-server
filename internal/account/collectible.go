package account

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Fragment collectibles change who a phone number or username belongs to.
// The rules are fragment-identities.js; the tables belong to
// internal/fragment, read here with plain SQL so sign-in doesn't import it.

// phoneAccount is who signs in with phone. A primary number wins unless it
// is listed for sale or owned by someone else; an owned, unlisted
// collectible signs in its owner; then numbers kept after a swap.
func (s *Service) phoneAccount(ctx context.Context, phone string) (User, bool, error) {
	lookup := s.secret.PhoneLookup(phone)
	var owner *int64
	var listed, isItem bool
	err := s.db.QueryRow(ctx, "SELECT owner_id, listed FROM fragment_items WHERE kind='number' AND title=$1", phone).Scan(&owner, &listed)
	if err == nil {
		isItem = true
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, err
	}
	u, ok, err := s.userByPhone(ctx, phone)
	if err != nil {
		return User{}, false, err
	}
	if ok && (!isItem || !listed && owner != nil && *owner == u.ID) {
		return u, true, nil
	}
	if isItem {
		if listed || owner == nil {
			return User{}, false, nil
		}
		return s.User(ctx, *owner)
	}
	for _, q := range []string{
		"SELECT user_id FROM phone_aliases WHERE lookup=$1",
		"SELECT user_id FROM fragment_assignments WHERE previous_lookup=$1",
	} {
		var id int64
		err := s.db.QueryRow(ctx, q, lookup).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return User{}, false, err
		}
		return s.User(ctx, id)
	}
	return User{}, false, nil
}

// collectibleCode refuses sign-ups under a collectible country code longer
// than three digits (+8881...): those numbers only ever sign in.
func collectibleCode(ctx context.Context, tx pgx.Tx, phone string) error {
	var hit bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fragment_items WHERE kind='number' AND length(country_code) > 3
		AND starts_with($1, country_code))`, strings.TrimPrefix(phone, "+")).Scan(&hit)
	if err == nil && hit {
		err = fail(400, "Collectible country codes longer than three digits can only be used to sign in to an existing account.")
	}
	return err
}

// reservedOnFragment is the old sign-up trigger: any listing holds the name.
func reservedOnFragment(ctx context.Context, tx pgx.Tx, username string) error {
	var hit bool
	err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM fragment_items WHERE kind='username' AND lower(title)='@'||lower($1)) OR EXISTS(SELECT 1 FROM bot_secondary_usernames WHERE username=lower($1))", username).Scan(&hit)
	if err == nil && hit {
		err = fail(409, "That username is reserved on Fragment.")
	}
	return err
}

// retainedPhone is a number still tied to an account through a collectible.
func retainedPhone(ctx context.Context, tx pgx.Tx, lookup string) error {
	var hit bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM phone_aliases WHERE lookup=$1)
		OR EXISTS(SELECT 1 FROM fragment_assignments WHERE previous_lookup=$1)`, lookup).Scan(&hit)
	if err == nil && hit {
		err = fail(409, "This phone number is retained by an existing account.")
	}
	return err
}

func reservedNumber(ctx context.Context, tx pgx.Tx, phone string) error {
	var hit bool
	err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM fragment_items WHERE kind='number' AND title=$1)", phone).Scan(&hit)
	if err == nil && hit {
		err = fail(409, "This number is reserved on Fragment.")
	}
	return err
}
