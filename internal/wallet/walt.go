package wallet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/stars"
)

// Walt is the backend the Wallet mini-app calls under /api/walt (walt-api.js
// and walt-read-model.js). The app expects a full custodial wallet API;
// what this instance has no use for gets the empty answer that keeps its
// screens quiet, the rest maps onto the local wallet.

var (
	spendPath     = regexp.MustCompile(`/(?:transfers/(?:create_transfer_request|process_transfer/[^/]+)|exchange/(?:create_exchange|submit_exchange/[^/]+)|earn/public-api/v1/campaigns/[^/]+/(?:deposit|withdraw|full-withdraw))/?$`)
	defiPath      = regexp.MustCompile(`^/local/defi(?:/(?:create|quote|confirm))?$`)
	marketPath    = regexp.MustCompile(`/accounts(?:/|$)|/coins/`)
	telemetryPath = regexp.MustCompile(`^/(?:v1/events|v2/(?:meter|logs))/?$`)
	submitPath    = regexp.MustCompile(`/exchange/submit_exchange/[a-f0-9]{48}/?$`)
	accountsPath  = regexp.MustCompile(`/accounts/?$`)
	detailsPath   = regexp.MustCompile(`/coins/details/([A-Za-z0-9]+)/[A-Z]{3}/?$`)
	coinsPath     = regexp.MustCompile(`/coins/(?:list|trending)/([A-Z]{3})/?$`)
	catalogPath   = regexp.MustCompile(`/coins/catalog/[A-Z]{3}/?$`)
	accountPath   = regexp.MustCompile(`/accounts/([A-Za-z0-9]+)/$`)
	chartsPath    = regexp.MustCompile(`/charts/([A-Za-z0-9]+)/([A-Z]{3})/?$`)
	limitPath     = regexp.MustCompile(`/transfers/get_limit/([A-Za-z0-9]+)/$`)
	processPath   = regexp.MustCompile(`/transfers/process_transfer/([a-f0-9]+)/$`)
	txListPath    = regexp.MustCompile(`/transactions(?:/(?:single-account|crypto))?/?$`)
	txDetailPath  = regexp.MustCompile(`/transactions/(?:details|reference_transaction_details|withdraw_details)/(\d+)/?$`)
	emptyListPath = regexp.MustCompile(`/(?:notifications|banners|transactions|history|watchlist|collections|positions|campaigns|alerts)(?:/|$)`)
	digitsRe      = regexp.MustCompile(`^\d+$`)

	passcodeInfoPath = regexp.MustCompile(`/public-api/passcode/info/?$`)
	networksPath     = regexp.MustCompile(`/users/available_networks/?$`)
	unconfirmedPath  = regexp.MustCompile(`/transactions/unconfirmed/?$`)
	providerPath     = regexp.MustCompile(`/transfers/(?:get_limit|price_for_fiat)/`)
	chainWalletPath  = regexp.MustCompile(`/get_or_create_wallets/[^/]+`)
)

// stubs are fixed answers, matched in order: method, path pattern.
var stubs = []struct {
	method string
	path   *regexp.Regexp
	body   string
}{
	{"GET", regexp.MustCompile(`^/onboarding/public-api/v1/offers/?$`), `[]`},
	{"GET", regexp.MustCompile(`/payment_links/enabled/?$`), `{"enabled":false}`},
	{"GET", regexp.MustCompile(`/perps/public-api/v1/frontend-flags/?$`), `{"flags":{},"user_flags":{"view_perps_enabled":false,"open_order_enabled":false,"adjust_margin_enabled":false,"edit_position_enabled":false}}`},
	{"GET", regexp.MustCompile(`/alerting-banners/?$`), `{"status":"SUCCESS","data":{"pages":{},"degradationBanners":{}}}`},
	{"GET", regexp.MustCompile(`/giveaways/gift/available/?$`), `{"gift":null}`},
	{"GET", regexp.MustCompile(`/pnl/assets/?$`), `{"assets":[],"positions":[],"total":{"fiat_currency":"USD","current_value_in_fiat":null,"absolute_all_time":null,"absolute_24h":null,"pnl":null,"pnl_percent":null,"realized_pnl":null,"unrealized_pnl":null}}`},
}

