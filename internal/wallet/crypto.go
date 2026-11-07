package wallet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
)

func nullable(p *string) any {
	if p == nil || *p == "" {
		return nil
	}
	return *p
}

// Account is cryptoAccount(): one asset as the wallet's main screen shows
// it. Only internal sends and swaps are on; nothing reaches a chain.
func (s *Service) Account(ctx context.Context, userID int64, code string, m Market) (map[string]any, error) {
	a, ok := Lookup(code)
	if !ok {
		return nil, fail(404, "Asset not supported.")
	}
	bal, err := s.Balance(ctx, userID, a.Code)
	if err != nil {
		return nil, err
	}
	fiat, rate := s.fiatFor(ctx, userID)
	return s.account(a, code, bal, m, fiat, rate), nil
}

// account prices one asset. usdValue stays in USD (the client adds those up
// for itself); everything else is in the user's currency at rate.
func (s *Service) account(a Asset, code string, bal *big.Int, m Market, fiat, rate string) map[string]any {
	q, fresh := s.fresh(m, a.Code)
	var usdValue, valuation any = "0", "0"
	if fresh {
		if v, ok := MultiplyMarketAmount(format(bal, a.Code), q.Price); ok {
			usdValue, valuation = v, convertFiat(v, rate)
		} else {
			usdValue, valuation = nil, nil
		}
	} else if bal.Sign() != 0 {
		usdValue, valuation = nil, nil
	}
	actions := map[string]any{}
	for _, name := range []string{"withdraw_outer", "withdraw_inner", "deposit", "purchase", "p2p", "exchange"} {
		actions[name] = map[string]any{"is_enabled": name == "withdraw_inner" || name == "exchange", "is_popular": false, "scheduled_at": nil}
	}
	var updated, price any
	if fresh {
		updated, price = q.UpdatedAt, convertFiat(q.Price, rate)
	}
	// not always on main: the home screen then only lists what you hold
	// (and suggests a few when you hold nothing)
	return map[string]any{
		"currency": code, "name": a.Name, "available_balance": format(bal, a.Code),
		"available_balance_usd_amount": usdValue, "available_balance_fiat_amount": valuation, "available_balance_fiat_currency": fiat,
		"account_type": "main", "addresses": []any{}, "actions": actions, "monetary_value": false,
		"market_source": m.Source, "market_updated_at": updated, "market_stale": !fresh, "rate": price,
		"absolute_rate_change": convertFiatPtr(q.PriceChange24h, rate), "percent_rate_change": nullable(q.PriceChange24hPercent),
		"is_created": true, "is_always_on_main": false, "category": clientCategory(a), "market_cap": convertFiatPtr(q.MarketCap, rate), "description": a.Name,
	}
}

// Accounts is every asset, in list order, on one balance query: the list is
// long now and the home screen asks for it a lot.
func (s *Service) Accounts(ctx context.Context, userID int64, m Market) ([]map[string]any, error) {
	rows, err := s.db.Query(ctx, "SELECT asset, units::text FROM crypto_accounts WHERE user_id=$1", userID)
	if err != nil {
		return nil, err
	}
	held := map[string]*big.Int{}
	var asset, units string
	if _, err := pgx.ForEachRow(rows, []any{&asset, &units}, func() error {
		held[asset] = bigOf(units)
		return nil
	}); err != nil {
		return nil, err
	}
	fiat, rate := s.fiatFor(ctx, userID)
	out := make([]map[string]any, 0, len(Assets))
	for _, a := range Assets {
		bal := held[a.Code]
		if bal == nil {
			bal = new(big.Int)
		}
		out = append(out, s.account(a, a.Code, bal, m, fiat, rate))
	}
	return out, nil
}

func adminKind(kind string) bool    { return kind == "simulation_admin" || kind == "admin_adjustment" }
func fragmentKind(kind string) bool { return kind == "fragment_purchase" || kind == "fragment_sale" }

