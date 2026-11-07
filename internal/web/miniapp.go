package web

import (
	"errors"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
)

var errRelaunch = &account.Error{Status: 401, Message: "Session expired. Restart the app to continue."}

// appUser is who is using a built-in bot's mini app (kind is the bot's):
// the signed launch data the app forwards in X-Telegram-Init-Data, else
// the site's session cookie. The Android app has no cookie, and in Web A
// the cookie slot isn't known to the frame, so the launch data wins. The
// hash is the launching session's, "" when it has ended.
func (a *Auth) appUser(q *req, kind string) (account.User, string, error) {
	raw := q.r.Header.Get("X-Telegram-Init-Data")
	if raw == "" || a.Bots == nil {
		u, hash, err := a.Accounts.SessionUser(q.ctx, q.cookie("session"))
		if err == nil {
			err = q.writable(u)
		}
		return u, hash, err
	}
	l, err := a.Bots.Launched(q.ctx, kind, raw)
	if errors.Is(err, bots.ErrInitData) {
		return account.User{}, "", errRelaunch
	}
	if err != nil {
		return account.User{}, "", err
	}
	u, ok, err := a.Accounts.User(q.ctx, l.User)
	if err != nil {
		return account.User{}, "", err
	}
	if !ok || u.Disabled || u.IsSystem || u.IsBot {
		return account.User{}, "", errRelaunch
	}
	hashes, err := a.Accounts.SessionHashes(q.ctx, u.ID)
	if err != nil {
		return account.User{}, "", err
	}
	hash := l.Session(hashes)
	if hash == "" && l.Fields.Get("gg_session") != "" {
		// the session that opened the app signed out
		return account.User{}, "", errRelaunch
	}
	return u, hash, q.writable(u)
}
