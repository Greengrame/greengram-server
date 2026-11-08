// Package erase deletes accounts. The admin panel, account.deleteAccount and
// the inactivity job (account.setAccountTTL) all go through Erase, so a
// deleted account looks the same whoever deleted it.
//
// The row stays: messages keep their author and show as Deleted Account in
// everyone else's chats. What made it someone goes: name, username, phone,
// password, photos, contacts, privacy rules, sign-ins and the auth keys
// behind them. What it owned that others rely on is handed on: chats to an
// admin, collectible usernames and numbers back to the instance, Stars,
// wallet balances and collectible gifts to the Deleted Accounts system
// account, so every ledger keeps its total.
//
// The lifecycle (a short tombstone write, sign-ins revoked after the reply
// to the deleting session, an inactivity sweep that re-checks each account
// under its lock) follows gramsrv's internal/store/postgres/
// account_lifecycle.go and internal/rpc/account_lifecycle_worker.go; the
// code is Greengram's own, the schemas have nothing in common.
package erase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/fragment"
	"github.com/Greengrame/greengram-server/internal/settings"
	"github.com/Greengrame/greengram-server/internal/stars"
	"github.com/Greengrame/greengram-server/internal/wallet"
)

const (
	SourceAdmin    = "admin"
	SourceSelf     = "self"
	SourceInactive = "inactive"
)

// Request names the account and who is deleting it: an administrator, the
// account itself, or nobody (Actor 0) for the inactivity job.
type Request struct {
	User   int64
	Actor  int64
	Source string
	Reason string
}

// Result is what Erase did, for the audit log and for the caller to finish
// outside the transaction: Keys are the auth keys it deleted (signed-in ones
// and the temp keys bound to them), Perms the signed-in ones, Events the
// chat updates to deliver.
type Result struct {
	User         int64               `json:"-"`
	Source       string              `json:"source"`
	Sessions     int64               `json:"sessions"`
	Keys         []int64             `json:"-"`
	Perms        []int64             `json:"-"`
	Chats        []channels.Handover `json:"chats"`
	Bots         []int64             `json:"bots"`
	Fragment     []fragment.Released `json:"fragment"`
	Stars        int64               `json:"stars"`
	Wallet       []wallet.Moved      `json:"wallet"`
	Collectibles []int64             `json:"collectibles"`
	GiftsKept    int64               `json:"giftsKept"`
	Photos       int64               `json:"photos"`
	Events       []channels.Event    `json:"-"`
}

var (
	ErrNotFound = errors.New("erase: no such account")
	// ErrRefused is a system account or a bot: bots go through bot
	// management, system accounts never go.
	ErrRefused = errors.New("erase: this account cannot be deleted")
	ErrDeleted = errors.New("erase: already deleted")
)

