package wallet

import (
	"context"
	"encoding/json"
	"math/big"
	"time"

	"github.com/Greengrame/greengram-server/internal/account"
)

// walletTiers is Telegram's wallet table: USD exchanged in a calendar month
// unlocks a tier until the end of the next one (help.walt.io/article/894-tiers,
// updated 2026-11-03). Its fees (0.9/0.6/0.3/0.09%) aren't copied: swaps here have
// no fee, so every tier says 0%.
var walletTiers = []struct {
	Name, Volume string
}{{"base", "0"}, {"bronze", "300"}, {"silver", "2000"}, {"gold", "30000"}}

// tierList is the app's list of tiers; version 2 is the one with these names.
func tierList() json.RawMessage {
	list := make([]map[string]any, 0, len(walletTiers))
	for _, t := range walletTiers {
		list = append(list, map[string]any{"name": t.Name, "is_active": true, "trading_fees_percent": "0", "monthly_turnover": t.Volume})
	}
	out, _ := json.Marshal(map[string]any{"subscriptions": list, "version": 2})
	return out
}

// tierFor is the highest tier a month's volume reaches, as an index.
func tierFor(volume *big.Rat) int {
	at := 0
	for i, t := range walletTiers {
		if volume.Cmp(bigOfRat(t.Volume)) >= 0 {
			at = i
		}
	}
	return at
}

// tierAt picks from this month's and last month's volume: last month's tier
// holds to the end of this month, this month's to the end of the next.
func tierAt(current, previous *big.Rat, month time.Time) (string, any) {
	now, last := tierFor(current), tierFor(previous)
	switch {
	case now == 0 && last == 0:
		return walletTiers[0].Name, nil
	case now >= last:
		return walletTiers[now].Name, month.AddDate(0, 2, 0).Add(-time.Second).Format("2006-01-02T15:04:05Z")
	}
	return walletTiers[last].Name, month.AddDate(0, 1, 0).Add(-time.Second).Format("2006-01-02T15:04:05Z")
}

func (s *Service) userTier(ctx context.Context, u account.User, m Market) (any, error) {
	now := s.now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	last := month.AddDate(0, -1, 0)
	current, err := s.swapVolumeUSD(ctx, u.ID, month.Unix(), m)
	if err != nil {
		return nil, err
	}
	both, err := s.swapVolumeUSD(ctx, u.ID, last.Unix(), m)
	if err != nil {
		return nil, err
	}
	tier, end := tierAt(current, new(big.Rat).Sub(both, current), month)
	return map[string]any{"status": "active", "version": 2, "subscription": map[string]any{"name": tier, "is_active": true}, "has_subscription": true,
		"trading_fees_percent": "0", "swap_volume_month": current.FloatString(2), "end_dt": end}, nil
}
