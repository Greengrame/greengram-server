package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/stars"
	"github.com/Greengrame/greengram-server/internal/wallet"
)

// Buying Stars from an app the way Telegram's apps do it: top-up options,
// a payments.paymentForm whose url the app opens in its own browser, and
// payments.sendPaymentForm with what that page handed back. The page is
// the wallet's (/pay/stars/<token>): it prices the Stars in an asset the
// person holds, and the wallet settles it. Web A keeps its own purchase
// screen, which talks to the wallet directly.

// RegisterTopup turns the top-up screens on; without it the options are
// empty and the apps offer nothing to buy.
func (a *API) RegisterTopup(w *wallet.Service) { a.topup.Store(w) }

func (a *API) getStarsTopupOptions(ctx context.Context, _ *tg.PaymentsGetStarsTopupOptionsRequest) (any, error) {
	out := []tg.StarsTopupOption{}
	if a.topup.Load() == nil {
		return out, nil
	}
	for _, o := range a.stars.TopupOptions(ctx) {
		out = append(out, tg.StarsTopupOption{Extended: o.Extended, Stars: o.Stars, Currency: stars.TopupCurrency, Amount: o.Amount})
	}
	return out, nil
}

func (a *API) topupForm(ctx context.Context, me account.User, p *tg.InputStorePaymentStarsTopup, theme tg.DataJSON) (any, error) {
	if a.topup.Load() == nil {
		return nil, tgerr.New(400, "INVOICE_INVALID")
	}
	f, err := a.stars.TopupForm(ctx, me.ID, p.Stars, p.Currency, p.Amount)
	if err != nil {
		return nil, err
	}
	bot, users, err := a.paymentBot(ctx, me.ID, stars.WalletAccount)
	if err != nil {
		return nil, err
	}
	title := fmt.Sprintf("%d Stars", f.Stars)
	u := strings.TrimRight(a.in.BaseURL, "/") + "/pay/stars/" + f.PayToken
	// the app's own colors win over the system's on the page
	if dark, ok := darkTheme(theme.Data); ok && dark {
		u += "#dark"
	} else if ok {
		u += "#light"
	}
	return &tg.PaymentsPaymentForm{FormID: f.ID, BotID: clientID(bot), Title: title, Description: "Paid from your Wallet",
		Invoice:    tg.Invoice{Currency: stars.TopupCurrency, Prices: []tg.LabeledPrice{{Label: title, Amount: p.Amount}}},
		ProviderID: clientID(bot), URL: u, Users: users}, nil
}

// darkTheme reads bg_color off the theme_params the apps send.
func darkTheme(data string) (dark, ok bool) {
	var t struct {
		Bg string `json:"bg_color"`
	}
	if json.Unmarshal([]byte(data), &t) != nil || len(t.Bg) != 7 || t.Bg[0] != '#' {
		return false, false
	}
	n, err := strconv.ParseUint(t.Bg[1:], 16, 32)
	if err != nil {
		return false, false
	}
	r, g, b := n>>16&255, n>>8&255, n&255
	return r*299+g*587+b*114 < 128000, true
}

var errCredentials = tgerr.New(400, "PAYMENT_CREDENTIALS_INVALID")

// sendPaymentForm pays a top-up form. The credentials are the page's
// {"quote": id}; everything else about the price is in the form.
func (a *API) sendPaymentForm(ctx context.Context, r *tg.PaymentsSendPaymentFormRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	w := a.topup.Load()
	if w == nil || r.FormID == 0 {
		return nil, tgerr.New(400, "FORM_ID_EMPTY")
	}
	kind, err := a.stars.FormKind(ctx, me.ID, r.FormID)
	if err != nil {
		return nil, err
	}
	if kind != stars.KindTopup {
		// Stars forms are paid with sendStarsForm
		return nil, tgerr.New(400, "FORM_ID_EMPTY")
	}
	var invoiceStars int64
	if inv, ok := r.Invoice.(*tg.InputInvoiceStars); ok {
		if p, ok := inv.Purpose.(*tg.InputStorePaymentStarsTopup); ok {
			invoiceStars = p.Stars
		}
	}
	cred, ok := r.Credentials.(*tg.InputPaymentCredentials)
	if !ok {
		return nil, errCredentials
	}
	var c struct {
		Quote string `json:"quote"`
	}
	if json.Unmarshal([]byte(cred.Data.Data), &c) != nil || c.Quote == "" {
		return nil, errCredentials
	}
	balance, err := w.PayTopup(ctx, me.ID, r.FormID, c.Quote, invoiceStars)
	if err != nil {
		return nil, err
	}
	bal := &tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: balance}}
	up := &tg.Updates{Updates: []tg.UpdateClass{bal}, Chats: []tg.ChatClass{}, Date: now()}
	if up.Users, err = a.usersFor(ctx, me.ID, []int64{me.ID}); err != nil {
		return nil, err
	}
	a.echo(ctx, me, []tg.UpdateClass{bal}, []tg.UserClass{})
	return &tg.PaymentsPaymentResult{Updates: up}, nil
}
