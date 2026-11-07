// Package fragment is the instance's Fragment: collectible usernames and
// anonymous numbers, listed by admins or their owners and bought with TON
// from the wallet. Rules, texts and JSON are greengram-web's fragment.js;
// the Fragment page calls /api/fragment/* with these shapes.
package fragment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/secret"
	"github.com/Greengrame/greengram-server/internal/wallet"
)

type Service struct {
	db  *pgxpool.Pool
	key *secret.Key
	// tonPrice is the fresh USD price of TON, if the market has one
	tonPrice func() (string, bool)
	// LastPrice is the newest known USD price of TON however old, for display
	LastPrice func() (string, bool)
	now      func() time.Time

	// optional; the apps hold these in memory, so they need telling.
	// UsernamesChanged: the user's collectible usernames moved.
	UsernamesChanged func(user int64)
	// ChannelReleased: a sale or a listing changed the channel's public username.
	ChannelReleased func(channel int64)
}

// New wires the TON price for the "~ $" amounts; nil shows none.
func New(db *pgxpool.Pool, key *secret.Key, tonPrice func() (usd string, ok bool)) *Service {
	return &Service{db: db, key: key, tonPrice: tonPrice, now: time.Now}
}

func (s *Service) unix() int { return int(s.now().Unix()) }

func (s *Service) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.db, fn)
}

func fail(status int, msg string) error { return &account.Error{Status: status, Message: msg} }

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

type item struct {
	ID            string
	Kind          string
	Title         string
	Units         *big.Int
	Owner         int64
	Listed        bool
	PurchasedAt   int
	CountryCode   string
	DisplayFormat string
}

const itemCols = "id, kind, title, units::text, coalesce(owner_id, 0), listed, coalesce(purchased_at, 0), country_code, display_format"

func scanItem(row pgx.Row) (item, error) {
	var it item
	var units string
	err := row.Scan(&it.ID, &it.Kind, &it.Title, &units, &it.Owner, &it.Listed, &it.PurchasedAt, &it.CountryCode, &it.DisplayFormat)
	if err == nil {
		it.Units, _ = new(big.Int).SetString(units, 10)
	}
	return it, err
}

func (s *Service) items(ctx context.Context, q querier, sql string, args ...any) ([]item, error) {
	rows, err := q.Query(ctx, "SELECT "+itemCols+" FROM fragment_items "+sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (item, error) { return scanItem(r) })
}

// itemBy returns ok=false for no such row.
func itemBy(ctx context.Context, q querier, where string, args ...any) (item, bool, error) {
	it, err := scanItem(q.QueryRow(ctx, "SELECT "+itemCols+" FROM fragment_items WHERE "+where, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return it, false, nil
	}
	return it, err == nil, err
}

func (it item) displayTitle() string {
	if it.DisplayFormat != "" {
		return it.DisplayFormat
	}
	if it.CountryCode != "" && len(it.Title) >= len(it.CountryCode)+1 {
		return "+" + it.CountryCode + " " + it.Title[len(it.CountryCode)+1:]
	}
	return it.Title
}

func ton(units *big.Int) string { return wallet.FormatAmount(units, "TON") }

// person is the users row the rules look at; phone is decrypted.
type person struct {
	ID       int64
	Username string
	Phone    string
	Lookup   string
	Password string
	Active   bool // not disabled, not a system account
}

func (s *Service) person(ctx context.Context, q querier, id int64, lock bool) (person, bool, error) {
	var p person
	var enc string
	var disabled, system bool
	sql := "SELECT id, username, coalesce(phone, ''), coalesce(phone_lookup, ''), password, disabled, is_system FROM users WHERE id=$1"
	if lock {
		sql += " FOR UPDATE"
	}
	err := q.QueryRow(ctx, sql, id).Scan(&p.ID, &p.Username, &enc, &p.Lookup, &p.Password, &disabled, &system)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	p.Active = !disabled && !system
	if enc != "" {
		if p.Phone, err = s.key.DecryptPhone(enc, fmt.Sprintf("user:%d", id)); err != nil {
			return p, false, err
		}
	}
	return p, true, nil
}

func (s *Service) lookup(phone string) string { return s.key.PhoneLookup(phone) }

func (s *Service) usd(units *big.Int) any {
	if s.tonPrice == nil {
		return nil
	}
	price, ok := s.tonPrice()
	if !ok {
		return nil
	}
	v, ok := mulDecimal(ton(units), price)
	if !ok {
		return nil
	}
	return v
}

// view is how fragmentCatalog/fragmentItem show a listing to viewer.
func (s *Service) view(it item, viewer person) map[string]any {
	var owner any
	if it.Owner != 0 {
		owner = it.Owner
	}
	return map[string]any{
		"id": it.ID, "kind": it.Kind, "title": it.Title, "countryCode": it.CountryCode,
		"displayTitle": it.displayTitle(),
		"isAssigned":   it.Kind == "number" && viewer.Lookup != "" && viewer.Lookup == s.lookup(it.Title),
		"amount":       ton(it.Units), "usdAmount": s.usd(it.Units),
		"listed": it.Listed, "ownerId": owner, "isOwned": it.Owner == viewer.ID,
	}
}

// Inventory is the admin panel's Fragment page: every username and number,
// listed or sold.
func (s *Service) Inventory(ctx context.Context) ([]map[string]any, error) {
	list, err := s.items(ctx, s.db, "WHERE kind IN ('username', 'number') ORDER BY kind, id")
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(list))
	for _, it := range list {
		out = append(out, s.view(it, person{ID: -1})) // nobody's viewpoint
	}
	return out, nil
}