var stubs2 = []struct {
	method string
	path   *regexp.Regexp
	body   string
}{
	{"GET", regexp.MustCompile(`^/referral/public-api/v1/programs/?$`), `[]`},
	{"GET", regexp.MustCompile(`/adresses/all/?$`), `{"all":[],"actives":[],"backups":[]}`},
	{"GET", regexp.MustCompile(`/meta/config/?$`), `{"enabled":false,"is_enabled":false,"features":{},"networks":[],"currencies":[]}`},
	{"GET", regexp.MustCompile(`/stars/availability/?$`), `{"available":false}`},
	{"GET", regexp.MustCompile(`/public-api/v1/ramp/?$`), `{"buy":{"crypto_currencies":[]},"sell":{"crypto_currencies":[]}}`},
	{"POST", regexp.MustCompile(`/loyalty/public-api/v1/availability/?$`), `{"available":false}`},
	{"POST", regexp.MustCompile(`/loyalty/public-api/v1/tooltip/(?:claim|ack)/?$`), `{"acknowledged":true,"spending_option":null,"available":false}`},
	{"POST", regexp.MustCompile(`/p2p/.*(?:all-supported|all_supported|supported/all)`), `{"status":"SUCCESS","data":{"crypto":[],"fiat":[]},"crypto":[],"fiat":[]}`},
	{"GET", regexp.MustCompile(`/p2p/public-api/v2/currency/available/?$`), `{"status":"SUCCESS","data":false}`},
	{"POST", regexp.MustCompile(`/p2p/public-api/v2/express/currency-pairs/?$`), `{"status":"SUCCESS","data":{"pairs":[]}}`},
	{"POST", regexp.MustCompile(`/p2p/public-api/v2/offer/settings/get/?$`), `{"status":"SUCCESS","data":null}`},
}

func raw(body string) json.RawMessage { return json.RawMessage(body) }

func (s *Service) viewSettings(ctx context.Context, u account.User) (map[string]any, error) {
	var hidden bool
	err := s.db.QueryRow(ctx, "SELECT hide_balances FROM wallet_view_flags WHERE user_id=$1", u.ID).Scan(&hidden)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return map[string]any{"userFlags": map[string]any{"is_custody_available": true, "wallet_onboarding_finished": true, "intended_use_completed": true,
		"send_onboarding_finished": true, "rebranding_splash_completed": true, "hide_balances": hidden}, "banners": map[string]any{}}, nil
}

// setViewSettings keeps what the app can't get wrong: of everything it posts,
// hiding balances is all that's a person's own choice. The other flags answer
// what this server offers, and stay fixed.
func (s *Service) setViewSettings(ctx context.Context, u account.User, data map[string]any) (map[string]any, error) {
	flags, _ := data["changedUserFlags"].(map[string]any)
	if hide, ok := flags["hide_balances"].(bool); ok {
		if _, err := s.db.Exec(ctx, `INSERT INTO wallet_view_flags(user_id, hide_balances) VALUES ($1, $2)
			ON CONFLICT (user_id) DO UPDATE SET hide_balances = excluded.hide_balances`, u.ID, hide); err != nil {
			return nil, err
		}
	}
	return s.viewSettings(ctx, u)
}

func publicUsername(u account.User) string {
	if u.UsernamePublic {
		return u.Username
	}
	return ""
}

