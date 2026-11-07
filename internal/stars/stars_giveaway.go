package stars

import (
	"context"
	"fmt"

	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
)

// The other prize of "get boosts via gifts": Stars instead of Premium, drawn
// at the end date like the Premium one (draw.go). These Stars come off the
// buyer's balance and land on the winners', nothing is created.

const (
	KindStarsGiveaway = "stars_giveaway"
	starsGiveawayDays = 365
)

var (
	giveawayStars = []int64{500, 1000, 2500, 5000, 10000, 25000, 50000, 100000}
	// All of these divide every amount above: no Star is left over, and the
	// app's winner slider stays valid when the amount changes (Web A doesn't
	// move it). Web A starts the slider on 5.
	giveawayWinners = []int{1, 2, 5, 10, 20, 50, 100}

	errStarsGiveawayInvalid = tgerr.New(400, "INVOICE_INVALID")
	errGiveawayAdmin        = tgerr.New(400, "CHAT_ADMIN_REQUIRED")
)

type StarsGiveawayOption struct {
	Stars   int64
	Boosts  int // for a year, in all
	Winners []int
}

// yearlyBoosts keeps a boost at the price of a yearly Premium gift, so this is
// not a cheaper way to a level. The smallest amount still gets one.
func yearlyBoosts(stars, annual int64) int {
	return max(int(stars*boostsPerGiveaway/annual), 1)
}

func (s *Service) StarsGiveawayOptions(ctx context.Context) []StarsGiveawayOption {
	annual := int64(s.settings.Get(ctx).PremiumAnnualStars)
	out := make([]StarsGiveawayOption, 0, len(giveawayStars))
	for _, n := range giveawayStars {
		out = append(out, StarsGiveawayOption{Stars: n, Boosts: yearlyBoosts(n, annual), Winners: giveawayWinners})
	}
	return out
}

// StarsGiveawayForm prices a giveaway of stars split over users winners. The
// numbers have to be one of the options; what the app said they cost is not
// looked at.
func (s *Service) StarsGiveawayForm(ctx context.Context, buyer, channel, stars int64, users int, opts GiveawayOptions) (Form, error) {
	var opt StarsGiveawayOption
	for _, o := range s.StarsGiveawayOptions(ctx) {
		if o.Stars == stars {
			opt = o
		}
	}
	valid := false
	for _, n := range opt.Winners {
		valid = valid || n == users
	}
	if !valid {
		return Form{}, errStarsGiveawayInvalid
	}
	if err := s.checkOptions(&opts, channel); err != nil {
		return Form{}, err
	}
	f, err := s.issue(ctx, Form{Kind: KindStarsGiveaway, User: buyer, Recipient: buyer, Days: starsGiveawayDays, Stars: stars})
	if err != nil {
		return f, err
	}
	if _, err = s.db.Exec(ctx, "UPDATE payment_forms SET channel_id=$2, quantity=$3, boosts=$4 WHERE id=$1", f.ID, channel, users, opt.Boosts); err != nil {
		return f, err
	}
	return f, s.saveOptions(ctx, f.ID, opts)
}

func starsGiveawayRef(formID int64) string { return fmt.Sprintf("stars_giveaway:%d", formID) }

// boostShare is winner i's part of boosts split over quantity rows. With fewer
// boosts than winners the last ones hold none.
func boostShare(boosts, quantity, i int) int {
	share := boosts / quantity
	if i < boosts%quantity {
		share++
	}
	return share
}

// PayStarsGiveaway launches a Stars giveaway: the Stars go to escrow and the
// boosts to the channel, for a year from now.
func (s *Service) PayStarsGiveaway(ctx context.Context, buyer, formID int64, post LaunchPost) (Launched, error) {
	var out Launched
	err := s.tx(ctx, func(tx pgx.Tx) error {
		f, err := s.claim(ctx, tx, buyer, formID, KindStarsGiveaway)
		if err != nil {
			return err
		}
		var channel int64
		var quantity, boosts int
		if err := tx.QueryRow(ctx, "SELECT channel_id, quantity, boosts FROM payment_forms WHERE id=$1", f.ID).Scan(&channel, &quantity, &boosts); err != nil {
			return err
		}
		if quantity < 1 || f.Stars%int64(quantity) != 0 {
			return errStarsGiveawayInvalid
		}
		opts, ok, err := s.formOptions(ctx, tx, f.ID)
		if err != nil {
			return err
		}
		if !ok {
			return errFormInvalid
		}
		g := Giveaway{FormID: f.ID, Kind: "stars", Buyer: buyer, Channel: channel, GiveawayOptions: opts, Quantity: quantity, Stars: f.Stars}
		if err := buyerAdmin(ctx, tx, buyer, g.Channels()); err != nil {
			return err
		}
		e := Entry{User: buyer, Amount: -f.Stars, Reason: "stars_giveaway", Reference: starsGiveawayRef(f.ID) + ":out",
			CreatedBy: buyer, Channel: channel, Title: "Stars giveaway"}
		out, err = s.launch(ctx, tx, g, e, boosts, 0, int64(s.unix())+starsGiveawayDays*86400, post)
		return err
	})
	return out, err
}