// Catalog is the Fragment page's data. administrative is the admin panel's
// inventory: every item, no owned assets; the caller checks the role.
func (s *Service) Catalog(ctx context.Context, userID int64, administrative bool) (map[string]any, error) {
	me, ok, err := s.person(ctx, s.db, userID, false)
	if err != nil || !ok {
		return nil, notFound(err)
	}
	bal, err := tonBalance(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	list, err := s.items(ctx, s.db, "WHERE kind IN ('username', 'number') AND ($1 OR listed OR owner_id=$2) ORDER BY kind, id", administrative, userID)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(list))
	for _, it := range list {
		items = append(items, s.view(it, me))
	}
	owned := []map[string]any{}
	if !administrative {
		assets, err := s.owned(ctx, s.db, me)
		if err != nil {
			return nil, err
		}
		for _, a := range assets {
			owned = append(owned, a.json())
		}
	}
	return map[string]any{"balance": ton(bal), "ownedAssets": owned, "items": items}, nil
}

// Item is one collectible for its page, by "username"/"number" and the
// name or digits.
func (s *Service) Item(ctx context.Context, userID int64, kind, value string) (map[string]any, error) {
	me, ok, err := s.person(ctx, s.db, userID, false)
	if err != nil || !ok {
		return nil, notFound(err)
	}
	title := prefix(kind) + value
	it, found, err := itemBy(ctx, s.db, "kind=$1 AND lower(title)=lower($2)", kind, title)
	if err != nil {
		return nil, err
	}
	if !found && (kind == "username" || kind == "number") {
		assets, err := s.owned(ctx, s.db, me)
		if err != nil {
			return nil, err
		}
		for _, a := range assets {
			if a.Kind == kind && equalFold(a.Title, title) {
				return a.json(), nil
			}
		}
	}
	if !found || (kind != "username" && kind != "number") {
		return nil, fail(404, "This collectible is not listed on this instance.")
	}
	return s.view(it, me), nil
}

// Public is one collectible as a signed-out visitor sees its page, like
// fragment.com: price, whether it sells, when it was bought and, for a
// username, who holds it. Numbers keep their owner to themselves.
func (s *Service) Public(ctx context.Context, kind, value string) (map[string]any, error) {
	if kind != "username" && kind != "number" {
		return nil, fail(404, "This collectible is not listed on this instance.")
	}
	it, found, err := itemBy(ctx, s.db, "kind=$1 AND lower(title)=lower($2)", kind, prefix(kind)+value)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fail(404, "This collectible is not listed on this instance.")
	}
	v := s.view(it, person{ID: -1})
	v["purchasedAt"] = it.PurchasedAt
	delete(v, "ownerId")
	if kind == "username" && it.Owner != 0 {
		var name string
		var disabled, system bool
		err := s.db.QueryRow(ctx, "SELECT name, disabled, is_system FROM users WHERE id=$1", it.Owner).Scan(&name, &disabled, &system)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err == nil && !disabled && !system {
			v["ownerName"] = name
		}
	}
	return v, nil
}

// Collectible is what fragment.getCollectibleInfo reports.
type Collectible struct {
	PurchasedAt int
	Nanograms   *big.Int
	USDCents    int64 // 0 without a fresh price
	HasUSD      bool
}

// Info finds a collectible username (without @) or number (digits).
func (s *Service) Info(ctx context.Context, kind, value string) (Collectible, bool, error) {
	it, found, err := itemBy(ctx, s.db, "kind=$1 AND lower(title)=lower($2)", kind, prefix(kind)+value)
	if err != nil || !found {
		return Collectible{}, false, err
	}
	c := Collectible{PurchasedAt: it.PurchasedAt, Nanograms: it.Units}
	if s.tonPrice != nil {
		if price, ok := s.tonPrice(); ok {
			c.USDCents, c.HasUSD = usdCents(it.Units, price)
		}
	}
	// only a label, so an older price beats showing $0.00
	if !c.HasUSD && s.LastPrice != nil {
		if price, ok := s.LastPrice(); ok {
			c.USDCents, c.HasUSD = usdCents(it.Units, price)
		}
	}
	return c, true, nil
}

func prefix(kind string) string {
	if kind == "username" {
		return "@"
	}
	return "+"
}

func notFound(err error) error {
	if err != nil {
		return err
	}
	return fail(401, "Sign in to continue.")
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
