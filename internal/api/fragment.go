package api

import (
	"context"
	"math"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/fragment"
)

// RegisterFragment answers the profile's "collectible username/number"
// sheet from the instance's Fragment.
func (a *API) RegisterFragment(f *fragment.Service) {
	handle(a, tlprofile.SemanticMethodFragmentGetCollectibleInfo, func(ctx context.Context, r *tg.FragmentGetCollectibleInfoRequest) (any, error) {
		if _, err := a.me(ctx); err != nil {
			return nil, err
		}
		var kind, value string
		switch c := r.Collectible.(type) {
		case *tg.InputCollectibleUsername:
			kind, value = "username", strings.ToLower(strings.TrimPrefix(c.Username, "@"))
		case *tg.InputCollectiblePhone:
			kind, value = "number", digitsOnly(c.Phone)
		default:
			return nil, tgerr.New(400, "COLLECTIBLE_INVALID")
		}
		if value == "" {
			return nil, tgerr.New(400, "COLLECTIBLE_INVALID")
		}
		info, ok, err := f.Info(ctx, kind, value)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, tgerr.New(400, "COLLECTIBLE_NOT_FOUND")
		}
		return collectibleInfo(info, a.in.BaseURL+"/fragment/"+kind+"/"+value), nil
	})
}

// Telegram quotes the price in USD cents next to the TON amount; with no
// fresh TON price the dollar figure is just 0.
func collectibleInfo(c fragment.Collectible, url string) *tg.FragmentCollectibleInfo {
	nano := int64(math.MaxInt64)
	if c.Nanograms.IsInt64() {
		nano = c.Nanograms.Int64()
	}
	return &tg.FragmentCollectibleInfo{
		PurchaseDate:   c.PurchasedAt,
		Currency:       "USD",
		Amount:         c.USDCents,
		CryptoCurrency: "TON",
		CryptoAmount:   nano,
		URL:            url,
	}
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
