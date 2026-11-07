package admin

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/stars"
)

func giftView(g stars.CatalogGift) map[string]any {
	view := map[string]any{"id": g.ID, "title": g.Title, "emoji": g.Emoji, "stars": g.Stars, "convertStars": g.ConvertStars,
		"premiumOnly": g.PremiumOnly, "limited": g.Limited, "enabled": g.Enabled, "sold": g.Sold, "stickerId": nil, "stock": nil, "stockTotal": nil}
	if g.StickerID != 0 {
		// a string: document ids don't fit a JS number
		view["stickerId"] = strconv.FormatInt(g.StickerID, 10)
	}
	if g.Limited {
		view["stock"], view["stockTotal"] = g.Stock, g.StockTotal
	}
	view["upgrade"] = nil
	if g.UpgradeSet != 0 {
		view["upgrade"] = map[string]any{"setId": g.UpgradeSet, "stars": g.UpgradeStars, "transferStars": g.TransferStars}
	}
	return view
}

// withSet adds the collection's numbers to a gift that upgrades into one.
func (a *API) withSet(ctx context.Context, view map[string]any, g stars.Gift, sets map[int64]stars.Set) {
	up, ok := view["upgrade"].(map[string]any)
	if !ok {
		return
	}
	if set, ok := sets[g.UpgradeSet]; ok {
		up["title"], up["supply"], up["issued"] = set.Title, set.Supply, set.Issued
	}
}

func (a *API) setsByID(ctx context.Context) (map[int64]stars.Set, error) {
	list, err := a.Gifts.Sets(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]stars.Set, len(list))
	for _, s := range list {
		out[s.ID] = s
	}
	return out, nil
}

// collectibles are the imported collections the gift form offers, with a
// few models to show and the gift's own art to borrow.
func (a *API) collectibles(q *req) (any, error) {
	list, err := a.Gifts.Sets(q.ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(list))
	for _, set := range list {
		models, _, _, err := a.Gifts.Attributes(q.ctx, set.ID)
		if err != nil {
			return nil, err
		}
		preview := []string{}
		// rarest first, so the preview shows what people hope for
		for _, m := range models[:min(4, len(models))] {
			preview = append(preview, strconv.FormatInt(m.Document, 10))
		}
		item := map[string]any{"id": set.ID, "name": set.Name, "title": set.Title, "slug": set.Slug, "supply": set.Supply, "issued": set.Issued,
			"models": set.Models, "patterns": set.Patterns, "backdrops": set.Backdrops, "giftId": nil, "originalId": nil, "preview": preview}
		if set.GiftID != 0 {
			item["giftId"] = set.GiftID
		}
		if set.Original != 0 {
			item["originalId"] = strconv.FormatInt(set.Original, 10)
		}
		out = append(out, item)
	}
	return map[string]any{"collections": out}, nil
}

// upgradeFields reads "upgrade": null makes the gift plain, an object
// {setId, stars, transferStars, supply} makes it upgradable. A supply of 0
// or null is unlimited. ok is false when the request doesn't touch it.
func upgradeFields(q *req, g stars.Gift) (stars.UpgradeSettings, bool, error) {
	if !q.has("upgrade") {
		return stars.UpgradeSettings{}, false, nil
	}
	raw := q.body["upgrade"]
	if raw == nil {
		return stars.UpgradeSettings{}, true, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return stars.UpgradeSettings{}, true, fail(400, "upgrade is an object, or null.")
	}
	num := func(key string) (int64, bool) {
		f, ok := m[key].(float64)
		return int64(f), ok && f == float64(int64(f))
	}
	u := stars.UpgradeSettings{TransferStars: g.TransferStars}
	var ok1, ok2, ok3 bool
	u.Set, ok1 = num("setId")
	u.Stars, ok2 = num("stars")
	supply, ok3 := num("supply")
	if v, has := m["supply"]; has && v == nil {
		supply, ok3 = 0, true
	}
	if !ok1 || u.Set < 1 {
		return u, true, fail(400, "Pick the collection the gift upgrades into.")
	}
	if !ok2 || u.Stars < 1 || u.Stars > stars.MaxBalance {
		return u, true, fail(400, "An upgrade costs at least 1 Star.")
	}
	if !ok3 || supply < 0 || supply > 1<<31-1 {
		return u, true, fail(400, "The collection's supply is a whole number, or 0 for unlimited.")
	}
	u.Supply = int(supply)
	if _, has := m["transferStars"]; has {
		n, ok := num("transferStars")
		if !ok || n < 0 || n > stars.MaxBalance {
			return u, true, fail(400, "The transfer fee is 0 or more Stars.")
		}
		u.TransferStars = n
	}
	return u, true, nil
}