// History is cryptoHistory(): the last hundred balance changes.
func (s *Service) History(ctx context.Context, userID int64) ([]map[string]any, error) {
	rows, err := s.db.Query(ctx, `SELECT e.id, e.event_key, e.asset, e.units::text, e.kind, e.peer_id, e.created_at, u.username, u.name
		FROM crypto_events e LEFT JOIN users u ON u.id = e.peer_id WHERE e.user_id=$1 ORDER BY e.id DESC LIMIT 100`, userID)
	if err != nil {
		return nil, err
	}
	type event struct {
		id               int64
		key, asset, kind string
		units            *big.Int
		peer             *int64
		at               int64
		username, name   *string
	}
	var list []event
	for rows.Next() {
		var e event
		var units string
		if err := rows.Scan(&e.id, &e.key, &e.asset, &units, &e.kind, &e.peer, &e.at, &e.username, &e.name); err != nil {
			rows.Close()
			return nil, err
		}
		e.units = bigOf(units)
		list = append(list, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		out = append(out, s.historyEntry(ctx, userID, e.id, e.key, e.asset, e.kind, e.units, e.peer, e.at, e.username, e.name))
	}
	return out, nil
}

func (s *Service) historyEntry(ctx context.Context, userID, id int64, key, asset, kind string, units *big.Int, peer *int64, at int64, username, name *string) map[string]any {
	out := new(big.Int).Abs(units)
	amount := format(out, asset)
	date := isoDate(at)
	gateway := "top_up"
	switch {
	case kind == "internal_transfer":
		gateway = "tg_transfer"
	case adminKind(kind) || fragmentKind(kind) || kind == "earn_move":
		gateway = "internal"
	case kind == "stars_purchase":
		gateway = "tg_stars_purchase"
	case kind == "crypto_swap":
		gateway = "crypto_exchange"
	}
	var shown, photo any
	switch {
	case adminKind(kind):
		shown, photo = "Admin", "/branding/logo"
	case fragmentKind(kind):
		shown, photo = "Fragment", "/__miniapps/fragment/img/fragment_icon.svg"
	case kind == "earn_move":
		shown = "Earn"
	case name != nil && *name != "":
		shown = *name
	case username != nil && *username != "":
		shown = *username
	}
	var peerID, mention any
	if peer != nil {
		peerID = *peer
	}
	if username != nil && *username != "" {
		mention = *username
	}
	dir, typ := "in", "deposit"
	if units.Sign() < 0 {
		dir, typ = "out", "withdraw"
	}
	entry := map[string]any{"id": fmt.Sprint(id), "currency": asset, "crypto_currency": asset, "amount": amount, "crypto_amount": amount,
		"direction": dir, "type": typ, "gateway": gateway, "date": date, "created_at": date, "status": "success",
		"peer_id": peerID, "tg_id": peerID, "username": shown, "mention": mention, "photo_url": photo,
		"local_fiat_amount": nil, "local_currency": "USD", "fee": "0", "input_addresses": []any{}, "is_blocked": false}
	if kind == "crypto_swap" {
		swap := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(key, "swap:"), ":from"), ":to")
		var from, to, fromUnits, toUnits string
		err := s.db.QueryRow(ctx, "SELECT from_asset, to_asset, from_units::text, to_units::text FROM crypto_swap_quotes WHERE id=$1 AND user_id=$2",
			swap, userID).Scan(&from, &to, &fromUnits, &toUnits)
		if err == nil {
			if units.Sign() < 0 {
				entry["pair_transaction_currency"], entry["pair_transaction_amount"] = to, format(bigOf(toUnits), to)
			} else {
				entry["pair_transaction_currency"], entry["pair_transaction_amount"] = from, format(bigOf(fromUnits), from)
			}
		}
	}
	return entry
}

func field(data map[string]any, keys ...string) any {
	for _, k := range keys {
		if truthy(data[k]) {
			return data[k]
		}
	}
	return nil
}

// recipient is localCryptoRecipient(): another active person, by id or
// username.
func (s *Service) recipient(ctx context.Context, q querier, user int64, data map[string]any) (person, error) {
	notFound := fail(404, "Choose another active person on this instance.")
	var p person
	var ok bool
	var err error
	if id := field(data, "recipientId", "recipient_id", "user_id", "receiver_tg_id"); id != nil {
		var n int64
		if _, scanErr := fmt.Sscan(jsString(id), &n); scanErr != nil {
			return p, notFound
		}
		p, ok, err = activePerson(ctx, q, n)
	} else {
		name := strings.ToLower(strings.TrimPrefix(jsString(field(data, "username", "mention")), "@"))
		var id int64
		err = q.QueryRow(ctx, "SELECT id FROM users WHERE lower(username)=$1", name).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return p, notFound
		}
		if err == nil {
			p, ok, err = activePerson(ctx, q, id)
		}
	}
	if err != nil {
		return p, err
	}
	if !ok || p.ID == user {
		return p, notFound
	}
	return p, nil
}

