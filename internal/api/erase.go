package api

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/erase"
)

// RegisterErase serves account.deleteAccount and has svc sign deleted
// accounts out here (the admin panel and the inactivity job delete too).
func (a *API) RegisterErase(svc *erase.Service) {
	a.erase = svc
	svc.Finish = a.AccountErased
	handle(a, tlprofile.SemanticMethodAccountDeleteAccount, a.deleteAccount)
}

// eraseGrace is how long the deleting app's own key outlives the deletion,
// so the reply still reaches it.
const eraseGrace = 3 * time.Second

// deleteAccount deletes the caller's account. With a password on it the
// session must have passed POST /api/account/deletion in the last minutes:
// the password never travels over MTProto, so the stock SRP field can't
// carry it. The last active administrator can't leave this way.
func (a *API) deleteAccount(ctx context.Context, r *tg.AccountDeleteAccountRequest) (any, error) {
	me, hash, err := a.session(ctx)
	if err != nil {
		return nil, err
	}
	if me.IsBot || me.IsSystem {
		return nil, tgerr.New(400, "BOT_METHOD_INVALID")
	}
	ok, err := a.accounts.DeletionConfirmed(ctx, me, hash)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, tgerr.New(400, "PASSWORD_REQUIRED")
	}
	var res erase.Result
	err = pgx.BeginFunc(ctx, a.erase.DB, func(tx pgx.Tx) error {
		if me.Role == "admin" {
			var others int
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM users WHERE role='admin' AND NOT disabled AND id<>$1", me.ID).Scan(&others); err != nil {
				return err
			}
			if others == 0 {
				return tgerr.New(403, "ADMIN_LAST_CANNOT_DELETE")
			}
		}
		var err error
		res, err = erase.Erase(ctx, tx, erase.Request{User: me.ID, Actor: me.ID, Source: erase.SourceSelf, Reason: r.Reason}, time.Now())
		return err
	})
	if errors.Is(err, erase.ErrDeleted) || errors.Is(err, erase.ErrNotFound) {
		return nil, errUnregistered
	}
	if err != nil {
		return nil, err
	}
	slog.Info("api: account deleted by its owner", "user", me.ID, "keys", len(res.Keys))
	a.finishErase(res, edge.CallFrom(ctx).PermKeyID)
	return true, nil
}

// AccountErased signs a deleted account's apps out and tells its chats.
func (a *API) AccountErased(res erase.Result) { a.finishErase(res, 0) }

// finishErase drops what erase.Erase deleted from the store: every key but
// keep goes now, keep (the key asking) and its temp keys a moment later.
func (a *API) finishErase(res erase.Result, keep int64) {
	var now, later []int64
	for _, perm := range res.Perms {
		if perm == keep {
			continue
		}
		a.srv.StopPushes(perm)
		a.hub.leaveKey(perm)
	}
	bound := map[int64]bool{}
	if keep != 0 {
		bound[keep] = true
		for _, temp := range a.srv.BoundTo(keep) {
			bound[temp] = true
		}
	}
	for _, id := range res.Keys {
		if bound[id] {
			later = append(later, id)
		} else {
			now = append(now, id)
		}
	}
	if keep != 0 && !slices.Contains(later, keep) {
		later = append(later, keep)
	}
	a.srv.DropKeys(now)
	if len(later) > 0 {
		time.AfterFunc(eraseGrace, func() {
			a.srv.StopPushes(keep)
			a.hub.leaveKey(keep)
			a.srv.DropKeys(later)
		})
	}
	if len(res.Events) > 0 {
		a.deliverChannel(res.Events, edge.Session{})
	}
}