func (a *API) gifts(q *req) (any, error) {
	list, err := a.Gifts.AllGifts(q.ctx)
	if err != nil {
		return nil, err
	}
	sets, err := a.setsByID(q.ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, len(list))
	for i, g := range list {
		out[i] = giftView(g)
		a.withSet(q.ctx, out[i], g.Gift, sets)
		if err := a.withMarket(q.ctx, out[i], g.Gift); err != nil {
			return nil, err
		}
	}
	return map[string]any{"gifts": out}, nil
}

// savedView is a gift as the panel lists it after a save.
func (a *API) savedView(ctx context.Context, id int64) (any, error) {
	g, err := a.Gifts.CatalogGift(ctx, id)
	if err != nil {
		return nil, err
	}
	sets, err := a.setsByID(ctx)
	if err != nil {
		return nil, err
	}
	view := giftView(g)
	a.withSet(ctx, view, g.Gift, sets)
	if err := a.withMarket(ctx, view, g.Gift); err != nil {
		return nil, err
	}
	return view, nil
}

// giftFields applies the request on top of g. Missing keys keep what's there,
// so an edit can send one field. A null stock makes the gift unlimited.
func (a *API) giftFields(ctx context.Context, q *req, g *stars.Gift) error {
	if q.has("title") {
		s, ok := q.str("title")
		s = strings.TrimSpace(s)
		if !ok || s == "" || utf8.RuneCountInString(s) > 64 {
			return fail(400, "A gift's title is 1 to 64 characters.")
		}
		g.Title = s
	}
	if q.has("emoji") {
		s, ok := q.str("emoji")
		s = strings.TrimSpace(s)
		if !ok || utf8.RuneCountInString(s) > 16 {
			return fail(400, "Choose one emoji for the gift.")
		}
		g.Emoji = s
	}
	if q.has("stars") {
		n, ok := q.int("stars")
		if !ok || n < 1 || n > stars.MaxBalance {
			return fail(400, "A gift costs at least 1 Star.")
		}
		g.Stars = n
	}
	if q.has("convertStars") {
		n, ok := q.int("convertStars")
		if !ok || n < 0 {
			return fail(400, "The conversion value can't be negative.")
		}
		g.ConvertStars = n
	}
	// converting for more than it cost is a way to print Stars
	if g.ConvertStars > g.Stars {
		return fail(400, "A gift can't convert to more Stars than it costs.")
	}
	if q.has("premiumOnly") {
		v, ok := q.boolean("premiumOnly")
		if !ok {
			return fail(400, "premiumOnly is true or false.")
		}
		g.PremiumOnly = v
	}
	if q.has("enabled") {
		v, ok := q.boolean("enabled")
		if !ok {
			return fail(400, "enabled is true or false.")
		}
		g.Enabled = v
	}
	if q.has("stock") {
		if q.body["stock"] == nil {
			g.Limited, g.Stock, g.StockTotal = false, 0, 0
		} else {
			n, ok := q.int("stock")
			if !ok || n < 0 || n > 1<<31-1 {
				return fail(400, "Stock is a whole number, or null for unlimited.")
			}
			// raising the stock raises the total; selling down doesn't lower it
			g.Stock, g.Limited = int(n), true
			if g.StockTotal < g.Stock {
				g.StockTotal = g.Stock
			}
		}
	}
	if q.has("stickerId") {
		if q.body["stickerId"] == nil {
			g.StickerID = 0
		} else {
			id, _ := q.body["stickerId"].(string)
			n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
			if err != nil || n <= 0 {
				return fail(400, "stickerId is a document id as a string, or null to pick by emoji.")
			}
			var found bool
			if err := a.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM documents WHERE id=$1)", n).Scan(&found); err != nil {
				return err
			}
			if !found {
				return fail(400, "No document has that id.")
			}
			g.StickerID = n
		}
	}
	// the emoji picks the art when there's no id, and starts the gift's message
	if g.Emoji == "" {
		if g.StickerID == 0 {
			return fail(400, "Choose one emoji for the gift, or a custom emoji.")
		}
		// a document outside any pack just has none
		if err := a.DB.QueryRow(ctx, "SELECT emoji FROM sticker_set_items WHERE document_id=$1 AND emoji<>'' AND char_length(emoji)<=16 ORDER BY set_id, position LIMIT 1", g.StickerID).Scan(&g.Emoji); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	return nil
}

