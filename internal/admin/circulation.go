package admin

import (
	"context"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/stars"
	"github.com/Greengrame/greengram-server/internal/wallet"
)

// The Gifts & Collectibles page: what exists and who holds it. Read-only,
// paged on the server; every list reads one page of rows through an index
// (gift_receipts_gift, unique_gifts' (set_id, num), fragment_items_title),
// so a page costs the same however big the tables get.

func (a *API) registerCirculation(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/circulation/gifts", a.handle(a.circulationGifts))
	mux.HandleFunc("GET /api/admin/circulation/gifts/{id}/holders", a.handle(a.giftHolders))
	mux.HandleFunc("GET /api/admin/circulation/collections", a.handle(a.circulationCollections))
	mux.HandleFunc("GET /api/admin/circulation/collections/{id}/items", a.handle(a.collectionItems))
	mux.HandleFunc("GET /api/admin/circulation/fragment", a.handle(a.circulationFragment))
	mux.HandleFunc("GET /api/admin/circulation/users", a.handle(a.holdingUsers))
	mux.HandleFunc("GET /api/admin/circulation/users/{id}", a.handle(a.holdings))
}

// page reads ?page= (from 1) and ?size= (up to 100, default 25).
func (q *req) page() (limit, offset, page int) {
	v := q.r.URL.Query()
	page, _ = strconv.Atoi(v.Get("page"))
	limit, _ = strconv.Atoi(v.Get("size"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 25
	}
	return limit, (page - 1) * limit, page
}

func pageOf(items any, total int64, page, size int) map[string]any {
	return map[string]any{"items": items, "total": total, "page": page, "pageSize": size}
}

// holder is a gift's or a name's owner as the page shows it.
type holder struct {
	Kind     string `json:"kind"` // user, channel
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username,omitempty"`
	State    string `json:"state,omitempty"`
	System   bool   `json:"isSystem,omitempty"`
}

// holders names the users and channels in a page in two queries.
func (a *API) holders(ctx context.Context, users, chans []int64) (map[int64]holder, map[int64]holder, error) {
	us, cs := map[int64]holder{}, map[int64]holder{}
	if len(users) > 0 {
		rows, err := a.DB.Query(ctx, "SELECT id, name, username, account_state, is_system FROM users WHERE id = ANY($1)", users)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			h := holder{Kind: "user"}
			if err := rows.Scan(&h.ID, &h.Name, &h.Username, &h.State, &h.System); err != nil {
				rows.Close()
				return nil, nil, err
			}
			us[h.ID] = h
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	}
	if len(chans) > 0 {
		rows, err := a.DB.Query(ctx, "SELECT id, title, coalesce(username, ''), CASE WHEN deleted THEN 'deleted' ELSE 'active' END FROM channels WHERE id = ANY($1)", chans)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			h := holder{Kind: "channel"}
			if err := rows.Scan(&h.ID, &h.Name, &h.Username, &h.State); err != nil {
				rows.Close()
				return nil, nil, err
			}
			cs[h.ID] = h
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	}
	return us, cs, nil
}

func ownerOf(user, channel *int64, us, cs map[int64]holder) any {
	switch {
	case user != nil:
		if h, ok := us[*user]; ok {
			return h
		}
		return holder{Kind: "user", ID: *user}
	case channel != nil:
		if h, ok := cs[*channel]; ok {
			return h
		}
		return holder{Kind: "channel", ID: *channel}
	}
	return nil
}

func ids(m map[int64]bool) []int64 {
	out := make([]int64, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	return out
}

// circulationGifts is one page of the catalog with what became of each
// gift: sold (every receipt), converted to Stars, upgraded to a
// collectible, held by deleted accounts, and in circulation (the rest:
// held by a person, a channel or a system account).
func (a *API) circulationGifts(q *req) (any, error) {
	limit, offset, page := q.page()
	var total int64
	if err := a.DB.QueryRow(q.ctx, "SELECT count(*) FROM gift_catalog").Scan(&total); err != nil {
		return nil, err
	}
	rows, err := a.DB.Query(q.ctx, `SELECT g.id, g.title, g.emoji, g.stars, g.convert_stars, g.stock, g.stock_total, g.enabled, g.upgrade_set_id,
			s.sold, s.converted, s.upgraded, s.holders,
			(SELECT count(*) FROM users u JOIN gift_receipts r ON r.owner_id = u.id AND r.gift_id = g.id AND NOT r.converted WHERE u.account_state = 'deleted')
		FROM (SELECT * FROM gift_catalog ORDER BY id LIMIT $1 OFFSET $2) g,
		LATERAL (SELECT count(*) AS sold, count(*) FILTER (WHERE r.converted) AS converted, count(*) FILTER (WHERE r.unique_id IS NOT NULL) AS upgraded,
			count(DISTINCT coalesce(r.owner_id, -r.owner_channel_id)) FILTER (WHERE NOT r.converted) AS holders
			FROM gift_receipts r WHERE r.gift_id = g.id) s
		ORDER BY g.id`, limit, offset)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) {
		var id, price, convert, sold, converted, upgraded, holders, deleted int64
		var title, emoji string
		var stock, stockTotal *int64
		var set *int64
		var enabled bool
		err := r.Scan(&id, &title, &emoji, &price, &convert, &stock, &stockTotal, &enabled, &set, &sold, &converted, &upgraded, &holders, &deleted)
		return map[string]any{"id": id, "title": title, "emoji": emoji, "stars": price, "convertStars": convert, "stock": stock, "stockTotal": stockTotal,
			"enabled": enabled, "upgradeSetId": set, "sold": sold, "converted": converted, "upgraded": upgraded, "heldByDeleted": deleted,
			"inCirculation": sold - converted - deleted, "holders": holders}, err
	})
	if err != nil {
		return nil, err
	}
	return pageOf(items, total, page, limit), nil
}

