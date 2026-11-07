package stars

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
)

// Txn is a ledger row as the transaction list shows it.
type Txn struct {
	ID            int64
	TransactionID string
	Amount        int64
	Reason        string
	Date          int
	Peer          int64
	Title         string
	Note          string
	Months        int
	GiftID        int64
	Channel       int64 // a giveaway's channel
	Post          int   // and its message there
}

type HistoryQuery struct {
	Inbound, Outbound bool
	Offset            string // the last row id of the previous page
	Limit             int
}

// History is starsHistory(), newest first, in pages of up to 100 (the web
// showed the last 100 and nothing more).
func (s *Service) History(ctx context.Context, user int64, q HistoryQuery) ([]Txn, string, error) {
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	before := int64(1<<63 - 1)
	if q.Offset != "" {
		n, err := strconv.ParseInt(q.Offset, 10, 64)
		if err != nil || n <= 0 {
			return nil, "", tgerr.New(400, "OFFSET_INVALID")
		}
		before = n
	}
	where := "user_id=$1 AND id < $2"
	if q.Inbound {
		where += " AND amount > 0"
	}
	if q.Outbound {
		where += " AND amount < 0"
	}
	rows, err := s.db.Query(ctx, `SELECT id, transaction_id, amount, reason, created_at, coalesce(peer_id, 0), title, note, months, coalesce(gift_id, 0), coalesce(channel_id, 0), post_id
		FROM star_ledger WHERE `+where+" ORDER BY id DESC LIMIT $3", user, before, limit+1)
	if err != nil {
		return nil, "", err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Txn, error) {
		var t Txn
		err := r.Scan(&t.ID, &t.TransactionID, &t.Amount, &t.Reason, &t.Date, &t.Peer, &t.Title, &t.Note, &t.Months, &t.GiftID, &t.Channel, &t.Post)
		return t, err
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(list) > limit {
		list = list[:limit]
		next = strconv.FormatInt(list[limit-1].ID, 10)
	}
	return list, next, nil
}

type Rating struct {
	Level   int
	Stars   int64
	Current int64
	Next    int64 // 0 at the top level
}

// ProfileRating is profileRating(), as Telegram's Rating sheet explains it:
// Stars spent on gifts from the instance count in full, and a gift the user
// bought that someone cashed in takes back 85% of what it cost. Cashing in a
// gift someone else bought costs the one who cashed it nothing, and giveaway
// refunds and market sales aren't refunds. There are no refunds of payments
// to take 10x off, so this never goes below 0.
func (s *Service) ProfileRating(ctx context.Context, user int64) (Rating, error) {
	var r Rating
	err := s.db.QueryRow(ctx, `SELECT
		(SELECT coalesce(sum(-amount), 0) FROM star_ledger WHERE user_id=$1 AND amount < 0
			AND reason IN ('gift_purchase', 'premium_gift', 'gift_upgrade', 'gift_market')) -
		(SELECT coalesce(sum(cost), 0) * 85 / 100 FROM gift_receipts WHERE sender_id=$1 AND converted AND cost > 0)`, user).Scan(&r.Stars)
	if err != nil {
		return r, err
	}
	r.Stars = max(r.Stars, 0)
	r.Level, r.Current, r.Next = ratingLevel(r.Stars)
	return r, nil
}

// ratingAnchors are the points Telegram levels are known to start at; its docs
// only say levels start at "certain thresholds". gramgpt.io/en/blog/telegram-rating
// lists these (level 2 is confirmed by the app's Rating window, the rest
// are public breakdowns), and the level 75 they imply matches the 934,000
// durovscode.com reports. Levels in between are interpolated, past 80 at
// the same slope, up to the 99 the app has badges for.
var ratingAnchors = []struct {
	level int
	stars int64
}{{1, 1}, {2, 5000}, {3, 12000}, {4, 19000}, {5, 27000}, {10, 81000}, {20, 212000}, {50, 606000}, {80, 1000000}, {ratingLevelMax, 1250000}}

const ratingLevelMax = 99

func ratingLevelStars(level int) int64 {
	for i := 1; i < len(ratingAnchors); i++ {
		a, b := ratingAnchors[i-1], ratingAnchors[i]
		if level <= b.level {
			return a.stars + (b.stars-a.stars)*int64(level-a.level)/int64(b.level-a.level)
		}
	}
	return ratingAnchors[len(ratingAnchors)-1].stars
}

// ratingLevel gives the level for a score and the points it started at and
// the next one starts at (0: no next). Below zero there is one level, -1: the
// app only says a negative rating is bad, not how the depths divide.
func ratingLevel(stars int64) (level int, current, next int64) {
	if stars < 0 {
		return -1, stars, 0
	}
	for level < ratingLevelMax && ratingLevelStars(level+1) <= stars {
		level++
	}
	if level > 0 {
		current = ratingLevelStars(level)
	}
	if level < ratingLevelMax {
		next = ratingLevelStars(level + 1)
	}
	return level, current, next
}

// StarsGiftForm is quoteStarsGift(): Stars from the buyer's balance to
// someone else, up to a billion at a time.
func (s *Service) StarsGiftForm(ctx context.Context, buyer, recipient, amount int64) (Form, error) {
	if recipient == buyer || amount < 1 || amount > 1000000000 {
		return Form{}, tgerr.New(400, "STARS_AMOUNT_INVALID")
	}
	if _, err := person(ctx, s.db, recipient); err != nil {
		return Form{}, err
	}
	return s.issue(ctx, Form{Kind: KindStarsGift, User: buyer, Recipient: recipient, Stars: amount})
}

type TransferResult struct {
	Form          Form
	Balance       int64
	TransactionID string // the recipient's side
}

// PayStarsGift is purchaseStarsGift(): a transfer_out/transfer_in pair
// under one reference stem, as wallet.js transfer() wrote it.
func (s *Service) PayStarsGift(ctx context.Context, buyer, formID int64) (TransferResult, error) {
	var out TransferResult
	err := s.tx(ctx, func(tx pgx.Tx) error {
		f, err := s.claim(ctx, tx, buyer, formID, KindStarsGift)
		if err != nil {
			return err
		}
		out.Form = f
		if err := Lock(ctx, tx, buyer, f.Recipient); err != nil {
			return err
		}
		if _, err := person(ctx, tx, f.Recipient); err != nil {
			return err
		}
		now := s.unix()
		ref := fmt.Sprintf("transfer:%d:stars_gift_%d", buyer, f.ID)
		if _, err := Post(ctx, tx, Entry{User: buyer, Amount: -f.Stars, Reason: "transfer_out", Reference: ref + ":out",
			CreatedBy: buyer, Peer: f.Recipient, Title: "Stars gift"}, now); err != nil {
			return err
		}
		if out.TransactionID, err = Post(ctx, tx, Entry{User: f.Recipient, Amount: f.Stars, Reason: "transfer_in", Reference: ref + ":in",
			CreatedBy: buyer, Peer: buyer, Title: "Stars gift"}, now); err != nil {
			return err
		}
		out.Balance, err = Balance(ctx, tx, buyer)
		return err
	})
	return out, err
}

// Tip moves Stars between two people for a star reaction and runs record in
// the same transaction. Ledger references make a retried request (same random
// id from the app) a no-op, reported as ErrDuplicate.
func (s *Service) Tip(ctx context.Context, from, to, amount int64, ref, title string, record func(pgx.Tx) error) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		if err := Lock(ctx, tx, from, to); err != nil {
			return err
		}
		if _, err := person(ctx, tx, to); err != nil {
			return err
		}
		now := s.unix()
		if _, err := Post(ctx, tx, Entry{User: from, Amount: -amount, Reason: "transfer_out", Reference: ref + ":out",
			CreatedBy: from, Peer: to, Title: title}, now); err != nil {
			return err
		}
		if _, err := Post(ctx, tx, Entry{User: to, Amount: amount, Reason: "transfer_in", Reference: ref + ":in",
			CreatedBy: from, Peer: from, Title: title}, now); err != nil {
			return err
		}
		return record(tx)
	})
}