// Erase deletes r.User inside tx and writes the audit row. The caller has
// done its own checks (confirmation, password) and runs Finish after the
// commit.
func Erase(ctx context.Context, tx pgx.Tx, r Request, now time.Time) (Result, error) {
	res := Result{User: r.User, Source: r.Source}
	sys, err := stars.Account(ctx, tx, stars.ErasedAccount)
	if err != nil {
		return res, err
	}
	// one row lock per user covers the Stars ledger and the wallet
	if err := stars.Lock(ctx, tx, r.User, sys); err != nil {
		return res, err
	}
	var state string
	var system, bot bool
	err = tx.QueryRow(ctx, "SELECT account_state, is_system, is_bot FROM users WHERE id=$1", r.User).Scan(&state, &system, &bot)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return res, ErrNotFound
	case err != nil:
		return res, err
	case system || bot:
		return res, ErrRefused
	case state == "deleted":
		return res, ErrDeleted
	}
	unix := int(now.Unix())

	// sign-ins: the auth keys go with them, so an app that is still
	// connected gets -404 on its next packet and lands on the sign-in screen
	rows, err := tx.Query(ctx, `SELECT k.id, k.perm_id IS NULL FROM auth_keys k WHERE k.session_hash IN (SELECT token_hash FROM sessions WHERE user_id=$1)
		OR k.perm_id IN (SELECT p.id FROM auth_keys p WHERE p.session_hash IN (SELECT token_hash FROM sessions WHERE user_id=$1))`, r.User)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var id int64
		var perm bool
		if err := rows.Scan(&id, &perm); err != nil {
			rows.Close()
			return res, err
		}
		res.Keys = append(res.Keys, id)
		if perm {
			res.Perms = append(res.Perms, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM auth_keys WHERE id = ANY($1)", nonNil(res.Keys)); err != nil {
		return res, err
	}
	tag, err := tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", r.User)
	if err != nil {
		return res, err
	}
	res.Sessions = tag.RowsAffected()

	// chats first: a collectible name its channel uses follows the channel
	if res.Chats, res.Events, err = channels.EraseMember(ctx, tx, r.User); err != nil {
		return res, err
	}
	if res.Fragment, err = fragment.EraseOwner(ctx, tx, r.User); err != nil {
		return res, err
	}

	// bots stop: no token, disabled; the admin panel can only turn one back
	// on once its owner exists again, which a deleted one never does
	rows, err = tx.Query(ctx, "SELECT user_id FROM bots WHERE owner_id=$1 AND kind='custom' ORDER BY user_id", r.User)
	if err != nil {
		return res, err
	}
	if res.Bots, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
		return res, err
	}
	if len(res.Bots) > 0 {
		if _, err := tx.Exec(ctx, "UPDATE bots SET token_hash=NULL, webapp_key=NULL, token_sealed=NULL, info_version = info_version + 1 WHERE user_id = ANY($1)", res.Bots); err != nil {
			return res, err
		}
		if _, err := tx.Exec(ctx, "UPDATE users SET disabled=true, account_state='locked', is_suspended=false WHERE id = ANY($1)", res.Bots); err != nil {
			return res, err
		}
	}

	// money: Stars and every wallet balance to the system account, as
	// ledger pairs, so totals don't move
	ref := fmt.Sprintf("erase:%d", r.User)
	by := r.Actor
	if by == 0 {
		by = sys
	}
	if res.Stars, err = stars.Balance(ctx, tx, r.User); err != nil {
		return res, err
	}
	if res.Stars > 0 {
		out := stars.Entry{User: r.User, Amount: -res.Stars, Reason: "account_deleted", Reference: ref + ":stars", CreatedBy: by, Peer: sys, Title: "Account deleted"}
		in := stars.Entry{User: sys, Amount: res.Stars, Reason: "account_deleted", Reference: ref + ":stars:in", CreatedBy: by, Peer: r.User, Title: "Deleted account"}
		for _, e := range []stars.Entry{out, in} {
			if _, err := stars.Post(ctx, tx, e, unix); err != nil {
				return res, err
			}
		}
	}
	if res.Wallet, err = wallet.MoveAll(ctx, tx, r.User, sys, ref, unix); err != nil {
		return res, err
	}

	// collectible gifts are assets: off sale, onto the system account.
	// Plain gifts stay on the profile nobody can open; they were only ever
	// worth their conversion to this account.
	rows, err = tx.Query(ctx, `UPDATE gift_receipts SET owner_id=$2, displayed=false, pinned=NULL, message_id=NULL
		WHERE owner_id=$1 AND unique_id IS NOT NULL AND NOT converted RETURNING unique_id`, r.User, sys)
	if err != nil {
		return res, err
	}
	if res.Collectibles, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
		return res, err
	}
	if _, err := tx.Exec(ctx, "UPDATE unique_gifts SET resale_stars=NULL, resale_since=0 WHERE id = ANY($1)", nonNil(res.Collectibles)); err != nil {
		return res, err
	}
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM gift_receipts WHERE owner_id=$1 AND NOT converted", r.User).Scan(&res.GiftsKept); err != nil {
		return res, err
	}

	tag, err = tx.Exec(ctx, "DELETE FROM user_photos WHERE user_id=$1", r.User)
	if err != nil {
		return res, err
	}
	res.Photos = tag.RowsAffected()

	// the tombstone; messages keep pointing at it
	if _, err := tx.Exec(ctx, `UPDATE users SET name='Deleted Account', username='deleted_' || id, password='', phone=NULL, phone_lookup=NULL, bio='',
		username_public=false, premium_until=0, premium_starts_at=0, photo_id=NULL, emoji_status_id=NULL, emoji_status_until=NULL,
		emoji_status_collectible=NULL, account_state='deleted', disabled=true, is_suspended=false, deleted_at=$2, deletion_source=$3 WHERE id=$1`,
		r.User, now, r.Source); err != nil {
		return res, err
	}
	for _, q := range []string{
		"DELETE FROM auth_flows WHERE user_id=$1",
		"DELETE FROM privacy_rules WHERE user_id=$1",
		"DELETE FROM contacts WHERE owner_id=$1 OR user_id=$1",
		"DELETE FROM blocked_users WHERE blocker_id=$1",
		"DELETE FROM account_prefs WHERE user_id=$1",
		"DELETE FROM drafts WHERE user_id=$1",
		"DELETE FROM scheduled_messages WHERE owner_id=$1",
		"DELETE FROM stories WHERE owner_id=$1",
		"DELETE FROM close_friends WHERE owner_id=$1 OR friend_id=$1",
		"DELETE FROM recent_emoji_statuses WHERE user_id=$1",
		"DELETE FROM channel_boosts WHERE user_id=$1",
		"DELETE FROM peer_verifications WHERE user_id=$1",
		"UPDATE payment_forms SET expires_at=0 WHERE user_id=$1 AND NOT paid",
		"UPDATE freeze_appeals SET status='rejected', decided_at=now() WHERE user_id=$1 AND status='open'",
	} {
		if _, err := tx.Exec(ctx, q, r.User); err != nil {
			return res, fmt.Errorf("erase: %s: %w", q, err)
		}
	}

	details, err := json.Marshal(map[string]any{"state": "deleted", "reason": trim(r.Reason), "erased": true, "result": res})
	if err != nil {
		return res, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO audit_log(actor_id, action, target_id, details) VALUES ($1, 'account_deleted', $2, $3)", r.Actor, r.User, details); err != nil {
		return res, err
	}
	return res, nil
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	for utf8.RuneCountInString(s) > 500 {
		s = string([]rune(s)[:500])
	}
	return s
}