// SearchRecipient answers the send screen's lookup; no match is a null user.
func (s *Service) SearchRecipient(ctx context.Context, u account.User, mention, id string) (map[string]any, error) {
	p, err := s.recipient(ctx, s.db, u.ID, map[string]any{"mention": mention, "recipientId": id})
	var e *account.Error
	if errors.As(err, &e) && e.Status == 404 {
		return map[string]any{"user": nil}, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"user": map[string]any{"id": p.ID, "tg_id": p.ID, "mention": p.Username, "tg_full_name": p.Name, "tg_photo_url": s.photo(p.Photo)}}, nil
}

// Recipients is who Wallet's transfer sheet offers: people you have in
// contacts or have talked to, most recent first; with q, those whose name
// has a word starting with it, and anyone whose public username does.
func (s *Service) Recipients(ctx context.Context, u account.User, q string) (map[string]any, error) {
	q = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(q), "@"))
	like := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q) + "%"
	rows, err := s.db.Query(ctx, `SELECT p.id, p.username, p.name, coalesce(p.photo_id, 0)
		FROM users p
		LEFT JOIN dialogs d ON d.owner_id=$1 AND d.peer_id=p.id
		LEFT JOIN contacts c ON c.owner_id=$1 AND c.user_id=p.id
		WHERE p.id<>$1 AND NOT p.disabled AND NOT p.is_system AND NOT p.is_bot
			AND (
				(d.owner_id IS NOT NULL OR c.owner_id IS NOT NULL) AND ($2='' OR lower(p.name) LIKE $3 OR lower(p.name) LIKE '% ' || $3 OR lower(p.username) LIKE $3)
				OR $2<>'' AND p.username_public AND lower(p.username) LIKE $3
			)
		ORDER BY d.top_date DESC NULLS LAST, lower(p.name), p.id LIMIT 30`, u.ID, q, like)
	if err != nil {
		return nil, err
	}
	people, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) {
		var p person
		err := r.Scan(&p.ID, &p.Username, &p.Name, &p.Photo)
		return map[string]any{"id": p.ID, "mention": p.Username, "tg_full_name": p.Name, "tg_photo_url": s.photo(p.Photo)}, err
	})
	if err != nil {
		return nil, err
	}
	if people == nil {
		people = []map[string]any{}
	}
	return map[string]any{"users": people}, nil
}

// nanosOf rescales to 9 decimals, clamped to a long. An 18-decimal asset can
// lose dust here; only the picture in the chat reads this, not the ledger.
func nanosOf(units *big.Int, code string) int64 {
	a, _ := Lookup(code)
	n := new(big.Int).Set(units)
	if d := a.Decimals - 9; d > 0 {
		n.Quo(n, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(d)), nil))
	} else if d < 0 {
		n.Mul(n, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-d)), nil))
	}
	if !n.IsInt64() {
		return math.MaxInt64
	}
	return n.Int64()
}

