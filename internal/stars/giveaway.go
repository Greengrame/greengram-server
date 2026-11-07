package stars

import (
	"context"
	"fmt"

	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
)

// "Get boosts via gifts": a channel's admin pays for Premium for some of its
// members, chosen or drawn at random, and the channel gets four boosts per
// gift for as long as the gift lasts (giveaway_boosts_per_premium,
// core.telegram.org/api/config). Chosen people get theirs at payment; a draw
// happens at the giveaway's end date (draw.go).

const (
	KindGiveaway      = "giveaway"
	boostsPerGiveaway = 4
	maxGiveawayUsers  = 100
)

var errNotEnoughMembers = tgerr.New(400, "GIVEAWAY_NOT_ENOUGH_MEMBERS")

// GiveawayForm prices months of Premium for users people. list names them;
// empty, it's a giveaway with opts, drawn at opts.Until.
func (s *Service) GiveawayForm(ctx context.Context, buyer, channel int64, months, users int, list []int64, opts GiveawayOptions) (Form, error) {
	p, ok := s.plan(ctx, months)
	if !ok || users < 1 || users > maxGiveawayUsers || len(list) != 0 && len(list) != users {
		return Form{}, errPlanInvalid
	}
	if len(list) == 0 {
		if err := s.checkOptions(&opts, channel); err != nil {
			return Form{}, err
		}
	}
	f, err := s.issue(ctx, Form{Kind: KindGiveaway, User: buyer, Recipient: buyer, Months: p.Months, Days: p.Days, Stars: p.Stars * int64(users)})
	if err != nil {
		return f, err
	}
	if _, err = s.db.Exec(ctx, "UPDATE payment_forms SET channel_id=$2, quantity=$3, winners=$4 WHERE id=$1", f.ID, channel, users, list); err != nil {
		return f, err
	}
	if len(list) == 0 {
		err = s.saveOptions(ctx, f.ID, opts)
	}
	return f, err
}

type GiveawayResult struct {
	Form    Form
	Channel int64
	Winners []int64          // chosen people, who have it now
	Slugs   map[int64]string // their gift codes
	Balance int64

	Launched *Launched // a giveaway: drawn later
}

