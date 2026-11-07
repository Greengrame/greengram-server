package api

import (
	"context"
	"errors"
	"log/slog"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/edge"
)

// Apps sign in over HTTPS (/api/auth/*, the same flow as the website), then
// hand the session cookie over MTProto in auth.importAuthorization. The
// password never touches MTProto and stock clients don't need SRP.

func (a *API) registerAuth() {
	a.srv.OnKeyDestroyed = func(ctx context.Context, keyID int64) error {
		if err := a.accounts.UnbindKey(ctx, keyID); err != nil {
			return err
		}
		a.srv.StopPushes(keyID)
		a.hub.leaveKey(keyID)
		return nil
	}
	handle(a, tlprofile.SemanticMethodAuthImportAuthorization, a.importAuthorization)
	handle(a, tlprofile.SemanticMethodAuthLogOut, func(ctx context.Context, _ *tg.AuthLogOutRequest) (any, error) {
		c := edge.CallFrom(ctx)
		if err := a.accounts.UnbindKey(ctx, c.PermKeyID); err != nil {
			return nil, err
		}
		a.srv.StopPushes(c.PermKeyID)
		a.hub.leaveKey(c.PermKeyID)
		return &tg.AuthLoggedOut{}, nil
	})
	handle(a, tlprofile.SemanticMethodAuthResetAuthorizations, func(ctx context.Context, _ *tg.AuthResetAuthorizationsRequest) (any, error) {
		me, hash, err := a.session(ctx)
		if err != nil {
			return nil, err
		}
		return true, a.accounts.EndOtherSessions(ctx, me.ID, hash)
	})
	// the stock phone-code and QR-export screens can't work here
	useOurs := func(context.Context, any) (any, error) { return nil, tgerr.New(400, "USE_GREENGRAM_LOGIN") }
	handle(a, tlprofile.SemanticMethodAuthSendCode, func(ctx context.Context, r *tg.AuthSendCodeRequest) (any, error) { return useOurs(ctx, r) })
	handle(a, tlprofile.SemanticMethodAuthExportLoginToken, func(ctx context.Context, r *tg.AuthExportLoginTokenRequest) (any, error) { return useOurs(ctx, r) })
}

func (a *API) importAuthorization(ctx context.Context, r *tg.AuthImportAuthorizationRequest) (any, error) {
	c := edge.CallFrom(ctx)
	token := string(r.Bytes)
	if len(token) != 64 {
		return nil, tgerr.New(400, "AUTH_BYTES_INVALID")
	}
	ip := callIP(c)
	u, err := a.accounts.BindKey(ctx, c.PermKeyID, token, serverID(r.ID), account.AppInfo{
		APIID:         c.Client.APIID,
		DeviceModel:   c.Client.DeviceModel,
		SystemVersion: c.Client.SystemVersion,
		AppVersion:    c.Client.AppVersion,
		IP:            ip,
	})
	var e *account.Error
	if errors.As(err, &e) {
		if e.Message == "USER_ID_INVALID" {
			return nil, tgerr.New(400, "USER_ID_INVALID")
		}
		return nil, tgerr.New(400, "AUTH_BYTES_INVALID")
	}
	if err != nil {
		return nil, err
	}
	// the next signed-in call joins the new account's stream
	a.srv.StopPushes(c.PermKeyID)
	a.hub.leaveKey(c.PermKeyID)
	slog.Info("api: signed in", "user", u.ID, "device", c.Client.DeviceModel, "app", c.Client.AppVersion)
	return &tg.AuthAuthorization{User: tgUser(u, true)}, nil
}
