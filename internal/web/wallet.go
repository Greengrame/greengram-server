package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/wallet"
)

// Wallet serves the wallet mini-app's backend and the Stars-for-crypto
// purchase, same paths as greengram-web's api.js.
type Wallet struct {
	Auth   *Auth
	Wallet *wallet.Service
	// StarsChanged tells a user's apps the balance moved; may be nil
	StarsChanged func(user int64)
}

func (w *Wallet) Register(mux *http.ServeMux) {
	a := w.Auth
	mux.HandleFunc("/api/walt/", a.handle(w.walt))
	mux.HandleFunc("GET /api/stars/crypto/options", a.handle(w.options))
	mux.HandleFunc("POST /api/stars/crypto/quote", a.handle(w.quote))
	mux.HandleFunc("POST /api/stars/crypto/purchase", a.handle(w.purchase))
	// an app's top-up page: the token in the address is the only key, the
	// page has no session of its own
	mux.HandleFunc("GET /api/pay/stars/{token}", a.handle(func(q *req) (any, error) {
		return w.Wallet.TopupPage(q.ctx, q.r.PathValue("token"))
	}))
	mux.HandleFunc("POST /api/pay/stars/{token}/quote", a.handle(func(q *req) (any, error) {
		return w.Wallet.TopupQuote(q.ctx, q.r.PathValue("token"), q.body)
	}))
	mux.HandleFunc("GET /api/admin/users/{id}/crypto", a.handle(w.adminWallet))
	mux.HandleFunc("POST /api/admin/crypto-transactions", a.handle(w.adminTransaction))
}

func (w *Wallet) user(q *req) (account.User, string, error) {
	return w.Auth.appUser(q, bots.Wallet)
}

func (w *Wallet) walt(q *req) (any, error) {
	u, session, err := w.user(q)
	if err != nil {
		return nil, err
	}
	path := strings.TrimPrefix(q.r.URL.Path, "/api/walt")
	ctx := wallet.WithLaunchData(q.ctx, q.r.Header.Get("X-Telegram-Init-Data"))
	return w.Wallet.Walt(ctx, u, session, q.r.Method, path, q.body, q.r.URL.Query())
}

func (w *Wallet) options(q *req) (any, error) {
	u, _, err := w.user(q)
	if err != nil {
		return nil, err
	}
	return w.Wallet.StarsOptions(q.ctx, u)
}

func (w *Wallet) quote(q *req) (any, error) {
	u, _, err := w.user(q)
	if err != nil {
		return nil, err
	}
	return w.Wallet.QuoteStars(q.ctx, u, q.body)
}

func (w *Wallet) purchase(q *req) (any, error) {
	u, _, err := w.user(q)
	if err != nil {
		return nil, err
	}
	out, err := w.Wallet.BuyStars(q.ctx, u, q.body)
	if err == nil && w.StarsChanged != nil {
		w.StarsChanged(u.ID)
	}
	return out, err
}

func (w *Wallet) adminWallet(q *req) (any, error) {
	u, _, err := w.user(q)
	if err != nil {
		return nil, err
	}
	id, err := strconv.ParseInt(q.r.PathValue("id"), 10, 64)
	if err != nil {
		return nil, &account.Error{Status: 404, Message: "Person account not found."}
	}
	return w.Wallet.AdminWallet(q.ctx, u, id)
}

func (w *Wallet) adminTransaction(q *req) (any, error) {
	u, _, err := w.user(q)
	if err != nil {
		return nil, err
	}
	return w.Wallet.AdminTransaction(q.ctx, u, q.body)
}