func (a *API) addGift(q *req) (any, error) {
	g := stars.Gift{Enabled: true}
	if !q.has("title") || !q.has("stars") {
		return nil, fail(400, "A gift needs a title and a price.")
	}
	if err := a.giftFields(q.ctx, q, &g); err != nil {
		return nil, err
	}
	up, setUp, err := upgradeFields(q, g)
	if err != nil {
		return nil, err
	}
	saved, err := a.Gifts.AddGift(q.ctx, g)
	if err != nil {
		return nil, err
	}
	if setUp {
		if err := a.Gifts.SetGiftUpgrade(q.ctx, saved.ID, up); err != nil {
			// nobody bought it yet, so this deletes it
			a.Gifts.RemoveGift(q.ctx, saved.ID)
			return nil, err
		}
		saved.UpgradeSet, saved.UpgradeStars, saved.TransferStars = up.Set, up.Stars, up.TransferStars
	}
	if err := a.auditGift(q, "gift_added", saved.ID, saved); err != nil {
		return nil, err
	}
	if err := a.saveMarket(q, saved.ID); err != nil {
		return nil, err
	}
	return a.savedView(q.ctx, saved.ID)
}

func (a *API) editGift(q *req) (any, error) {
	id, err := q.pathID("id")
	if err != nil {
		return nil, err
	}
	g, err := a.Gifts.CatalogGift(q.ctx, id)
	if err != nil {
		return nil, err
	}
	if err := a.giftFields(q.ctx, q, &g.Gift); err != nil {
		return nil, err
	}
	up, setUp, err := upgradeFields(q, g.Gift)
	if err != nil {
		return nil, err
	}
	if err := a.Gifts.UpdateGift(q.ctx, g.Gift); err != nil {
		return nil, err
	}
	if setUp {
		if err := a.Gifts.SetGiftUpgrade(q.ctx, id, up); err != nil {
			return nil, err
		}
		g.UpgradeSet, g.UpgradeStars, g.TransferStars = up.Set, up.Stars, up.TransferStars
	}
	if err := a.auditGift(q, "gift_edited", id, g.Gift); err != nil {
		return nil, err
	}
	if err := a.saveMarket(q, id); err != nil {
		return nil, err
	}
	return a.savedView(q.ctx, id)
}

// removeGift deletes a gift nobody bought. A bought one has to stay for its
// receipts and messages, so it only leaves the catalog.
func (a *API) removeGift(q *req) (any, error) {
	id, err := q.pathID("id")
	if err != nil {
		return nil, err
	}
	deleted, err := a.Gifts.RemoveGift(q.ctx, id)
	if err != nil {
		return nil, err
	}
	if err := a.auditGift(q, "gift_removed", id, map[string]bool{"deleted": deleted}); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "deleted": deleted}, nil
}

func (a *API) auditGift(q *req, action string, id int64, details any) error {
	return a.tx(q.ctx, func(tx pgx.Tx) error { return audit(q.ctx, tx, q.me.ID, action, id, details) })
}