// giftHolders is who holds a catalog gift, most first.
func (a *API) giftHolders(q *req) (any, error) {
	id, err := strconv.ParseInt(q.r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return nil, fail(404, "Gift not found.")
	}
	limit, offset, page := q.page()
	var total int64
	if err := a.DB.QueryRow(q.ctx, `SELECT count(DISTINCT coalesce(owner_id, -owner_channel_id)) FROM gift_receipts WHERE gift_id=$1 AND NOT converted`, id).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := a.DB.Query(q.ctx, `SELECT owner_id, owner_channel_id, count(*), count(*) FILTER (WHERE unique_id IS NOT NULL) FROM gift_receipts
		WHERE gift_id=$1 AND NOT converted GROUP BY owner_id, owner_channel_id ORDER BY 3 DESC, owner_id, owner_channel_id LIMIT $2 OFFSET $3`, id, limit, offset)
	if err != nil {
		return nil, err
	}
	type line struct {
		user, channel *int64
		n, upgraded   int64
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (line, error) {
		var l line
		return l, r.Scan(&l.user, &l.channel, &l.n, &l.upgraded)
	})
	if err != nil {
		return nil, err
	}
	users, chans := map[int64]bool{}, map[int64]bool{}
	for _, l := range list {
		if l.user != nil {
			users[*l.user] = true
		} else if l.channel != nil {
			chans[*l.channel] = true
		}
	}
	us, cs, err := a.holders(q.ctx, ids(users), ids(chans))
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(list))
	for _, l := range list {
		items = append(items, map[string]any{"owner": ownerOf(l.user, l.channel, us, cs), "count": l.n, "upgraded": l.upgraded})
	}
	return pageOf(items, total, page, limit), nil
}

