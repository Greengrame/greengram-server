package admin

import (
	"context"
	"math"

	"github.com/Greengrame/greengram-server/internal/stars"
)

// The collectible market on the gifts page: per gift, whether it sells as
// variants, its base price and rarity multiplier, and what's on sale; the
// commission on sales between users is one number for the instance.

// withMarket adds a gift's market to its view. Only a gift that upgrades
// into a collection has one.
func (a *API) withMarket(ctx context.Context, view map[string]any, g stars.Gift) error {
	view["market"] = nil
	if g.UpgradeSet == 0 {
		return nil
	}
	m, err := a.Gifts.MarketOf(ctx, g.ID)
	if err != nil {
		return err
	}
	st, err := a.Gifts.MarketStats(ctx, g.ID)
	if err != nil {
		return err
	}
	var base any
	if m.Base > 0 {
		base = m.Base
	}
	view["market"] = map[string]any{"enabled": m.Enabled, "baseStars": base, "defaultBaseStars": g.Stars + g.UpgradeStars,
		"rarityMultiplier": float64(m.RarityX100) / 100, "listed": st.Listed, "offers": st.Offers, "minStars": st.MinStars, "maxStars": st.MaxStars}
	return nil
}

// marketFields reads "market": {enabled, baseStars (null: the gift's price
// plus its upgrade), rarityMultiplier (1 for a flat price)}. Missing keys
// keep what's saved.
func marketFields(q *req, old stars.Market) (stars.Market, bool, error) {
	if !q.has("market") {
		return old, false, nil
	}
	m, ok := q.body["market"].(map[string]any)
	if !ok {
		return old, true, fail(400, "market is an object.")
	}
	out := old
	if v, has := m["enabled"]; has {
		b, ok := v.(bool)
		if !ok {
			return out, true, fail(400, "market.enabled is true or false.")
		}
		out.Enabled = b
	}
	if v, has := m["baseStars"]; has {
		if v == nil {
			out.Base = 0
		} else {
			f, ok := v.(float64)
			if !ok || f != math.Trunc(f) || f < 1 || f > stars.ResaleMaxStars {
				return out, true, fail(400, "The base price is 1 to 1,000,000 Stars, or null for the gift's price plus its upgrade.")
			}
			out.Base = int64(f)
		}
	}
	if v, has := m["rarityMultiplier"]; has {
		f, ok := v.(float64)
		if !ok || f < 1 || f > 1000 {
			return out, true, fail(400, "The rarity multiplier is 1 (a flat price) to 1000.")
		}
		out.RarityX100 = int(math.Round(f * 100))
	}
	return out, true, nil
}

// saveMarket applies a request's market fields to gift id.
func (a *API) saveMarket(q *req, id int64) error {
	old, err := a.Gifts.MarketOf(q.ctx, id)
	if err != nil {
		return err
	}
	m, ok, err := marketFields(q, old)
	if err != nil || !ok {
		return err
	}
	if err := a.Gifts.SetMarket(q.ctx, id, m); err != nil {
		return err
	}
	return a.auditGift(q, "gift_market", id, m)
}

// giftMarket is the instance's commission, as a percent the panel shows.
func (a *API) giftMarket(q *req) (any, error) {
	n, err := a.Gifts.Commission(q.ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"commissionPermille": n, "maxCommissionPermille": stars.MaxCommission,
		"minResaleStars": stars.ResaleMinStars, "maxResaleStars": stars.ResaleMaxStars}, nil
}

func (a *API) editGiftMarket(q *req) (any, error) {
	n, ok := q.int("commissionPermille")
	if !ok || n < 0 || n > stars.MaxCommission {
		return nil, fail(400, "The commission is 0 to 900 permille (0 to 90%).")
	}
	if err := a.Gifts.SetCommission(q.ctx, int(n)); err != nil {
		return nil, err
	}
	if err := a.auditGift(q, "gift_market_commission", 0, map[string]int64{"commissionPermille": n}); err != nil {
		return nil, err
	}
	return a.giftMarket(q)
}