// session is waltSession(). The jwt is unsigned on purpose: the app only
// reads it, the cookie is what authenticates.
func (s *Service) session(ctx context.Context, u account.User) map[string]any {
	go s.market.Refresh(context.WithoutCancel(ctx))
	claims, _ := json.Marshal(map[string]any{"sub": strconv.FormatInt(u.ID, 10), "exp": s.unix() + 3600})
	name := publicUsername(u)
	fiat, _ := s.fiatFor(ctx, u.ID)
	photo, _ := s.photo(u.PhotoID).(string)
	lang := s.language(ctx, u.ID)
	view, err := s.viewSettings(ctx, u)
	if err != nil {
		slog.Warn("wallet: view settings", "user", u.ID, "err", err)
	}
	return map[string]any{"user_id": u.ID, "user_info": map[string]any{"telegram_id": u.ID, "first_name": u.Name, "username": name,
		"telegram_username": name, "language_code": lang, "photo_url": photo}, "is_new_user": false, "bot_language": lang, "local_currency": fiat,
		"country_alpha2_ip": "", "country_alpha2_phone": "", "residence_country_alpha2": "", "bot_username": "wallet", "bot_is_blocked": false,
		"ab_tests": map[string]any{}, "restriction_code": nil, "subdivision_status": nil, "webview_settings": view,
		"created_at": u.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"), "session_id": "local-" + strconv.FormatInt(u.ID, 10),
		"jwt": "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".", "refresh_token": "", "access_token_expires_in": 3600}
}

// Walt answers one call. session is the session hash, for passcode unlocks.
func (s *Service) Walt(ctx context.Context, u account.User, session, method, path string, data map[string]any, query url.Values) (any, error) {
	if data == nil {
		data = map[string]any{}
	}
	if out, ok, err := s.Passcode(ctx, u, session, method, path, data); ok {
		return out, err
	}
	if defiPath.MatchString(path) {
		if method == "POST" && path == "/local/defi/confirm" {
			if err := s.RequireUnlocked(ctx, u.ID, session); err != nil {
				return nil, err
			}
		}
		return s.defi(ctx, u, method, path, data)
	}
	if path == "/local/stars" && method == "GET" {
		n, err := stars.Balance(ctx, s.db, u.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"balance": n}, nil
	}
	if method == "POST" && spendPath.MatchString(path) {
		if err := s.RequireUnlocked(ctx, u.ID, session); err != nil {
			return nil, err
		}
	}
	if out, ok, err := s.readModel(ctx, u, method, path, data, query); ok {
		return out, err
	}
	return s.walt(ctx, u, method, path, data, query)
}

func (s *Service) defi(ctx context.Context, u account.User, method, path string, data map[string]any) (any, error) {
	switch {
	case path == "/local/defi" && method == "GET":
		return s.DefiState(ctx, u)
	case path == "/local/defi/create" && method == "POST":
		return s.CreateDefi(ctx, u)
	case path == "/local/defi/quote" && method == "POST":
		return s.QuoteDefiMove(ctx, u, data)
	case path == "/local/defi/confirm" && method == "POST":
		return s.ConfirmDefiMove(ctx, u, data)
	}
	return nil, fail(405, "Method not allowed.")
}

func (s *Service) readModel(ctx context.Context, u account.User, method, path string, data map[string]any, query url.Values) (any, bool, error) {
	switch {
	case path == "/users/" && (method == "GET" || method == "POST"):
		for k := range data {
			if k != "utmTags" {
				return nil, true, fail(409, "These DeFi account settings are not available on this instance.")
			}
		}
		return map[string]any{"id": u.ID, "name": u.Name, "displaySettings": map[string]any{}, "earnData": map[string]any{}, "tonstakersTosAcceptedAt": nil}, true, nil
	case method == "GET" && path == "/wallet/collectiblePreviewsRecentlyAdded/":
		return raw(`{"collectibles":[],"cursor":null}`), true, nil
	case method == "GET" && path == "/public-api/recovery-email/":
		return raw(`{"email":null}`), true, nil
	case method == "GET" && path == "/users/public-api/v2/user/limits-settings":
		return raw(`{"status":"SUCCESS","data":null}`), true, nil
	case method != "GET" || path != "/v2/rates":
		return nil, false, nil
	}
	tokens := splitList(query.Get("tokens"))
	currencies := splitList(query.Get("currencies"))
	if query.Get("currencies") == "" {
		currencies = []string{"USD"}
	}
	long := false
	for _, t := range tokens {
		long = long || len(t) > 128
	}
	if len(tokens) > 100 || long || len(currencies) > 10 {
		return nil, true, fail(400, "Choose a smaller set of price quotes.")
	}
	m := s.market.Refresh(ctx)
	rates := map[string]any{}
	for _, token := range tokens {
		code := Canonical(token)
		a, ok := Lookup(code)
		q, fresh := s.fresh(m, code)
		if !ok || a.Code != code || !fresh {
			continue
		}
		prices, changes := map[string]any{}, map[string]any{}
		if contains(currencies, "USD") {
			prices["USD"] = q.Price
			if q.PriceChange24hPercent != nil {
				changes["USD"] = *q.PriceChange24hPercent + "%"
			}
		}
		if ton, ok := s.fresh(m, "TON"); ok && contains(currencies, "TON") {
			prices["TON"], _ = SwapRate(q.Price, ton.Price)
		}
		rates[token] = map[string]any{"prices": prices, "diff_24h": changes}
	}
	return map[string]any{"rates": rates, "market_source": m.Source, "market_updated_at": m.UpdatedAt}, true, nil
}

func splitList(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func queryData(q url.Values) map[string]any {
	out := make(map[string]any, len(q))
	for k, v := range q {
		if len(v) > 0 {
			out[k] = v[len(v)-1]
		}
	}
	return out
}

func (s *Service) walt(ctx context.Context, u account.User, method, path string, data map[string]any, query url.Values) (any, error) {
	var m Market
	if marketPath.MatchString(path) {
		m = s.market.Refresh(ctx)
	} else {
		m = s.market.Read()
	}
	get, post := method == "GET", method == "POST"
	switch {
	case post && telemetryPath.MatchString(path):
		return map[string]any{}, nil
	case get && strings.TrimSuffix(path, "/") == "/api/v1/notifications":
		return s.notificationSettings(ctx, u)
	case method == "PATCH" && strings.TrimSuffix(path, "/") == "/api/v1/notifications/update":
		return s.setNotificationSettings(ctx, u, data)
	case path == "/session":
		return s.session(ctx, u), nil
	case get && strings.HasSuffix(strings.TrimSuffix(path, "/"), "/exchange/get-available-exchanges"):
		out := map[string]any{}
		for _, a := range Assets {
			var others []string
			for _, b := range Assets {
				if b.Code != a.Code {
					others = append(others, b.Code)
				}
			}
			out[a.Code] = others
		}
		return out, nil
	case post && strings.HasSuffix(strings.TrimSuffix(path, "/"), "/exchange/convert"):
		return s.Convert(ctx, u, data)
	case post && strings.HasSuffix(strings.TrimSuffix(path, "/"), "/exchange/create_exchange"):
		return s.QuoteSwap(ctx, u, data)
	case post && submitPath.MatchString(path):
		parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
		return s.SubmitSwap(ctx, u, parts[len(parts)-1])
	}
	for _, st := range stubs {
		if method == st.method && st.path.MatchString(path) {
			return raw(st.body), nil
		}
	}
	if get && passcodeInfoPath.MatchString(path) {
		st, err := s.PasscodeStatus(ctx, u.ID, "")
		if err != nil {
			return nil, err
		}
		return map[string]any{"passcodeType": st["passcodeType"], "unlockDuration": unlockFor, "requiredOnOpen": true, "deviceBiometrySet": false}, nil
	}
	for _, st := range stubs2 {
		if method == st.method && st.path.MatchString(path) {
			return raw(st.body), nil
		}
	}
	if get && strings.HasSuffix(path, "/p2p/public-api/v2/user/get") || get && strings.HasSuffix(path, "/p2p/public-api/v2/user/get/") {
		return map[string]any{"status": "NOT_INITIALIZED", "data": map[string]any{"userId": u.ID, "isExperienced": false, "merchantLevel": nil,
			"is_p2p_available": false, "is_blocked": false, "roles": []any{}, "restrictions": []any{}}}, nil
	}
	switch {
	case strings.HasSuffix(path, "/auth") || strings.HasSuffix(path, "/auth-refresh"):
		return s.session(ctx, u), nil
	case strings.HasSuffix(path, "/webview-settings/get"), post && strings.HasSuffix(path, "/webview-settings/set"):
		view, err := s.viewSettings(ctx, u)
		if post {
			view, err = s.setViewSettings(ctx, u, data)
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"status": "SUCCESS", "data": view}, nil
	case strings.HasSuffix(path, "/feature-flags"):
		return map[string]any{"status": "SUCCESS", "data": map[string]any{}}, nil
	case strings.HasSuffix(path, "/profile/get"):
		p := s.session(ctx, u)
		p["first_name"], p["username"], p["language"] = u.Name, u.Username, p["bot_language"]
		// no identity checks here, so the settings row for them stays hidden
		// no identity checks exist here; below level 1 the app's Earn waits for one
		p["kycLevel"], p["kyc_level"] = "LEVEL_1", "LEVEL_1"
		return map[string]any{"status": "SUCCESS", "data": p}, nil
	case post && strings.HasSuffix(path, "/profile/set-local-currency"):
		code, _ := data["localCurrency"].(string)
		if err := s.setFiat(ctx, u, code); err != nil {
			return nil, err
		}
		return map[string]any{"status": "SUCCESS", "data": map[string]any{"localCurrency": strings.ToUpper(code)}}, nil
	case strings.HasSuffix(path, "/profile/languages"):
		return map[string]any{"status": "SUCCESS", "data": map[string]any{"available": availableLanguages()}}, nil
	case post && strings.HasSuffix(path, "/profile/set-language"):
		code, _ := data["language"].(string)
		if err := s.setLanguage(ctx, u, code); err != nil {
			return nil, err
		}
		return map[string]any{"status": "SUCCESS", "data": map[string]any{"language": code}}, nil
	case strings.HasSuffix(path, "/profile/local-currencies"):
		return map[string]any{"status": "SUCCESS", "data": map[string]any{"available": s.fiatCurrencies(ctx)}}, nil
	case accountsPath.MatchString(path):
		return s.accountsView(ctx, u, path, m)
	}
	if r := detailsPath.FindStringSubmatch(path); r != nil {
		return s.coinDetails(ctx, r[1], detailsFiat(path), m)
	}
	if catalogPath.MatchString(path) {
		return tradeCatalog(), nil
	}
	if r := coinsPath.FindStringSubmatch(path); r != nil {
		fx := s.fiatRate(ctx, r[1])
		coins := make([]map[string]any, 0, len(Assets))
		for _, a := range Assets {
			var rate any
			q, ok := m.Assets[a.Code]
			if ok && q.Price != "" {
				rate = convertFiat(q.Price, fx)
			}
			coins = append(coins, map[string]any{"currency": a.Code, "fiat_currency": r[1], "name": a.Name, "category": clientCategory(a), "networks": a.Networks, "rate": rate,
				"rate_change_24h_absolute": convertFiatPtr(q.PriceChange24h, fx), "rate_change_24h_percent": nullable(q.PriceChange24hPercent),
				"market_cap": convertFiatPtr(q.MarketCap, fx), "volume24h": convertFiatPtr(q.Volume24h, fx)})
		}
		return map[string]any{"coins": coins, "collections": []any{}, "items": []any{}}, nil
	}
	if out, ok, err := s.earn(ctx, u, method, path, data, query, m); ok {
		return out, err
	}
	if get && strings.TrimSuffix(path, "/") == "/subscription/public-api/v1/subscriptions" {
		return tierList(), nil
	}
	if get && strings.HasSuffix(strings.TrimSuffix(path, "/"), "/subscriptions/user/subscription") {
		return s.userTier(ctx, u, m)
	}
	if r := accountPath.FindStringSubmatch(path); r != nil {
		return s.Account(ctx, u.ID, r[1], m)
	}
	if r := chartsPath.FindStringSubmatch(path); r != nil {
		code := Canonical(r[1])
		if out := NativeCharts(scaleHistory(s.charts.Refresh(ctx, code, "USD"), s.fiatRate(ctx, r[2]))); out != nil {
			return out, nil
		}
		return nil, fail(503, "Price history is unavailable. Please try again shortly.")
	}
	if get && strings.HasSuffix(path, "/telegram/recipient") {
		return s.SearchRecipient(ctx, u, "", query.Get("user_id"))
	}
	if get && strings.HasSuffix(path, "/telegram/recipients") {
		return s.Recipients(ctx, u, query.Get("q"))
	}
	if get && strings.HasSuffix(path, "/telegram/search-user") {
		return s.SearchRecipient(ctx, u, query.Get("mention"), "")
	}
	if get && strings.HasSuffix(path, "/withdrawals/validate_amount/") {
		return s.ValidateTransfer(ctx, u, queryData(query))
	}
	if r := limitPath.FindStringSubmatch(path); get && r != nil {
		a, ok := Lookup(r[1])
		if !ok {
			return nil, fail(404, "Asset not supported.")
		}
		bal, err := s.Balance(ctx, u.ID, a.Code)
		if err != nil {
			return nil, err
		}
		return map[string]any{"currency": a.Code, "min_amount": format(big.NewInt(1), a.Code), "max_amount": format(bal, a.Code), "fee": "0"}, nil
	}
	if get && strings.HasSuffix(path, "/transfers/price_for_fiat/") {
		a, ok := Lookup(query.Get("crypto_currency"))
		if !ok {
			return nil, fail(400, "Choose a supported asset.")
		}
		q, fresh := s.fresh(m, a.Code)
		if !fresh {
			return nil, fail(503, "A current price is unavailable. You can enter an amount in cryptocurrency.")
		}
		local := query.Get("local_currency")
		return map[string]any{"rate": convertFiat(q.Price, s.fiatRate(ctx, local)), "crypto_currency": a.Code, "local_currency": local, "updated_at": q.UpdatedAt}, nil
	}
	if post && strings.HasSuffix(path, "/transfers/create_transfer_request/") {
		return s.QuoteTransfer(ctx, u, data)
	}
	if r := processPath.FindStringSubmatch(path); post && r != nil {
		return s.SendTransfer(ctx, u, r[1])
	}
	if networksPath.MatchString(path) {
		nets := map[string]any{}
		for _, a := range Assets {
			nets[a.Code] = []any{}
		}
		return map[string]any{"available_networks": nets}, nil
	}
	if unconfirmedPath.MatchString(path) {
		return raw(`{"transactions":[],"has_next":false,"cursor":null}`), nil
	}
	if txListPath.MatchString(path) {
		return s.transactions(ctx, u, query)
	}
	if r := txDetailPath.FindStringSubmatch(path); r != nil {
		list, err := s.History(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		for _, e := range list {
			if e["id"] == r[1] {
				return e, nil
			}
		}
		return nil, fail(404, "Transaction not found.")
	}
	switch {
	case providerPath.MatchString(path):
		return nil, fail(409, "This transfer flow needs a connected crypto provider. Instance transfers require an explicit local recipient.")
	case strings.HasSuffix(path, "/accounts/frozen"):
		return []any{}, nil
	case strings.HasSuffix(path, "/currencies/local_currency/"):
		fiat, _ := s.fiatFor(ctx, u.ID)
		return map[string]any{"currency": fiat, "code": fiat}, nil
	case strings.HasSuffix(path, "/get-kyc"):
		return raw(`{"status":"SUCCESS","data":{"kycStatus":{"level":"LEVEL_1","status":"NOT_STARTED","permissions":{"can_withdraw_inner":true,"can_withdraw_outer":false,"can_exchange":true}},"verificationStatus":"NOT_STARTED","restrictions":[]}}`), nil
	case get && emptyListPath.MatchString(path):
		return raw(`{"status":"SUCCESS","data":[],"items":[],"results":[],"total":0,"count":0}`), nil
	case chainWalletPath.MatchString(path):
		return nil, fail(409, "Blockchain wallets are not connected to this independent instance.")
	}
	return nil, fail(501, fmt.Sprintf("Walt backend operation is not implemented: %s %s", method, path))
}

func (s *Service) accountsView(ctx context.Context, u account.User, path string, m Market) (any, error) {
	accounts, err := s.Accounts(ctx, u.ID, m)
	if err != nil {
		return nil, err
	}
	fiat := make([]string, 0, len(accounts))
	var total any
	priced := true
	for _, acc := range accounts {
		acc["rate_to_fiat"], acc["rate_to_usd"] = acc["rate"], acc["rate"]
		if ok := acc["market_stale"] == false; ok {
			if q, fresh := s.fresh(m, acc["currency"].(string)); fresh {
				acc["rate_to_usd"] = q.Price
			}
		}
		v, ok := acc["available_balance_fiat_amount"].(string)
		priced = priced && ok
		fiat = append(fiat, v)
	}
	if priced {
		// the app adds Earn into its asset rows, so the total has to as well
		parked, err := s.earnValues(ctx, u, m)
		if err != nil {
			return nil, err
		}
		total, _ = SumMarketAmounts(append(fiat, parked...))
	}
	fiatCode, _ := s.fiatFor(ctx, u.ID)
	result := map[string]any{"user_id": u.ID, "accounts": accounts, "market": m, "fiat_currency": fiatCode,
		"overall_available_balance_fiat_amount": total, "available_balance_total_fiat_amount": total, "available_balance_total_fiat_currency": fiatCode,
		"access_to_withdraws": true, "access_to_exchanges": true, "access_to_exchanges_by_country": true, "access_to_reward_payments": false,
		"access_to_unstake": false, "access_to_p2p": false, "access_to_create_address": false, "access_to_set_user_currency": true,
		"access_to_saving_settings": true, "access_to_transaction_list": true, "access_to_asset_charts": true,
		"access_to_trade_stocks_and_etfs": true, "access_to_trade_stocks_and_etfs_by_country": true, "has_open_perp_position": false}
	if strings.TrimSuffix(path, "/") == "/portmone/public-api/v1/user/accounts" {
		return map[string]any{"status": "SUCCESS", "data": result}, nil
	}
	return result, nil
}

func detailsFiat(path string) string {
	parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
	return parts[len(parts)-1]
}

func (s *Service) coinDetails(ctx context.Context, code, fiat string, m Market) (any, error) {
	a, ok := Lookup(code)
	if !ok {
		return nil, fail(404, "Asset not supported.")
	}
	q, have := m.Assets[a.Code]
	_, fresh := s.fresh(m, a.Code)
	fx := s.fiatRate(ctx, fiat)
	h := scaleHistory(s.charts.Refresh(ctx, a.Code, "USD"), fx)
	var preview, updated, rate any
	if h != nil {
		preview = h.Charts["day"]
	}
	if have {
		updated = q.UpdatedAt
		if q.Price != "" {
			rate = convertFiat(q.Price, fx)
		}
	}
	return map[string]any{"market_source": m.Source, "market_updated_at": updated, "market_stale": !fresh, "currency": a.Code, "name": a.Name,
		"symbol": a.Code, "description": a.Name, "category": clientCategory(a), "networks": a.Networks, "rate": rate,
		"rate_change_24h_absolute": convertFiatPtr(q.PriceChange24h, fx), "rate_change_24h_percent": nullable(q.PriceChange24hPercent),
		"chart_preview_rate_24h": preview, "fiat_currency": fiat, "market_cap": convertFiatPtr(q.MarketCap, fx),
		"market_cap_change_24h_percent": nullable(q.MarketCapChange24hPercent), "volume24h": convertFiatPtr(q.Volume24h, fx),
		"volume24h_change_24h_percent": nil, "circulating_supply": nullable(q.CirculatingSupply), "total_supply": nullable(q.TotalSupply),
		"max_supply": nullable(q.MaxSupply), "links": []any{}, "mira_summary": map[string]any{"summary": ""}, "trading_hours_state": nil}, nil
}

func (s *Service) transactions(ctx context.Context, u account.User, query url.Values) (any, error) {
	limit := 50
	if v := query.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return nil, fail(400, "Choose a transaction limit between 1 and 100.")
		}
		limit = n
	}
	after := query.Get("last_id")
	if after == "" {
		after = query.Get("cursor")
	}
	if after != "" && !digitsRe.MatchString(after) {
		return nil, fail(400, "Invalid transaction cursor.")
	}
	var afterN *big.Int
	if after != "" {
		afterN = bigOf(after)
	}
	all, err := s.History(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	currency := query.Get("crypto_currency")
	gateways := query["transaction_gateway"]
	list := []map[string]any{}
	for _, e := range all {
		id := bigOf(e["id"].(string))
		switch {
		case currency != "" && e["currency"] != Canonical(currency),
			afterN != nil && id.Cmp(afterN) >= 0,
			query.Get("transaction_type") != "" && e["type"] != query.Get("transaction_type"),
			query.Get("transaction_status") != "" && e["status"] != query.Get("transaction_status"),
			len(gateways) > 0 && !contains(gateways, e["gateway"].(string)),
			query.Get("created_from") != "" && e["created_at"].(string) < query.Get("created_from"):
			continue
		}
		list = append(list, e)
	}
	page := list[:min(limit, len(list))]
	var next any
	more := len(list) > limit
	if more {
		next = list[limit-1]["id"]
	}
	return map[string]any{"transactions": page, "results": page, "count": len(list), "size": len(page), "next_cursor": next, "has_next": more, "cursor": next}, nil
}

// tradeCatalog is the Trade tab: the app ranks its coin list by these tabs.
func tradeCatalog() map[string]any {
	codes := make([]string, 0, len(Assets))
	for _, a := range Assets {
		codes = append(codes, a.Code)
	}
	tab := func(title, id, ranking string) map[string]any {
		return map[string]any{"title": title, "tab_id": id, "ranking_mode": ranking, "action": ranking, "currencies": []any{}}
	}
	section := func(id, title string, order int, tabs ...map[string]any) map[string]any {
		return map[string]any{"catalog_id": id, "order": order, "title": title, "display_mode": "list", "tabs_style": "segmented",
			"display_new_badge": false, "min_limit_currencies": 0, "action": nil, "limit_currencies": 5, "tabs": tabs}
	}
	return map[string]any{"display_products_section": false, "promotion_whitelist_currencies": codes, "catalog": []any{
		section("coins", "Cryptocurrencies", 0, tab("Top", "all", "all_limit"), tab("Gainers", "gainers", "gainers"), tab("Losers", "losers", "losers"), tab("Volume", "volume24h", "volume24h")),
		section("stocks", "Stocks & ETFs", 1, tab("Stocks", "stocks", "stocks"), tab("ETFs", "etfs", "etfs")),
	}}
}