// Adjust is makeStarsTransaction(): an admin issues or takes back Stars.
// requestID makes a retry return the first result; reusing it for a
// different adjustment is refused. Refusals are *account.Error with the
// web's wording, for the admin panel.
func (s *Service) Adjust(ctx context.Context, admin, recipient, amount int64, note, requestID string) (string, int64, error) {
	limit := int64(s.settings.Get(ctx).StarsAdjustmentLimit)
	if amount == 0 || amount > limit || -amount > limit || len([]rune(note)) > 256 || !requestRe.MatchString(requestID) {
		return "", 0, adjustFail(400, "Choose a recipient, a nonzero Stars amount, a reason up to 256 characters and a unique request ID.")
	}
	ref := fmt.Sprintf("admin:%d:%s", admin, requestID)
	var id string
	var bal int64
	// a retry with the same request id waits on the first one's lock and
	// then finds its row, so both answer with the same transaction
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := Lock(ctx, tx, recipient); err != nil {
			return err
		}
		var prevUser, prevAmount int64
		var prevNote string
		err := tx.QueryRow(ctx, "SELECT user_id, amount, note, transaction_id FROM star_ledger WHERE reference=$1", ref).Scan(&prevUser, &prevAmount, &prevNote, &id)
		if err == nil {
			if prevUser != recipient || prevAmount != amount || prevNote != note {
				return adjustFail(409, "This request ID belongs to another transaction.")
			}
			bal, err = Balance(ctx, tx, recipient)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := person(ctx, tx, recipient); err != nil {
			return adjustFail(400, "Choose an active person as the recipient.")
		}
		issuer, err := Account(ctx, tx, AdminAccount)
		if err != nil {
			return err
		}
		reason := "stars_issued"
		if amount < 0 {
			reason = "stars_reversed"
		}
		id, err = Post(ctx, tx, Entry{User: recipient, Amount: amount, Reason: reason, Reference: ref, CreatedBy: admin, Peer: issuer, Title: "Admin", Note: note}, s.unix())
		switch {
		case errors.Is(err, ErrBalanceTooLow), errors.Is(err, ErrBalanceTooHigh):
			return adjustFail(400, "The resulting Stars balance is invalid.")
		case errors.Is(err, ErrDuplicate):
			// same request id under another recipient's lock
			return adjustFail(409, "This request ID belongs to another transaction.")
		case err != nil:
			return err
		}
		details, _ := json.Marshal(map[string]any{"transactionId": id, "amount": amount, "note": note})
		if _, err := tx.Exec(ctx, "INSERT INTO audit_log(actor_id, action, target_id, details) VALUES ($1, 'stars_transaction', $2, $3)", admin, recipient, details); err != nil {
			return err
		}
		bal, err = Balance(ctx, tx, recipient)
		return err
	})
	return id, bal, err
}

func adjustFail(status int, msg string) error { return &account.Error{Status: status, Message: msg} }

var requestRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,100}$`)