func centsOf(amount, price string) int64 {
	v, ok := MultiplyMarketAmount(amount, price)
	if !ok {
		return 0
	}
	whole, frac, _ := strings.Cut(v, ".")
	frac = (frac + "00")[:2]
	n, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func transferAsset(data map[string]any, keys ...string) string {
	return Canonical(jsString(field(data, keys...)))
}

func (s *Service) ValidateTransfer(ctx context.Context, u account.User, data map[string]any) (map[string]any, error) {
	code := transferAsset(data, "currency", "asset", "crypto_currency")
	amount, err := parseAmount(jsString(data["amount"]), code)
	if err != nil {
		return nil, err
	}
	avail, err := s.Balance(ctx, u.ID, code)
	if err != nil {
		return nil, err
	}
	if truthy(data["address"]) {
		return nil, fail(409, "External blockchain transfers are unavailable. Choose a person on this instance.")
	}
	if _, err := s.recipient(ctx, s.db, u.ID, data); err != nil {
		return nil, err
	}
	switch {
	case amount.Sign() < 1:
		return map[string]any{"status": "MIN_LIMIT_EXCEEDED", "min_limit": format(big.NewInt(1), code)}, nil
	case amount.Cmp(avail) > 0:
		return map[string]any{"status": "INSUFFICIENT_FUNDS", "need_to_buy": format(new(big.Int).Sub(amount, avail), code), "fee": "0"}, nil
	}
	return map[string]any{"status": "OK", "amount": format(amount, code), "fee": "0"}, nil
}

func (s *Service) QuoteTransfer(ctx context.Context, u account.User, data map[string]any) (map[string]any, error) {
	code := transferAsset(data, "asset", "crypto_currency", "currency")
	units, err := parseAmount(jsString(data["amount"]), code)
	if err != nil {
		return nil, err
	}
	target, err := s.recipient(ctx, s.db, u.ID, data)
	if err != nil {
		return nil, err
	}
	if units.Sign() < 1 {
		return nil, fail(400, "Choose another person and a positive crypto amount.")
	}
	bal, err := s.Balance(ctx, u.ID, code)
	if err != nil {
		return nil, err
	}
	if bal.Cmp(units) < 0 {
		return nil, errInsufficient
	}
	id := randomHex(16)
	if _, err := s.db.Exec(ctx, "INSERT INTO crypto_transfer_quotes(id, user_id, recipient_id, asset, units, expires_at) VALUES ($1, $2, $3, $4, $5::numeric, $6)",
		id, u.ID, target.ID, code, units.String(), s.unix()+300); err != nil {
		return nil, err
	}
	return map[string]any{"transfer_request_id": id, "id": id, "asset": code, "crypto_currency": code, "amount": format(units, code),
		"recipient": map[string]any{"id": target.ID, "username": target.Username, "first_name": target.Name, "photo_url": s.photo(target.Photo)}, "expires_in": 300}, nil
}

func (s *Service) SendTransfer(ctx context.Context, u account.User, id string) (map[string]any, error) {
	done := map[string]any{"ok": true, "status": "completed", "transaction_id": id}
	var recipient int64
	var code string
	var units *big.Int
	sent := false
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var completed bool
		var expires int
		var raw string
		err := tx.QueryRow(ctx, "SELECT recipient_id, asset, units::text, expires_at, completed FROM crypto_transfer_quotes WHERE id=$1 AND user_id=$2 FOR UPDATE",
			id, u.ID).Scan(&recipient, &code, &raw, &expires, &completed)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(404, "Transfer request not found.")
		}
		if err != nil || completed {
			return err
		}
		if expires <= s.unix() {
			return fail(410, "The transfer request has expired.")
		}
		if _, ok, err := activePerson(ctx, tx, recipient); err != nil || !ok {
			if err == nil {
				err = fail(404, "Recipient unavailable.")
			}
			return err
		}
		if err := lock(ctx, tx, u.ID, recipient); err != nil {
			return err
		}
		units = bigOf(raw)
		if _, err := change(ctx, tx, u.ID, code, new(big.Int).Neg(units)); err != nil {
			return err
		}
		if _, err := change(ctx, tx, recipient, code, units); err != nil {
			return err
		}
		if err := s.event(ctx, tx, "transfer:"+id+":out", u.ID, code, new(big.Int).Neg(units), "internal_transfer", recipient); err != nil {
			return err
		}
		if err := s.event(ctx, tx, "transfer:"+id+":in", recipient, code, units, "internal_transfer", u.ID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE crypto_transfer_quotes SET completed=true WHERE id=$1", id)
		sent = err == nil
		return err
	})
	if err != nil {
		return nil, err
	}
	if sent && s.notify != nil {
		shown := code
		if code == "TON" {
			shown = "GRAM"
		}
		text := u.Name + " sent " + format(units, code) + " " + shown + " via Wallet.\nTransfer reference: " + id
		t := Transfer{From: u.ID, To: recipient, Asset: code, Nanos: nanosOf(units, code), ID: id, Text: text}
		if q, fresh := s.fresh(s.market.Read(), code); fresh {
			t.USDCents = centsOf(format(units, code), q.Price)
		}
		if err := s.notify(ctx, t); err != nil {
			slog.Warn("wallet: transfer message", "err", err)
		}
	}
	return done, nil
}