// buyerAdmin is whether buyer still runs every channel: the form may be older
// than a demotion, and paying posts in the channel.
func buyerAdmin(ctx context.Context, tx pgx.Tx, buyer int64, chans []int64) error {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM channel_members m JOIN channels c ON c.id = m.channel_id
		WHERE m.user_id=$1 AND m.channel_id = ANY($2) AND m.status IN ('admin', 'creator') AND NOT c.deleted`, buyer, chans).Scan(&n)
	if err != nil {
		return err
	}
	if n != len(chans) {
		return errGiveawayAdmin
	}
	return nil
}

// PayGiveaway pays a Premium form. Chosen people get Premium and the
// channel its boosts in this transaction; a giveaway is launched instead
// (launch), with post putting it in the channel.
func (s *Service) PayGiveaway(ctx context.Context, buyer, formID int64, post LaunchPost) (GiveawayResult, error) {
	var out GiveawayResult
	err := s.tx(ctx, func(tx pgx.Tx) error {
		f, err := s.claim(ctx, tx, buyer, formID, KindGiveaway)
		if err != nil {
			return err
		}
		out.Form = f
		var quantity int
		var chosen []int64
		if err := tx.QueryRow(ctx, "SELECT channel_id, quantity, coalesce(winners, '{}') FROM payment_forms WHERE id=$1", f.ID).Scan(&out.Channel, &quantity, &chosen); err != nil {
			return err
		}
		ref := fmt.Sprintf("giveaway:%d:%d", buyer, f.ID)
		if len(chosen) == 0 {
			opts, ok, err := s.formOptions(ctx, tx, f.ID)
			if err != nil {
				return err
			}
			if !ok || quantity < 1 {
				return errFormInvalid
			}
			g := Giveaway{FormID: f.ID, Kind: "premium", Buyer: buyer, Channel: out.Channel, GiveawayOptions: opts, Quantity: quantity,
				Months: f.Months, Days: f.Days, Stars: f.Stars}
			if err := buyerAdmin(ctx, tx, buyer, g.Channels()); err != nil {
				return err
			}
			e := Entry{User: buyer, Amount: -f.Stars, Reason: "premium_giveaway", Reference: ref, CreatedBy: buyer, Title: "Premium", Months: f.Months}
			if e.Peer, err = Account(ctx, tx, PremiumAccount); err != nil {
				return err
			}
			// the boosts last through the run and the Premium after it; the
			// draw restarts them from the day it's given
			l, err := s.launch(ctx, tx, g, e, 0, boostsPerGiveaway, int64(opts.Until)+int64(f.Days)*86400, post)
			if err != nil {
				return err
			}
			out.Launched, out.Balance = &l, l.Balance
			return nil
		}
		out.Winners = chosen
		if err := Lock(ctx, tx, append([]int64{buyer}, out.Winners...)...); err != nil {
			return err
		}
		now := int64(s.unix())
		e := Entry{User: buyer, Amount: -f.Stars, Reason: "premium_giveaway", Reference: ref, CreatedBy: buyer, Title: "Premium", Months: f.Months}
		if e.Peer, err = Account(ctx, tx, PremiumAccount); err != nil {
			return err
		}
		if _, err := Post(ctx, tx, e, int(now)); err != nil {
			return err
		}
		out.Slugs = map[int64]string{}
		for _, w := range out.Winners {
			until, err := person(ctx, tx, w)
			if err != nil {
				return err
			}
			if until == UnlimitedPremium {
				return errUnlimited
			}
			next := max(now, until) + int64(f.Days)*86400
			if next >= UnlimitedPremium {
				return errDurationTooBig
			}
			if _, err := tx.Exec(ctx, "UPDATE users SET premium_starts_at=least(premium_starts_at, $2), premium_until=$3 WHERE id=$1", w, now, next); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "INSERT INTO channel_gift_boosts(channel_id, user_id, assigned_at, expires_at, drawn, multiplier) VALUES ($1, $2, $3, $4, false, $5)",
				out.Channel, w, now, now+int64(f.Days)*86400, boostsPerGiveaway); err != nil {
				return err
			}
			slug := newSlug()
			if _, err := tx.Exec(ctx, "INSERT INTO gift_codes(slug, user_id, channel_id, days, created_at) VALUES ($1, $2, $3, $4, $5)",
				slug, w, out.Channel, f.Days, now); err != nil {
				return err
			}
			out.Slugs[w] = slug
		}
		out.Balance, err = Balance(ctx, tx, buyer)
		return err
	})
	return out, err
}

// GiftBoost is what a channel holds for one winner of what it gave away.
type GiftBoost struct {
	ID         int64
	User       int64 // 0 while its giveaway runs
	Date       int
	Expires    int64
	Drawn      bool
	Multiplier int
	Stars      int64 // the prize, for a Stars giveaway
	Post       int   // the giveaway's post, for one with a draw
}

func (s *Service) channelGiftBoosts(ctx context.Context, channel int64) ([]GiftBoost, error) {
	rows, err := s.db.Query(ctx, `SELECT b.id, coalesce(b.user_id, 0), b.assigned_at, b.expires_at, b.drawn, b.multiplier, b.stars, coalesce(g.post_id, 0)
		FROM channel_gift_boosts b LEFT JOIN giveaways g ON g.id = b.giveaway_id
		WHERE b.channel_id=$1 AND b.expires_at>$2 ORDER BY b.assigned_at DESC, b.user_id NULLS LAST, b.id`, channel, s.unix())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (GiftBoost, error) {
		var b GiftBoost
		err := r.Scan(&b.ID, &b.User, &b.Date, &b.Expires, &b.Drawn, &b.Multiplier, &b.Stars, &b.Post)
		return b, err
	})
}
