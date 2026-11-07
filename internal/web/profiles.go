package web

import (
	"math/big"
	"net/http"
	"strconv"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/wallet"
)

// Profiles has the bits of a profile that MTProto has no field for.
type Profiles struct {
	Auth   *Auth
	Wallet *wallet.Service
}

func (p *Profiles) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/profile/{id}/gram", p.Auth.handle(p.gram))
}

// gram is the GRAM balance shown under "last seen"; amount is null at zero,
// which the profile shows as nothing. {id} is the client's user id.
func (p *Profiles) gram(q *req) (any, error) {
	if _, _, err := p.Auth.Accounts.SessionUser(q.ctx, q.cookie("session")); err != nil {
		return nil, err
	}
	id, err := strconv.ParseInt(q.r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return nil, &account.Error{Status: 404, Message: "Account not found."}
	}
	u, ok, err := p.Auth.Accounts.User(q.ctx, ids.Server(id))
	if err != nil {
		return nil, err
	}
	if !ok || u.IsBot || u.IsSystem || u.Disabled {
		return map[string]any{"amount": nil}, nil
	}
	units, err := p.Wallet.TONBalance(q.ctx, u.ID)
	if err != nil {
		return nil, err
	}
	if units.Cmp(new(big.Int)) <= 0 {
		return map[string]any{"amount": nil}, nil
	}
	return map[string]any{"amount": wallet.FormatAmount(units, "TON")}, nil
}
