package stars

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/settings"
)

// UnlimitedPremium is the premium_until admins set for "forever"
// (9999-12-31); nothing can be bought on top of it.
const UnlimitedPremium = 253402300799

type Plan struct {
	Months int
	Days   int
	Stars  int64
}

// Plans is wallet.js plans(): monthly, quarterly and annual from settings,
// half a year at two quarters less 1%, and long plans at the annual price
// less 2-6%.
func Plans(set settings.Settings) []Plan {
	ceilPct := func(base, times, pct int64) int64 { return (base*times*(100-pct) + 99) / 100 }
	out := []Plan{
		{1, 30, int64(set.PremiumMonthlyStars)},
		{3, 90, int64(set.PremiumQuarterlyStars)},
		{6, 180, ceilPct(int64(set.PremiumQuarterlyStars), 2, 1)},
		{12, 365, int64(set.PremiumAnnualStars)},
	}
	for _, p := range []struct{ months, discount int64 }{{24, 2}, {36, 3}, {60, 5}, {120, 6}} {
		years := p.months / 12
		out = append(out, Plan{int(p.months), int(years * 365), ceilPct(int64(set.PremiumAnnualStars), years, p.discount)})
	}
	return out
}

func (s *Service) Plans(ctx context.Context) []Plan { return Plans(s.settings.Get(ctx)) }

func (s *Service) plan(ctx context.Context, months int) (Plan, bool) {
	for _, p := range s.Plans(ctx) {
		if p.Months == months {
			return p, true
		}
	}
	return Plan{}, false
}

// Active is isPremium(): inside [premium_starts_at, premium_until).
func Active(until, startsAt, now int64) bool { return until > now && startsAt <= now }

var (
	errPlanInvalid    = tgerr.New(400, "PREMIUM_GIFT_CODE_INVALID")
	errUserInvalid    = tgerr.New(400, "USER_ID_INVALID")
	errUnlimited      = tgerr.New(400, "PREMIUM_CURRENTLY_UNLIMITED")
	errDurationTooBig = tgerr.New(400, "PREMIUM_DURATION_INVALID")
)

// person is an account that can receive things: active, not a bot or a
// system account.
func person(ctx context.Context, q querier, id int64) (premiumUntil int64, err error) {
	var disabled, system, bot bool
	err = q.QueryRow(ctx, "SELECT premium_until, disabled, is_system, is_bot FROM users WHERE id=$1", id).Scan(&premiumUntil, &disabled, &system, &bot)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (disabled || system || bot) {
		return 0, errUserInvalid
	}
	return premiumUntil, err
}

// PremiumForm prices a plan for recipient (the buyer for themselves).
func (s *Service) PremiumForm(ctx context.Context, buyer, recipient int64, months int, message string) (Form, error) {
	p, ok := s.plan(ctx, months)
	if !ok {
		return Form{}, errPlanInvalid
	}
	if utf8.RuneCountInString(message) > MaxGiftMessage {
		return Form{}, errGiftMessage
	}
	until, err := person(ctx, s.db, recipient)
	if err != nil {
		return Form{}, err
	}
	if until == UnlimitedPremium {
		return Form{}, errUnlimited
	}
	return s.issue(ctx, Form{Kind: KindPremium, User: buyer, Recipient: recipient, Months: p.Months, Days: p.Days, Stars: p.Stars, Message: message})
}

type PremiumResult struct {
	Form          Form
	Until         int64
	Balance       int64
	TransactionID string
}

// PayPremium is purchasePremium(): the price is the form's, the days stack
// on whatever Premium the recipient has left.
func (s *Service) PayPremium(ctx context.Context, buyer, formID int64) (PremiumResult, error) {
	var out PremiumResult
	err := s.tx(ctx, func(tx pgx.Tx) error {
		f, err := s.claim(ctx, tx, buyer, formID, KindPremium)
		if err != nil {
			return err
		}
		out.Form = f
		if err := Lock(ctx, tx, buyer, f.Recipient); err != nil {
			return err
		}
		until, err := person(ctx, tx, f.Recipient)
		if err != nil {
			return err
		}
		if until == UnlimitedPremium {
			return errUnlimited
		}
		now := int64(s.unix())
		out.Until = max(now, until) + int64(f.Days)*86400
		if out.Until >= UnlimitedPremium {
			return errDurationTooBig
		}
		e := Entry{User: buyer, Amount: -f.Stars, Reason: "premium_purchase", Reference: fmt.Sprintf("premium:%d:%d", buyer, f.ID),
			CreatedBy: buyer, Title: "Premium", Months: f.Months}
		if f.Recipient != buyer {
			e.Reason, e.Peer = "premium_gift", f.Recipient
		} else if e.Peer, err = Account(ctx, tx, PremiumAccount); err != nil {
			return err
		}
		if out.TransactionID, err = Post(ctx, tx, e, int(now)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE users SET premium_starts_at=least(premium_starts_at, $2), premium_until=$3 WHERE id=$1", f.Recipient, now, out.Until); err != nil {
			return err
		}
		out.Balance, err = Balance(ctx, tx, buyer)
		return err
	})
	return out, err
}