// circulationCollections is one page of collections: issued of supply (0
// is unlimited), how many hold one, how many are up for resale.
func (a *API) circulationCollections(q *req) (any, error) {
	limit, offset, page := q.page()
	var total int64
	if err := a.DB.QueryRow(q.ctx, "SELECT count(*) FROM collectible_sets").Scan(&total); err != nil {
		return nil, err
	}
	rows, err := a.DB.Query(q.ctx, `SELECT s.id, s.title, s.slug, s.supply, s.issued, (SELECT g.id FROM gift_catalog g WHERE g.upgrade_set_id = s.id),
			(SELECT count(DISTINCT coalesce(r.owner_id, -r.owner_channel_id)) FROM unique_gifts u JOIN gift_receipts r ON r.unique_id = u.id
				WHERE u.set_id = s.id AND NOT r.converted),
			(SELECT count(*) FROM unique_gifts u WHERE u.set_id = s.id AND u.resale_stars IS NOT NULL)
		FROM collectible_sets s ORDER BY s.id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) {
		var id, supply, issued, owners, listed int64
		var gift *int64
		var title, slug string
		err := r.Scan(&id, &title, &slug, &supply, &issued, &gift, &owners, &listed)
		return map[string]any{"id": id, "title": title, "slug": slug, "supply": supply, "issued": issued, "giftId": gift, "owners": owners, "listed": listed}, err
	})
	if err != nil {
		return nil, err
	}
	return pageOf(items, total, page, limit), nil
}

// collectionItems is one page of a collection's pieces by number, with
// who holds each and its resale price. ?listed=1 keeps the ones for sale,
// ?num= finds one.
func (a *API) collectionItems(q *req) (any, error) {
	set, err := strconv.ParseInt(q.r.PathValue("id"), 10, 64)
	if err != nil || set < 1 {
		return nil, fail(404, "Collection not found.")
	}
	limit, offset, page := q.page()
	v := q.r.URL.Query()
	where, args := "u.set_id=$1", []any{set}
	if v.Get("listed") == "1" {
		where += " AND u.resale_stars IS NOT NULL"
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(v.Get("num")), "#")); err == nil && n > 0 {
		args = append(args, n)
		where += " AND u.num=$2"
	}
	var total int64
	if err := a.DB.QueryRow(q.ctx, "SELECT count(*) FROM unique_gifts u WHERE "+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	n := len(args)
	rows, err := a.DB.Query(q.ctx, `SELECT u.id, u.num, u.slug, u.model, u.pattern, u.backdrop, u.resale_stars, u.resale_since, r.owner_id, r.owner_channel_id
		FROM unique_gifts u LEFT JOIN gift_receipts r ON r.unique_id = u.id WHERE `+where+` ORDER BY u.num LIMIT $`+strconv.Itoa(n+1)+` OFFSET $`+strconv.Itoa(n+2),
		append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	type piece struct {
		id                     int64
		num                    int
		slug, model, pat, back string
		price                  *int64
		since                  int
		user, channel          *int64
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (piece, error) {
		var p piece
		return p, r.Scan(&p.id, &p.num, &p.slug, &p.model, &p.pat, &p.back, &p.price, &p.since, &p.user, &p.channel)
	})
	if err != nil {
		return nil, err
	}
	users, chans := map[int64]bool{}, map[int64]bool{}
	for _, p := range list {
		if p.user != nil {
			users[*p.user] = true
		} else if p.channel != nil {
			chans[*p.channel] = true
		}
	}
	us, cs, err := a.holders(q.ctx, ids(users), ids(chans))
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(list))
	for _, p := range list {
		items = append(items, map[string]any{"id": strconv.FormatInt(p.id, 10), "num": p.num, "slug": p.slug, "model": p.model, "pattern": p.pat,
			"backdrop": p.back, "listed": p.price != nil, "price": p.price, "listedSince": p.since, "owner": ownerOf(p.user, p.channel, us, cs)})
	}
	return pageOf(items, total, page, limit), nil
}

func gram(units *string) any {
	if units == nil {
		return nil
	}
	n, ok := new(big.Int).SetString(*units, 10)
	if !ok {
		return nil
	}
	return wallet.FormatAmount(n, "TON")
}

// circulationFragment is every collectible username and number: who owns
// it (nobody: the instance's own reserve), the channel using a username,
// the asking price when listed, and the last sale. ?kind=, ?listed=1, ?q=.
func (a *API) circulationFragment(q *req) (any, error) {
	limit, offset, page := q.page()
	v := q.r.URL.Query()
	where, args := "f.kind IN ('username', 'number')", []any{}
	if k := v.Get("kind"); k == "username" || k == "number" {
		args = append(args, k)
		where += " AND f.kind=$" + strconv.Itoa(len(args))
	}
	if v.Get("listed") == "1" {
		where += " AND f.listed"
	}
	if s := strings.ToLower(strings.TrimSpace(v.Get("q"))); s != "" {
		s = strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`, " ", "").Replace(strings.TrimLeft(s, "@+"))
		args = append(args, "%"+s+"%")
		where += " AND lower(f.title) LIKE $" + strconv.Itoa(len(args))
	}
	var total int64
	if err := a.DB.QueryRow(q.ctx, "SELECT count(*) FROM fragment_items f WHERE "+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	n := len(args)
	rows, err := a.DB.Query(q.ctx, `SELECT f.id, f.kind, f.title, f.display_format, f.units::text, f.owner_id, f.listed, f.purchased_at, f.last_sale_units::text, c.id, c.title
		FROM fragment_items f LEFT JOIN channels c ON f.kind = 'username' AND lower(c.username) = lower(substr(f.title, 2)) AND NOT c.deleted
		WHERE `+where+` ORDER BY f.kind DESC, lower(f.title) LIMIT $`+strconv.Itoa(n+1)+` OFFSET $`+strconv.Itoa(n+2), append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	type entry struct {
		id, kind, title, format, units string
		owner                          *int64
		listed                         bool
		soldAt                         *int64
		lastSale                       *string
		channel                        *int64
		channelTitle                   *string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (entry, error) {
		var e entry
		return e, r.Scan(&e.id, &e.kind, &e.title, &e.format, &e.units, &e.owner, &e.listed, &e.soldAt, &e.lastSale, &e.channel, &e.channelTitle)
	})
	if err != nil {
		return nil, err
	}
	users := map[int64]bool{}
	for _, e := range list {
		if e.owner != nil {
			users[*e.owner] = true
		}
	}
	us, _, err := a.holders(q.ctx, ids(users), nil)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(list))
	for _, e := range list {
		item := map[string]any{"id": e.id, "kind": e.kind, "title": e.title, "displayTitle": e.format, "listed": e.listed,
			"price": nil, "lastSale": gram(e.lastSale), "lastSaleAt": e.soldAt, "owner": ownerOf(e.owner, nil, us, nil), "channel": nil}
		if e.listed {
			item["price"] = gram(&e.units)
		}
		if e.channel != nil {
			item["channel"] = map[string]any{"id": *e.channel, "title": *e.channelTitle}
		}
		items = append(items, item)
	}
	return pageOf(items, total, page, limit), nil
}