func nonNil(v []int64) []int64 {
	if v == nil {
		return []int64{}
	}
	return v
}

// Service runs Erase in its own transaction and the inactivity job.
// Finish signs the deleted account's apps out and delivers chat updates;
// the API sets it (api.AccountErased).
type Service struct {
	DB       *pgxpool.Pool
	Settings *settings.Source
	Finish   func(Result)
	now      func() time.Time
}

func New(db *pgxpool.Pool, set *settings.Source) *Service {
	return &Service{DB: db, Settings: set, now: time.Now}
}

// Run erases one account in its own transaction, then finishes it.
func (s *Service) Run(ctx context.Context, r Request) (Result, error) {
	var res Result
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		res, err = Erase(ctx, tx, r, s.now())
		return err
	})
	if err == nil && s.Finish != nil {
		s.Finish(res)
	}
	return res, err
}

// inactiveSQL lists accounts away longer than their own TTL (default 365
// days), oldest first. Locked, banned and suspended accounts can't sign in,
// so their absence isn't theirs to choose and they are left alone, and so
// are administrators. 30 days is the shortest TTL there is, which lets the
// index narrow the scan before the per-account TTL is read.
const inactiveSQL = `SELECT u.id FROM users u LEFT JOIN account_prefs p ON p.user_id = u.id
	WHERE u.account_state IN ('active', 'frozen') AND NOT u.is_system AND NOT u.is_bot AND u.role <> 'admin'
		AND u.last_seen_at < $1::timestamptz - interval '30 days'
		AND u.last_seen_at < $1::timestamptz - make_interval(days => coalesce(p.account_ttl_days, 365))`

// Sweep deletes up to limit accounts that have been away longer than their
// TTL, each in its own transaction that checks again under the row lock
// (a sign-in in between saves it). Returns how many went.
func (s *Service) Sweep(ctx context.Context, limit int) (int, error) {
	now := s.now()
	rows, err := s.DB.Query(ctx, inactiveSQL+" ORDER BY u.last_seen_at, u.id LIMIT $2", now, limit)
	if err != nil {
		return 0, err
	}
	due, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range due {
		var res Result
		gone := false
		err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
			var still int64
			if err := tx.QueryRow(ctx, inactiveSQL+" AND u.id=$2 FOR UPDATE OF u", now, id).Scan(&still); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if still != id {
				return nil
			}
			var err error
			res, err = Erase(ctx, tx, Request{User: id, Source: SourceInactive, Reason: "Inactive longer than the account's self-destruct period"}, now)
			gone = err == nil
			return err
		})
		if err != nil {
			return n, err
		}
		if gone {
			n++
			slog.Info("erase: inactive account deleted", "user", id)
			if s.Finish != nil {
				s.Finish(res)
			}
		}
	}
	return n, nil
}

// Every runs Sweep on a ticker while the instance setting allows it.
func (s *Service) Every(ctx context.Context, d time.Duration) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !s.Settings.Get(ctx).InactiveAccountDeletion {
			continue
		}
		for {
			n, err := s.Sweep(ctx, 100)
			if err != nil {
				slog.Warn("erase: inactivity sweep", "err", err)
			}
			if err != nil || n < 100 {
				break
			}
		}
	}
}
