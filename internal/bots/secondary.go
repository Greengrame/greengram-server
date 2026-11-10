package bots

import (
	"context"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

var secondaryUsernameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{4,31}$`)

func IsSecondaryUsername(name string) bool {
	var suffix string
	name = strings.ToLower(strings.TrimPrefix(name, "@"))
	if !secondaryUsernameRe.MatchString(name) {
		return false
	}
	for _, suffix = range []string{"agent", "ai", "app", "game", "gay"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func checkSecondary(ctx context.Context, tx pgx.Tx, owner, bot int64, name string) error {
	var premium, frozen, taken bool
	var count int
	var err error
	if !IsSecondaryUsername(name) {
		return refuse("Secondary usernames must end with agent, ai, app, game, or gay.")
	}
	err = tx.QueryRow(ctx, "SELECT premium_until>extract(epoch FROM now())::bigint,account_state='frozen' FROM users WHERE id=$1 AND NOT disabled", owner).Scan(&premium, &frozen)
	if err != nil {
		return err
	}
	if frozen {
		return ErrFrozen
	}
	if !premium {
		return refuse("A Premium subscription is required for secondary bot usernames.")
	}
	err = tx.QueryRow(ctx, "SELECT count(*) FROM bot_secondary_usernames n JOIN bots b ON b.user_id=n.bot_id JOIN users u ON u.id=b.user_id WHERE b.owner_id=$1 AND b.user_id<>$2 AND NOT u.disabled", owner, bot).Scan(&count)
	if err != nil {
		return err
	}
	if count >= 2 {
		return refuse("Secondary usernames may be assigned to at most two owned bots.")
	}
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(username)=$1) OR EXISTS(SELECT 1 FROM channels WHERE lower(username)=$1) OR EXISTS(SELECT 1 FROM fragment_items WHERE kind='username' AND lower(title)='@'||$1) OR EXISTS(SELECT 1 FROM bot_secondary_usernames WHERE username=$1 AND bot_id<>$2)`, name, bot).Scan(&taken)
	if err != nil {
		return err
	}
	if taken {
		return refuse("This username is already taken.")
	}
	return nil
}

func (s *Service) SetSecondaryUsername(ctx context.Context, owner, bot int64, name string) error {
	var err error
	name = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "@"))
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var owned bool
		var err error
		_, err = tx.Exec(ctx, "LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE")
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM bots b JOIN users u ON u.id=b.user_id WHERE b.user_id=$1 AND b.owner_id=$2 AND b.kind='custom' AND NOT u.disabled)", bot, owner).Scan(&owned)
		if err != nil {
			return err
		}
		if !owned {
			return refuse("Bot not found.")
		}
		if name == "" {
			_, err = tx.Exec(ctx, "DELETE FROM bot_secondary_usernames WHERE bot_id=$1", bot)
			return err
		}
		err = checkSecondary(ctx, tx, owner, bot, name)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO bot_secondary_usernames(bot_id,username) VALUES($1,$2) ON CONFLICT(bot_id) DO UPDATE SET username=excluded.username", bot, name)
		return err
	})
	return err
}

func (s *Service) Managed(ctx context.Context, manager, bot int64) (bool, error) {
	var found bool
	var err error
	err = s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM bots b JOIN users u ON u.id=b.user_id WHERE b.user_id=$1 AND b.manager_bot_id=$2 AND NOT u.disabled)", bot, manager).Scan(&found)
	return found, err
}
