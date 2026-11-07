package account

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Alias is a collectible username the user owns besides their primary one.
// Active ones are on the profile and resolve to the user.
type Alias struct {
	Name   string
	Active bool
}

const maxActiveAliases = 10

// aliasesCol reads the user's owned, unlisted names as "+name" or "-name". A
// name a channel is using stays off the profile, whoever owns it.
// fragment_items belongs to internal/fragment, plain SQL like collectible.go.
func aliasesCol(p string) string {
	return "ARRAY(SELECT CASE WHEN f.active THEN '+' ELSE '-' END || substr(f.title, 2) FROM fragment_items f WHERE f.owner_id = " + p +
		"id AND f.kind = 'username' AND NOT f.listed AND NOT EXISTS(SELECT 1 FROM channels c WHERE lower(c.username) = lower(substr(f.title, 2)) AND NOT c.deleted)" +
		" ORDER BY f.position, f.id)"
}

func parseAliases(raw []string, primary string) []Alias {
	var out []Alias
	for _, v := range raw {
		if name := v[1:]; !strings.EqualFold(name, primary) {
			out = append(out, Alias{Name: name, Active: v[0] == '+'})
		}
	}
	return out
}

func (s *Service) ownsCollectible(ctx context.Context, user int64, name string) (bool, error) {
	var owned bool
	err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM fragment_items WHERE kind='username' AND lower(title)='@'||$1 AND owner_id=$2 AND NOT listed)", name, user).Scan(&owned)
	return owned, err
}

// UserByAlias finds who shows name as an active collectible username.
func (s *Service) UserByAlias(ctx context.Context, name string) (User, bool, error) {
	var id int64
	err := s.db.QueryRow(ctx, "SELECT owner_id FROM fragment_items WHERE kind='username' AND lower(title)='@'||$1 AND active AND NOT listed AND owner_id IS NOT NULL", normUsername(name)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	return s.User(ctx, id)
}

// ToggleUsername is account.toggleUsername: a collectible goes on or off the
// profile. The primary name's switch is its visibility.
func (s *Service) ToggleUsername(ctx context.Context, u User, name string, active bool) error {
	name = normUsername(name)
	if strings.EqualFold(name, u.Username) {
		if u.UsernamePublic == active {
			return fail(400, "USERNAME_NOT_MODIFIED")
		}
		_, err := s.db.Exec(ctx, "UPDATE users SET username_public=$2 WHERE id=$1", u.ID, active)
		return err
	}
	on := 0
	for _, al := range u.Aliases {
		if al.Active {
			on++
		}
		if strings.EqualFold(al.Name, name) && al.Active == active {
			return fail(400, "USERNAME_NOT_MODIFIED")
		}
	}
	if active && on >= maxActiveAliases {
		return fail(400, "USERNAMES_ACTIVE_TOO_MUCH")
	}
	if active {
		// a channel of theirs may still have it
		var held bool
		if err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM channels WHERE lower(username)=$1 AND NOT deleted)", name).Scan(&held); err != nil {
			return err
		}
		if held {
			return fail(400, "USERNAME_OCCUPIED")
		}
	}
	tag, err := s.db.Exec(ctx, "UPDATE fragment_items SET active=$3 WHERE kind='username' AND lower(title)='@'||$1 AND owner_id=$2 AND NOT listed", name, u.ID, active)
	if err == nil && tag.RowsAffected() == 0 {
		return fail(400, "USERNAME_INVALID")
	}
	return err
}

// ReorderUsernames is account.reorderUsernames. Only collectibles move: the
// primary name is the one people sign in with and stays first.
func (s *Service) ReorderUsernames(ctx context.Context, u User, order []string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		for i, name := range order {
			if _, err := tx.Exec(ctx, "UPDATE fragment_items SET position=$3 WHERE kind='username' AND lower(title)='@'||$1 AND owner_id=$2", normUsername(name), u.ID, i); err != nil {
				return err
			}
		}
		return nil
	})
}
