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
	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/messages"
)

// registerPhone is Change Number. The stock screens ask for a code sent to
// the new number; no SMS leaves this server, so the code arrives in the
// service notifications chat of the account that asked.
func (a *API) registerPhone() {
	handle(a, tlprofile.SemanticMethodAccountSendChangePhoneCode, a.sendChangePhoneCode)
	handle(a, tlprofile.SemanticMethodAccountChangePhone, a.changePhone)
}

func phoneErr(err error) error {
	var e *account.Error
	if errors.As(err, &e) {
		if e.Status == 429 {
			return tgerr.New(420, "FLOOD_WAIT_600")
		}
		return tgerr.New(400, e.Message)
	}
	return err
}

func (a *API) sendChangePhoneCode(ctx context.Context, r *tg.AccountSendChangePhoneCodeRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	from := ids.Announcements()
	if from == 0 {
		return nil, tgerr.New(400, "PHONE_NUMBER_INVALID")
	}
	pc, err := a.accounts.StartPhoneChange(ctx, me, r.PhoneNumber)
	if err != nil {
		return nil, phoneErr(err)
	}
	text := "Code to change your number to " + pc.Phone + ": " + pc.Code + "\n\nIf you didn't ask for it, someone else is using one of your sessions. Check Settings > Devices."
	s, err := a.msgs.Send(ctx, from, me.ID, messages.Draft{Text: text})
	if err != nil {
		return nil, err
	}
	a.deliver(s.Events, edge.Session{})
	return &tg.AuthSentCode{Type: &tg.AuthSentCodeTypeApp{Length: account.PhoneCodeLength}, PhoneCodeHash: pc.Hash}, nil
}

func (a *API) changePhone(ctx context.Context, r *tg.AccountChangePhoneRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	u, err := a.accounts.ChangePhone(ctx, me.ID, r.PhoneNumber, r.PhoneCodeHash, r.PhoneCode)
	if err != nil {
		return nil, phoneErr(err)
	}
	slog.Info("api: number changed", "user", u.ID)
	// same news as an admin reset: the account's apps and whoever may see
	// the number get the new one
	a.IdentityReset(u.ID)
	return a.selfUser(u), nil
}