// holdingUsers finds accounts for the per-user lookup: people, deleted
// accounts and the system accounts (Deleted Accounts holds what deleted
// people left behind).
func (a *API) holdingUsers(q *req) (any, error) {
	s := strings.ToLower(strings.TrimSpace(q.r.URL.Query().Get("q")))
	s = strings.TrimPrefix(s, "@")
	id, _ := strconv.ParseInt(strings.TrimPrefix(s, "#"), 10, 64)
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s) + "%"
	rows, err := a.DB.Query(q.ctx, `SELECT id, name, username, account_state, is_system FROM users
		WHERE NOT is_bot AND (id=$1 OR $2 <> '' AND (lower(username) LIKE $3 OR lower(name) LIKE $3)) ORDER BY id=$1 DESC, is_system, id LIMIT 20`, id, s, like)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (holder, error) {
		h := holder{Kind: "user"}
		return h, r.Scan(&h.ID, &h.Name, &h.Username, &h.State, &h.System)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items}, nil
}

// holdings is everything one account holds.
func (a *API) holdings(q *req) (any, error) {
	id, err := q.pathID("id")
	if err != nil {
		return nil, err
	}
	ctx := q.ctx
	us, _, err := a.holders(ctx, []int64{id}, nil)
	if err != nil {
		return nil, err
	}
	who, found := us[id]
	if !found {
		return nil, fail(404, "Account not found.")
	}
	var deletedAt *int64
	var source *string
	if err := a.DB.QueryRow(ctx, "SELECT extract(epoch FROM deleted_at)::bigint, deletion_source FROM users WHERE id=$1", id).Scan(&deletedAt, &source); err != nil {
		return nil, err
	}
	balance, err := stars.Balance(ctx, a.DB, id)
	if err != nil {
		return nil, err
	}
	coins, err := wallet.Holdings(ctx, a.DB, id)
	if err != nil {
		return nil, err
	}
	rows, err := a.DB.Query(ctx, `SELECT g.id, g.title, g.emoji, count(*) FILTER (WHERE r.unique_id IS NULL), count(*) FILTER (WHERE r.unique_id IS NOT NULL)
		FROM gift_receipts r JOIN gift_catalog g ON g.id = r.gift_id WHERE r.owner_id=$1 AND NOT r.converted GROUP BY g.id ORDER BY g.id`, id)
	if err != nil {
		return nil, err
	}
	gifts, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) {
		var gift, plain, upgraded int64
		var title, emoji string
		err := r.Scan(&gift, &title, &emoji, &plain, &upgraded)
		return map[string]any{"giftId": gift, "title": title, "emoji": emoji, "plain": plain, "upgraded": upgraded}, err
	})
	if err != nil {
		return nil, err
	}
	var collectibleCount int64
	if err := a.DB.QueryRow(ctx, "SELECT count(*) FROM gift_receipts WHERE owner_id=$1 AND unique_id IS NOT NULL AND NOT converted", id).Scan(&collectibleCount); err != nil {
		return nil, err
	}
	rows, err = a.DB.Query(ctx, `SELECT u.id, s.id, s.title, u.num, u.slug, u.model, u.resale_stars FROM gift_receipts r JOIN unique_gifts u ON u.id = r.unique_id
		JOIN collectible_sets s ON s.id = u.set_id WHERE r.owner_id=$1 AND NOT r.converted ORDER BY s.title, u.num LIMIT 200`, id)
	if err != nil {
		return nil, err
	}
	collectibles, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) {
		var uid, set int64
		var num int
		var title, slug, model string
		var price *int64
		err := r.Scan(&uid, &set, &title, &num, &slug, &model, &price)
		return map[string]any{"id": strconv.FormatInt(uid, 10), "collectionId": set, "collection": title, "num": num, "slug": slug, "model": model,
			"listed": price != nil, "price": price}, err
	})
	if err != nil {
		return nil, err
	}
	frag, err := a.fragmentHeld(ctx, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"user": who, "deletedAt": deletedAt, "deletionSource": source, "stars": balance, "wallet": coins, "gifts": gifts,
		"collectibles": collectibles, "collectibleCount": collectibleCount, "fragment": frag}, nil
}

// fragmentHeld is the usernames and numbers an account owns, for the
// lookup and the member dialog.
func (a *API) fragmentHeld(ctx context.Context, user int64) ([]map[string]any, error) {
	rows, err := a.DB.Query(ctx, `SELECT f.id, f.kind, f.title, f.display_format, f.listed, f.units::text, f.purchased_at, f.last_sale_units::text
		FROM fragment_items f WHERE f.owner_id=$1 AND f.kind IN ('username', 'number') ORDER BY f.kind DESC, lower(f.title)`, user)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) {
		var id, kind, title, format, units string
		var listed bool
		var soldAt *int64
		var last *string
		err := r.Scan(&id, &kind, &title, &format, &listed, &units, &soldAt, &last)
		item := map[string]any{"id": id, "kind": kind, "title": title, "displayTitle": format, "listed": listed, "price": nil, "lastSale": gram(last), "lastSaleAt": soldAt}
		if listed {
			item["price"] = gram(&units)
		}
		return item, err
	})
}
