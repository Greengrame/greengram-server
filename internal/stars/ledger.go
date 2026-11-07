// Package stars is the Stars ledger and what it pays for: Premium, star
// gifts and channel boosts. Rules and numbers are greengram-web's
// (wallet.js, gifts.js, boosts.js, star-transactions.js).
package stars

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Greengrame/greengram-server/internal/settings"
)

type Service struct {
	db       *pgxpool.Pool
	settings *settings.Source
	now      func() time.Time
	skew     atomic.Int64 // seconds added to now, for tests

	// Countries narrows a giveaway's draw to its countries; nil, and a
	// giveaway with countries finds nobody.
	Countries CountryFilter
}

func New(db *pgxpool.Pool, set *settings.Source) *Service {
	return &Service{db: db, settings: set, now: time.Now}
}

func (s *Service) unix() int { return int(s.now().Unix() + s.skew.Load()) }

// Skip moves this service's clock ahead, for tests that need a giveaway's
// end date to come.
func (s *Service) Skip(d time.Duration) { s.skew.Add(int64(d / time.Second)) }

func (s *Service) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.db, fn)
}

var (
	ErrBalanceTooLow = tgerr.New(400, "BALANCE_TOO_LOW")
	// past MaxBalance
	ErrBalanceTooHigh = tgerr.New(400, "STARS_AMOUNT_INVALID")
	// ErrDuplicate is a reference that is already in the ledger.
	ErrDuplicate = errors.New("stars: duplicate reference")
)

// Entry is one ledger row. Amount is signed: spending is negative.
type Entry struct {
	User      int64
	Amount    int64
	Reason    string
	Reference string
	CreatedBy int64
	Peer      int64 // the other side; 0 for none
	Title     string
	Note      string
	Months    int
	GiftID    int64
	Channel   int64 // the giveaway's channel; 0 for none
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Lock takes the users' rows, in id order, so a balance checked under the
// lock still holds when the debit lands.
func Lock(ctx context.Context, tx pgx.Tx, users ...int64) error {
	_, err := tx.Exec(ctx, "SELECT 1 FROM users WHERE id = ANY($1) ORDER BY id FOR UPDATE", users)
	return err
}

func Balance(ctx context.Context, q querier, user int64) (int64, error) {
	var n int64
	err := q.QueryRow(ctx, "SELECT coalesce(sum(amount), 0) FROM star_ledger WHERE user_id=$1", user).Scan(&n)
	return n, err
}

func (s *Service) Balance(ctx context.Context, user int64) (int64, error) {
	return Balance(ctx, s.db, user)
}

// MaxBalance is 2^53-1: the web clients read balances as doubles.
const MaxBalance = 1<<53 - 1

// Post writes e inside the caller's transaction and returns its
// transaction id. The caller holds Lock on e.User. Post refuses an entry
// that would take the balance below zero or past MaxBalance; the check
// and the insert are one statement.
func Post(ctx context.Context, tx pgx.Tx, e Entry, now int) (string, error) {
	if e.Amount == 0 || e.Reason == "" || e.Reference == "" {
		return "", errors.New("stars: incomplete ledger entry")
	}
	if e.Amount > MaxBalance || e.Amount < -MaxBalance {
		return "", ErrBalanceTooHigh
	}
	id := newTransactionID()
	var peer *int64
	if e.Peer != 0 {
		peer = &e.Peer
	}
	var gift *int64
	if e.GiftID != 0 {
		gift = &e.GiftID
	}
	var channel *int64
	if e.Channel != 0 {
		channel = &e.Channel
	}
	var after int64
	var row *int64
	err := tx.QueryRow(ctx, `WITH bal AS (SELECT coalesce(sum(amount), 0) + $2::bigint AS after FROM star_ledger WHERE user_id=$1::bigint),
		ins AS (INSERT INTO star_ledger(user_id, amount, reason, reference, created_by, created_at, transaction_id, peer_id, title, note, months, gift_id, channel_id)
			SELECT $1::bigint, $2::bigint, $3::text, $4::text, $5::bigint, $6::int, $7::text, $8::bigint, $9::text, $10::text, $11::int, $12::bigint, $14::bigint
			FROM bal WHERE after BETWEEN 0 AND $13::bigint
			ON CONFLICT (reference) DO NOTHING RETURNING id)
		SELECT (SELECT after FROM bal)::bigint, (SELECT id FROM ins)`,
		e.User, e.Amount, e.Reason, e.Reference, e.CreatedBy, now, id, peer, e.Title, e.Note, e.Months, gift, int64(MaxBalance), channel).Scan(&after, &row)
	switch {
	case err != nil:
		return "", err
	case row != nil:
		return id, nil
	case after < 0:
		return "", ErrBalanceTooLow
	case after > MaxBalance:
		return "", ErrBalanceTooHigh
	}
	return "", ErrDuplicate
}

// transaction ids are what the web showed (a UUID); clients only echo them
func newTransactionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// System is a built-in account that shows up as a transaction's peer.
type System struct{ Kind, Username, Name string }

var (
	PremiumAccount = System{"premium_payments", "instance_premium_payments", "Premium"}
	StarsAccount   = System{"stars_payments", "instance_stars_payments", "Stars"}
	AdminAccount   = System{"stars_issuer", "admin", "Admin"}
	WalletAccount  = System{"wallet", "wallet", "Wallet"}
)

var errSystemTaken = errors.New("stars: system account handle is taken by a person")

// Account finds or makes the system account. Its handle is reserved for
// sign-ups, so only an existing system or bot account can already hold it
// (the bots phase may make @wallet a real bot; then that is the peer).
func Account(ctx context.Context, tx pgx.Tx, sys System) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, "SELECT user_id FROM system_accounts WHERE kind=$1", sys.Kind).Scan(&id)
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return id, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO users(username, name, password, is_system) VALUES ($1, $2, '', true)
		ON CONFLICT DO NOTHING RETURNING id`, sys.Username, sys.Name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		var system bool
		err = tx.QueryRow(ctx, "SELECT id, is_system OR is_bot FROM users WHERE lower(username)=$1", strings.ToLower(sys.Username)).Scan(&id, &system)
		if err == nil && !system {
			return 0, errSystemTaken
		}
	}
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO system_accounts(kind, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", sys.Kind, id); err != nil {
		return 0, err
	}
	err = tx.QueryRow(ctx, "SELECT user_id FROM system_accounts WHERE kind=$1", sys.Kind).Scan(&id)
	return id, err
}
