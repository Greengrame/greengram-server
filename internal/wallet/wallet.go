package wallet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/secret"
	"github.com/Greengrame/greengram-server/internal/settings"
	"github.com/Greengrame/greengram-server/internal/stars"
)

// Transfer is a finished send between two people, for the message in their chat.
type Transfer struct {
	From, To int64
	Asset    string
	Nanos    int64 // the amount at 9 decimals whatever the asset's, to fit the TL's long
	USDCents int64 // 0 without a fresh price
	ID       string
	Text     string // what search finds, and what the row reads as without its action
}

// Notifier tells a person they got crypto.
type Notifier func(ctx context.Context, t Transfer) error

type Service struct {
	signingKey *secret.Key
	db         *pgxpool.Pool
	settings   *settings.Source
	market     MarketSource
	charts     ChartSource
	notify     Notifier
	fx         *FXCache
	photoURL   func(id int64) string
	now        func() time.Time
}

// New wires the wallet. market is usually NewMarket(MarketOptions{});
// notify may be nil, then transfers send no message.
func New(db *pgxpool.Pool, set *settings.Source, market MarketSource, notify Notifier) *Service {
	return &Service{db: db, settings: set, market: market, charts: &ChartCache{}, notify: notify, fx: &FXCache{}, now: time.Now}
}

// WithPhotos says how a page can load a profile photo; without it people
// are drawn as initials.
func (s *Service) WithPhotos(url func(id int64) string) *Service {
	s.photoURL = url
	return s
}

func (s *Service) photo(id int64) any {
	if id == 0 || s.photoURL == nil {
		return nil
	}
	return s.photoURL(id)
}

// WithCharts swaps the price-history source (tests use a fake).
func (s *Service) WithCharts(c ChartSource) *Service {
	s.charts = c
	return s
}

func (s *Service) unix() int { return int(s.now().Unix()) }

func (s *Service) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.db, fn)
}

func fail(status int, msg string) error { return &account.Error{Status: status, Message: msg} }

var errInsufficient = fail(400, "Insufficient cryptocurrency balance.")

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func bigOf(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return new(big.Int)
	}
	return n
}

// Balance is a user's main-account units of an asset.
func Balance(ctx context.Context, q querier, user int64, code string) (*big.Int, error) {
	var units string
	err := q.QueryRow(ctx, "SELECT units::text FROM crypto_accounts WHERE user_id=$1 AND asset=$2", user, Canonical(code)).Scan(&units)
	if errors.Is(err, pgx.ErrNoRows) {
		return new(big.Int), nil
	}
	if err != nil {
		return nil, err
	}
	return bigOf(units), nil
}

func (s *Service) Balance(ctx context.Context, user int64, code string) (*big.Int, error) {
	return Balance(ctx, s.db, user, code)
}

func setBalance(ctx context.Context, tx pgx.Tx, user int64, code string, units *big.Int) error {
	if units.Sign() < 0 {
		return errInsufficient
	}
	_, err := tx.Exec(ctx, `INSERT INTO crypto_accounts(user_id, asset, units) VALUES ($1, $2, $3::numeric)
		ON CONFLICT (user_id, asset) DO UPDATE SET units = excluded.units`, user, code, units.String())
	return err
}

// change moves a balance by diff; the caller holds stars.Lock on user.
func change(ctx context.Context, tx pgx.Tx, user int64, code string, diff *big.Int) (*big.Int, error) {
	cur, err := Balance(ctx, tx, user, code)
	if err != nil {
		return nil, err
	}
	cur.Add(cur, diff)
	return cur, setBalance(ctx, tx, user, code, cur)
}

func (s *Service) event(ctx context.Context, tx pgx.Tx, key string, user int64, code string, units *big.Int, kind string, peer int64) error {
	var p *int64
	if peer != 0 {
		p = &peer
	}
	_, err := tx.Exec(ctx, "INSERT INTO crypto_events(event_key, user_id, asset, units, kind, peer_id, created_at) VALUES ($1, $2, $3, $4::numeric, $5, $6, $7)",
		key, user, code, units.String(), kind, p, s.unix())
	return err
}

// lock is stars.Lock: one row lock per user covers both ledgers.
func lock(ctx context.Context, tx pgx.Tx, users ...int64) error { return stars.Lock(ctx, tx, users...) }

type person struct {
	ID       int64
	Username string
	Name     string
	Photo    int64
}

// activePerson is an account that can hold a wallet: not disabled, not a
// system account, not a bot.
func activePerson(ctx context.Context, q querier, id int64) (person, bool, error) {
	var p person
	err := q.QueryRow(ctx, "SELECT id, username, name, coalesce(photo_id, 0) FROM users WHERE id=$1 AND NOT disabled AND NOT is_system AND NOT is_bot", id).Scan(&p.ID, &p.Username, &p.Name, &p.Photo)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, false, nil
	}
	return p, err == nil, err
}

func (s *Service) requirePerson(ctx context.Context, q querier, id int64) error {
	_, ok, err := activePerson(ctx, q, id)
	if err == nil && !ok {
		err = fail(403, "An active person account is required.")
	}
	return err
}

// parseAmount is parseCryptoAmount with its two refusals.
func parseAmount(value any, code string) (*big.Int, error) {
	str, isStr := value.(string)
	a, found := Lookup(code)
	if !isStr || !found || len(str) > 60 || !amountRe.MatchString(str) {
		return nil, fail(400, "Choose a supported asset and a decimal amount.")
	}
	n, ok := ParseAmount(str, code)
	if !ok {
		return nil, fail(400, "Use up to "+strconv.Itoa(a.Decimals)+" decimal places for "+code+".")
	}
	return n, nil
}

func format(units *big.Int, code string) string { return FormatAmount(units, code) }

// jsString is String(v || ”) for decoded JSON and query values.
func jsString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == 0 {
			return ""
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "true"
		}
	}
	return ""
}

// safeInt is Number.isSafeInteger on a decoded JSON value.
func safeInt(v any) (int64, bool) {
	f, ok := v.(float64)
	if !ok || f != float64(int64(f)) || f > 1<<53-1 || f < -(1<<53-1) {
		return 0, false
	}
	return int64(f), true
}

// jsInt is Number(v) for an id that may come as a number or a string.
func jsInt(v any) (int64, bool) {
	if s, ok := v.(string); ok {
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return n, err == nil && n <= 1<<53-1 && n >= -(1<<53-1)
	}
	return safeInt(v)
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	}
	return true
}

func isoDate(unix int64) string { return time.Unix(unix, 0).UTC().Format("2006-01-02T15:04:05.000Z") }

func (s *Service) fresh(m Market, code string) (Quote, bool) { return m.Fresh(code, s.now()) }
