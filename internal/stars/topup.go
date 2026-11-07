package stars

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/iamxvbaba/td/tgerr"
)

// KindTopup is Stars bought from an app's top-up screen. Nothing is sold
// for money: the form's page prices the Stars in a Wallet asset at the
// instance's starsPerUSD rate, and the wallet settles it (wallet.PayTopup).
const KindTopup = "stars_topup"

// TopupCurrency is what top-up options are priced in, at starsPerUSD.
const TopupCurrency = "USD"

// MaxTopup is the most Stars one top-up buys, the same cap as the web's.
const MaxTopup = 1000000000

type TopupOption struct {
	Stars, Amount int64 // Amount in cents
	Extended      bool  // behind "more options"
}

var topupTiers = []int64{50, 100, 250, 500, 1000, 2500, 5000, 10000, 25000, 50000, 100000}

var errTopupInvalid = tgerr.New(400, "INVOICE_INVALID")

// TopupPrice is n Stars in cents at today's rate, rounded up.
func (s *Service) TopupPrice(ctx context.Context, n int64) int64 {
	rate := max(int64(s.settings.Get(ctx).StarsPerUSD), 1)
	return (n*100 + rate - 1) / rate
}

func (s *Service) TopupOptions(ctx context.Context) []TopupOption {
	out := make([]TopupOption, 0, len(topupTiers))
	for _, n := range topupTiers {
		out = append(out, TopupOption{Stars: n, Amount: s.TopupPrice(ctx, n), Extended: n >= 5000})
	}
	return out
}

// TopupForm makes the form for n Stars. currency and amount are the price
// the app showed; a stale one is refused so the app reloads its options.
func (s *Service) TopupForm(ctx context.Context, user, n int64, currency string, amount int64) (Form, error) {
	if n < 1 || n > MaxTopup || currency != TopupCurrency || amount != s.TopupPrice(ctx, n) {
		return Form{}, errTopupInvalid
	}
	b := make([]byte, 24)
	rand.Read(b)
	return s.issue(ctx, Form{Kind: KindTopup, User: user, Recipient: user, Stars: n, PayToken: hex.EncodeToString(b)})
}
