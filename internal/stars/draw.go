package stars

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tgerr"
	"github.com/jackc/pgx/v5"
)

// A giveaway with a draw runs until its end date, as on Telegram
// (core.telegram.org/api/giveaways): paying takes the price off the buyer
// and holds it on the escrow account, the channel gets the giveaway post and
// its boosts, and at until_date the draw picks among who is in every
// channel then. Prizes and refunds come off escrow, so no Star is made or
// lost on the way; a Premium prize is spent the way any Premium purchase is.

const (
	giveawayPeriodMax   = 31 * 86400 // giveaway_period_max
	giveawayAddPeersMax = 10         // giveaway_add_peers_max
	giveawayCountryMax  = 10         // giveaway_countries_max
	giveawayPrizeMax    = 128
)

var (
	EscrowAccount = System{"giveaway_escrow", "instance_giveaways", "Giveaways"}

	errUntilDate     = tgerr.New(400, "GIVEAWAY_UNTIL_DATE_INVALID")
	errCountries     = tgerr.New(400, "COUNTRIES_ISO2_INVALID")
	errAddPeers      = tgerr.New(400, "ADDITIONAL_PEERS_INVALID")
	errPrizeTooLong  = tgerr.New(400, "PRIZE_DESCRIPTION_TOO_LONG")
	errGiftCodeEmpty = tgerr.New(400, "GIFT_SLUG_INVALID")
	iso2Re           = regexp.MustCompile(`^[A-Z]{2}$`)
)

// GiveawayOptions are what the buyer picked besides the prize.
type GiveawayOptions struct {
	Additional     []int64  `json:"additional,omitempty"`
	Countries      []string `json:"countries,omitempty"`
	OnlyNew        bool     `json:"onlyNew,omitempty"`
	WinnersVisible bool     `json:"winnersVisible,omitempty"`
	Prize          string   `json:"prize,omitempty"`
	Until          int      `json:"until"`
}

// check normalizes o for a giveaway in channel. Whether the buyer may use the
// additional channels is the caller's to check.
func (s *Service) checkOptions(o *GiveawayOptions, channel int64) error {
	now := s.unix()
	if o.Until <= now || o.Until > now+giveawayPeriodMax {
		return errUntilDate
	}
	seen := map[string]bool{}
	var countries []string
	for _, c := range o.Countries {
		c = strings.ToUpper(c)
		if !iso2Re.MatchString(c) {
			return errCountries
		}
		if !seen[c] {
			seen[c] = true
			countries = append(countries, c)
		}
	}
	if len(countries) > giveawayCountryMax {
		return errCountries
	}
	o.Countries = countries
	var add []int64
	for _, c := range o.Additional {
		if c == channel {
			return errAddPeers
		}
		if !slices.Contains(add, c) {
			add = append(add, c)
		}
	}
	if len(add) > giveawayAddPeersMax {
		return errAddPeers
	}
	o.Additional = add
	o.Prize = strings.TrimSpace(o.Prize)
	if utf8.RuneCountInString(o.Prize) > giveawayPrizeMax {
		return errPrizeTooLong
	}
	return nil
}

// Giveaway is one giveaway with a draw.
type Giveaway struct {
	ID      int64
	FormID  int64
	Kind    string // "premium" or "stars"
	Buyer   int64
	Channel int64
	GiveawayOptions
	Quantity int
	Months   int
	Days     int
	Stars    int64 // what is held: the whole price
	Started  int

	LaunchID, PostID int
	State            string // running, drawn, refunded
	DrawnAt          int
	Refunded         int64
	ResultsID        int
	ResultsPostID    int
	Winners          int
}

// PerWinner is what each winner gets: Stars, or the Premium price of one gift.
func (g Giveaway) PerWinner() int64 { return g.Stars / int64(g.Quantity) }

// Channels is the boost channel and then the additional ones.
func (g Giveaway) Channels() []int64 { return append([]int64{g.Channel}, g.Additional...) }

const giveawayCols = `g.id, g.form_id, g.kind, g.buyer_id, g.channel_id, g.additional, g.countries, g.only_new, g.winners_visible, g.prize_description,
	g.quantity, g.months, g.days, g.stars, g.started_at, g.until_date, g.launch_id, g.post_id, g.state, g.drawn_at, g.refunded_stars,
	g.results_id, g.results_post_id, (SELECT count(*) FROM giveaway_winners w WHERE w.giveaway_id = g.id)`

func scanGiveaway(row pgx.Row) (Giveaway, error) {
	var g Giveaway
	err := row.Scan(&g.ID, &g.FormID, &g.Kind, &g.Buyer, &g.Channel, &g.Additional, &g.Countries, &g.OnlyNew, &g.WinnersVisible, &g.Prize,
		&g.Quantity, &g.Months, &g.Days, &g.Stars, &g.Started, &g.Until, &g.LaunchID, &g.PostID, &g.State, &g.DrawnAt, &g.Refunded,
		&g.ResultsID, &g.ResultsPostID, &g.Winners)
	return g, err
}

// LaunchPost posts a giveaway in its channel inside the payment's
// transaction and returns the launch message's id and the post's.
type LaunchPost func(ctx context.Context, tx pgx.Tx, g Giveaway) (launch, post int, err error)

type Launched struct {
	Giveaway Giveaway
	Balance  int64
}

func (s *Service) formOptions(ctx context.Context, tx pgx.Tx, form int64) (GiveawayOptions, bool, error) {
	var raw []byte
	var o GiveawayOptions
	if err := tx.QueryRow(ctx, "SELECT giveaway FROM payment_forms WHERE id=$1", form).Scan(&raw); err != nil {
		return o, false, err
	}
	if raw == nil {
		return o, false, nil
	}
	return o, true, json.Unmarshal(raw, &o)
}

func (s *Service) saveOptions(ctx context.Context, form int64, o GiveawayOptions) error {
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, "UPDATE payment_forms SET giveaway=$2 WHERE id=$1", form, raw)
	return err
}

// launch takes the price into escrow, books the boosts with nobody on them
// yet and has post put the giveaway in the channel; all of it or nothing.
// boosts are the giveaway's in all, split over quantity rows by boostShare;
// Premium ones are perPremium each instead.
func (s *Service) launch(ctx context.Context, tx pgx.Tx, g Giveaway, buyerEntry Entry, boosts, perPremium int, expires int64, post LaunchPost) (Launched, error) {
	var out Launched
	escrow, err := Account(ctx, tx, EscrowAccount)
	if err != nil {
		return out, err
	}
	if err := Lock(ctx, tx, g.Buyer, escrow); err != nil {
		return out, err
	}
	now := s.unix()
	g.Started = now
	g.State = "running"
	if _, err := Post(ctx, tx, buyerEntry, now); err != nil {
		return out, err
	}
	hold := Entry{User: escrow, Amount: g.Stars, Reason: "giveaway_hold", Reference: fmt.Sprintf("giveaway:%d:hold", g.FormID), CreatedBy: g.Buyer,
		Channel: g.Channel, Title: "Giveaway"}
	if _, err := Post(ctx, tx, hold, now); err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO giveaways(form_id, kind, buyer_id, channel_id, additional, countries, only_new, winners_visible, prize_description,
			quantity, months, days, stars, started_at, until_date)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) RETURNING id`,
		g.FormID, g.Kind, g.Buyer, g.Channel, nonNil(g.Additional), nonNilS(g.Countries), g.OnlyNew, g.WinnersVisible, g.Prize,
		g.Quantity, g.Months, g.Days, g.Stars, now, g.Until).Scan(&g.ID)
	if err != nil {
		return out, err
	}
	for i := range g.Quantity {
		share := perPremium
		if perPremium == 0 {
			share = boostShare(boosts, g.Quantity, i)
		}
		if share == 0 {
			continue
		}
		var stars int64
		if g.Kind == "stars" {
			stars = g.PerWinner()
		}
		if _, err := tx.Exec(ctx, `INSERT INTO channel_gift_boosts(channel_id, user_id, assigned_at, expires_at, drawn, multiplier, stars, giveaway_id)
			VALUES ($1, NULL, $2, $3, true, $4, $5, $6)`, g.Channel, now, expires, share, stars, g.ID); err != nil {
			return out, err
		}
	}
	if g.LaunchID, g.PostID, err = post(ctx, tx, g); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, "UPDATE giveaways SET launch_id=$2, post_id=$3 WHERE id=$1", g.ID, g.LaunchID, g.PostID); err != nil {
		return out, err
	}
	// the buyer's row points at the post, like a prize's does
	if _, err := tx.Exec(ctx, "UPDATE star_ledger SET post_id=$2 WHERE reference=$1", buyerEntry.Reference, g.PostID); err != nil {
		return out, err
	}
	out.Giveaway = g
	out.Balance, err = Balance(ctx, tx, g.Buyer)
	return out, err
}

func nonNil(v []int64) []int64 {
	if v == nil {
		return []int64{}
	}
	return v
}

func nonNilS(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// CountryFilter keeps the users whose country, as far as the server knows
// it, is one of allowed.
type CountryFilter func(ctx context.Context, users []int64, allowed []string) ([]int64, error)

// poolSQL is everyone a giveaway may draw right now: in the boost channel and
// every additional one that still exists, not an admin of any of them, not
// the buyer, a bot or a system account. $1 channel, $2 buyer, $3 only new,
// $4 started, $5 premium prize (the days, 0 for Stars), $6 additional, $7 now.
const poolSQL = `SELECT m.user_id FROM channel_members m JOIN users u ON u.id = m.user_id
	WHERE m.channel_id = $1 AND m.status IN ('member', 'restricted') AND m.user_id <> $2
		AND NOT u.disabled AND NOT u.is_bot AND NOT u.is_system
		AND (NOT $3::bool OR m.date >= $4)
		AND ($5::int = 0 OR greatest(u.premium_until, $7::bigint) + $5::bigint * 86400 < ` + "253402300799" + `)
		AND NOT EXISTS (SELECT 1 FROM unnest($6::bigint[]) c(id) JOIN channels ch ON ch.id = c.id AND NOT ch.deleted
			WHERE NOT EXISTS (SELECT 1 FROM channel_members a WHERE a.channel_id = c.id AND a.user_id = m.user_id
				AND a.status IN ('member', 'restricted') AND (NOT $3::bool OR a.date >= $4)))`

func (g Giveaway) poolArgs(now int) []any {
	days := 0
	if g.Kind == "premium" {
		days = g.Days
	}
	return []any{g.Channel, g.Buyer, g.OnlyNew, g.Started, days, nonNil(g.Additional), int64(now)}
}

// pick draws up to Quantity winners in random order.
func (s *Service) pick(ctx context.Context, tx pgx.Tx, g Giveaway, now int) ([]int64, error) {
	rows, err := tx.Query(ctx, poolSQL+" ORDER BY random()", g.poolArgs(now)...)
	if err != nil {
		return nil, err
	}
	pool, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	if len(g.Countries) == 0 {
		return pool[:min(len(pool), g.Quantity)], nil
	}
	if s.Countries == nil {
		return nil, nil
	}
	var out []int64
	for len(pool) > 0 && len(out) < g.Quantity {
		batch := pool[:min(len(pool), 200)]
		pool = pool[len(batch):]
		ok, err := s.Countries(ctx, batch, g.Countries)
		if err != nil {
			return nil, err
		}
		// keep the draw's order, not the filter's
		for _, u := range batch {
			if slices.Contains(ok, u) && len(out) < g.Quantity {
				out = append(out, u)
			}
		}
	}
	return out, nil
}

// DrawPosts posts a drawn giveaway's results in its channel inside the
// draw's transaction: the results service message, and the winners' list
// when they're visible.
type DrawPosts func(ctx context.Context, tx pgx.Tx, g Giveaway, winners []int64) (results, resultsPost int, err error)

// Drawn is a giveaway the draw just settled.
type Drawn struct {
	Giveaway Giveaway
	Winners  []int64
	Gone     bool // the channel was deleted: all refunded, nothing posted
}

// DrawDue draws every running giveaway whose end date has passed, one
// transaction each. A giveaway another worker is drawing is skipped, and a
// drawn one is no longer running, so nothing is drawn twice.
func (s *Service) DrawDue(ctx context.Context, posts DrawPosts) ([]Drawn, error) {
	var out []Drawn
	var failed []int64
	var errs []error
	for {
		d, id, err := s.drawOne(ctx, posts, failed)
		if err != nil {
			// the rest still get drawn; this one is tried again next round
			if id == 0 || ctx.Err() != nil {
				return out, errors.Join(append(errs, err)...)
			}
			failed = append(failed, id)
			errs = append(errs, fmt.Errorf("giveaway %d: %w", id, err))
			continue
		}
		if id == 0 {
			return out, errors.Join(errs...)
		}
		out = append(out, d)
	}
}

// drawOne draws the next due giveaway not in skip and returns its id, 0
// when there is none.
func (s *Service) drawOne(ctx context.Context, posts DrawPosts, skip []int64) (Drawn, int64, error) {
	var d Drawn
	var found int64
	err := s.tx(ctx, func(tx pgx.Tx) error {
		now := s.unix()
		g, err := scanGiveaway(tx.QueryRow(ctx, "SELECT "+giveawayCols+` FROM giveaways g WHERE g.state = 'running' AND g.until_date <= $1
			AND NOT (g.id = ANY($2)) ORDER BY g.until_date, g.id LIMIT 1 FOR UPDATE SKIP LOCKED`, now, nonNil(skip)))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = g.ID
		var gone bool
		if err := tx.QueryRow(ctx, "SELECT coalesce((SELECT deleted FROM channels WHERE id=$1), true)", g.Channel).Scan(&gone); err != nil {
			return err
		}
		var winners []int64
		if !gone {
			if winners, err = s.pick(ctx, tx, g, now); err != nil {
				return err
			}
		}
		escrow, err := Account(ctx, tx, EscrowAccount)
		if err != nil {
			return err
		}
		// a buyer who deleted their account meanwhile: what they'd get back
		// goes where the rest of their Stars went
		var buyerGone bool
		if err := tx.QueryRow(ctx, "SELECT account_state = 'deleted' FROM users WHERE id=$1", g.Buyer).Scan(&buyerGone); err != nil {
			return err
		}
		var erased int64
		locks := append([]int64{g.Buyer, escrow}, winners...)
		if buyerGone {
			if erased, err = Account(ctx, tx, ErasedAccount); err != nil {
				return err
			}
			locks = append(locks, erased)
		}
		if err := Lock(ctx, tx, locks...); err != nil {
			return err
		}
		release := Entry{User: escrow, Amount: -g.Stars, Reason: "giveaway_release", Reference: fmt.Sprintf("giveaway:%d:release", g.FormID),
			CreatedBy: escrow, Title: "Giveaway"}
		if !gone {
			release.Channel = g.Channel
		}
		if _, err := Post(ctx, tx, release, now); err != nil {
			return err
		}
		prize := g.PerWinner()
		for _, w := range winners {
			var txn string
			if g.Kind == "stars" {
				e := Entry{User: w, Amount: prize, Reason: "stars_giveaway_prize", Reference: fmt.Sprintf("%s:%d", starsGiveawayRef(g.FormID), w),
					CreatedBy: escrow, Channel: g.Channel, Title: "Stars giveaway"}
				if txn, err = Post(ctx, tx, e, now); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, "UPDATE star_ledger SET post_id=$2 WHERE transaction_id=$1", txn, g.PostID); err != nil {
					return err
				}
			} else {
				until, err := person(ctx, tx, w)
				if err != nil {
					return err
				}
				next := max(int64(now), until) + int64(g.Days)*86400
				if _, err := tx.Exec(ctx, "UPDATE users SET premium_starts_at=least(premium_starts_at, $2), premium_until=$3 WHERE id=$1", w, now, next); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, "INSERT INTO gift_codes(slug, user_id, channel_id, giveaway_id, days, created_at) VALUES ($1, $2, $3, $4, $5, $6)",
					newSlug(), w, g.Channel, g.ID, g.Days, now); err != nil {
					return err
				}
			}
			won := int64(0)
			if g.Kind == "stars" {
				won = prize
			}
			if _, err := tx.Exec(ctx, "INSERT INTO giveaway_winners(giveaway_id, user_id, stars, transaction_id) VALUES ($1, $2, $3, $4)",
				g.ID, w, won, txn); err != nil {
				return err
			}
		}
		rest := g.Stars - prize*int64(len(winners))
		if rest > 0 {
			// a buyer who deleted their account meanwhile: what they'd get
			// back goes where the rest of their Stars went
			to := g.Buyer
			if erased != 0 {
				to = erased
			}
			refund := Entry{User: to, Amount: rest, Reason: "giveaway_refund", Reference: fmt.Sprintf("giveaway:%d:refund", g.FormID),
				CreatedBy: escrow, Title: "Giveaway refund"}
			if !gone {
				refund.Channel = g.Channel
			}
			if _, err := Post(ctx, tx, refund, now); err != nil {
				return err
			}
		}
		if err := s.settleBoosts(ctx, tx, g, winners, now); err != nil {
			return err
		}
		g.State, g.DrawnAt, g.Refunded, g.Winners = "drawn", now, rest, len(winners)
		if len(winners) == 0 {
			g.State = "refunded"
		}
		if !gone {
			if g.ResultsID, g.ResultsPostID, err = posts(ctx, tx, g, winners); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, "UPDATE giveaways SET state=$2, drawn_at=$3, refunded_stars=$4, results_id=$5, results_post_id=$6 WHERE id=$1",
			g.ID, g.State, now, rest, g.ResultsID, g.ResultsPostID); err != nil {
			return err
		}
		d = Drawn{Giveaway: g, Winners: winners, Gone: gone}
		return nil
	})
	return d, found, err
}

// settleBoosts puts the winners on the boosts booked at payment, in order,
// and drops the ones nobody won: those Stars went back.
func (s *Service) settleBoosts(ctx context.Context, tx pgx.Tx, g Giveaway, winners []int64, now int) error {
	rows, err := tx.Query(ctx, "SELECT id FROM channel_gift_boosts WHERE giveaway_id=$1 AND user_id IS NULL ORDER BY id", g.ID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	for i, id := range ids {
		if i >= len(winners) {
			if _, err := tx.Exec(ctx, "DELETE FROM channel_gift_boosts WHERE id = ANY($1)", ids[i:]); err != nil {
				return err
			}
			break
		}
		// a Premium prize's boosts last as long as the Premium; Stars ones
		// keep the year they got at payment
		if g.Kind == "premium" {
			_, err = tx.Exec(ctx, "UPDATE channel_gift_boosts SET user_id=$2, assigned_at=$3, expires_at=$4 WHERE id=$1", id, winners[i], now, int64(now)+int64(g.Days)*86400)
		} else {
			_, err = tx.Exec(ctx, "UPDATE channel_gift_boosts SET user_id=$2, assigned_at=$3 WHERE id=$1", id, winners[i], now)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

var slugEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

func newSlug() string {
	b := make([]byte, 10)
	rand.Read(b)
	return strings.ToLower(slugEnc.EncodeToString(b))
}

// Now is the service's clock, which tests move.
func (s *Service) Now() int { return s.unix() }

// GiveawayAt is the giveaway a channel message belongs to: the post, the
// launch message or either results message.
func (s *Service) GiveawayAt(ctx context.Context, channel int64, msg int) (Giveaway, bool, error) {
	if msg <= 0 {
		return Giveaway{}, false, nil
	}
	g, err := scanGiveaway(s.db.QueryRow(ctx, "SELECT "+giveawayCols+` FROM giveaways g WHERE g.channel_id=$1
		AND $2 IN (g.post_id, g.launch_id, g.results_id, g.results_post_id) ORDER BY g.id LIMIT 1`, channel, msg))
	if errors.Is(err, pgx.ErrNoRows) {
		return g, false, nil
	}
	return g, err == nil, err
}

// Eligibility is why a user takes part in a running giveaway or doesn't.
type Eligibility struct {
	InPool         bool  // country aside
	AdminOf        int64 // a channel of the giveaway they run
	JoinedTooEarly int   // they joined before it started, and it's for new subscribers
}

func (s *Service) Eligibility(ctx context.Context, g Giveaway, user int64) (Eligibility, error) {
	var e Eligibility
	err := s.db.QueryRow(ctx, `SELECT coalesce((SELECT channel_id FROM channel_members WHERE user_id=$1 AND channel_id = ANY($2)
		AND status IN ('admin', 'creator') ORDER BY array_position($2, channel_id) LIMIT 1), 0)`, user, g.Channels()).Scan(&e.AdminOf)
	if err != nil {
		return e, err
	}
	if g.OnlyNew {
		err := s.db.QueryRow(ctx, `SELECT coalesce(min(date), 0) FROM channel_members WHERE user_id=$1 AND channel_id = ANY($2)
			AND status IN ('member', 'restricted') AND date < $3`, user, g.Channels(), g.Started).Scan(&e.JoinedTooEarly)
		if err != nil {
			return e, err
		}
	}
	err = s.db.QueryRow(ctx, "SELECT EXISTS("+poolSQL+" AND m.user_id = $8)", append(g.poolArgs(s.unix()), user)...).Scan(&e.InPool)
	return e, err
}

// Won is a user's prize in a drawn giveaway.
type Won struct {
	Stars int64
	Slug  string
}

func (s *Service) WonIn(ctx context.Context, g Giveaway, user int64) (Won, bool, error) {
	var w Won
	err := s.db.QueryRow(ctx, `SELECT w.stars, coalesce(c.slug, '') FROM giveaway_winners w
		LEFT JOIN gift_codes c ON c.giveaway_id = w.giveaway_id AND c.user_id = w.user_id
		WHERE w.giveaway_id=$1 AND w.user_id=$2`, g.ID, user).Scan(&w.Stars, &w.Slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return w, false, nil
	}
	return w, err == nil, err
}

// PrizeNotice is one winner's prize message still to send.
type PrizeNotice struct {
	Giveaway      int64
	Kind          string
	User          int64
	Channel       int64
	Post          int
	Stars         int64
	TransactionID string
	Months        int
	Days          int
	Slug          string
}

// PendingPrizes are prize messages not sent yet: the draw commits first,
// the messages follow and a crash in between sends them on the next round.
func (s *Service) PendingPrizes(ctx context.Context) ([]PrizeNotice, error) {
	rows, err := s.db.Query(ctx, `SELECT g.id, g.kind, w.user_id, g.channel_id, g.post_id, w.stars, w.transaction_id, g.months, g.days, coalesce(c.slug, '')
		FROM giveaway_winners w JOIN giveaways g ON g.id = w.giveaway_id
		LEFT JOIN gift_codes c ON c.giveaway_id = w.giveaway_id AND c.user_id = w.user_id
		WHERE NOT w.notified ORDER BY g.id, w.user_id LIMIT 500`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (PrizeNotice, error) {
		var p PrizeNotice
		err := r.Scan(&p.Giveaway, &p.Kind, &p.User, &p.Channel, &p.Post, &p.Stars, &p.TransactionID, &p.Months, &p.Days, &p.Slug)
		return p, err
	})
}

func (s *Service) PrizeSent(ctx context.Context, giveaway, user int64) error {
	_, err := s.db.Exec(ctx, "UPDATE giveaway_winners SET notified=true WHERE giveaway_id=$1 AND user_id=$2", giveaway, user)
	return err
}

// GiftCode is a gift code a channel gave, as checkGiftCode shows it.
type GiftCode struct {
	Slug     string
	User     int64
	Channel  int64
	Giveaway bool
	Post     int
	Days     int
	Date     int
}

func (s *Service) GiftCode(ctx context.Context, slug string) (GiftCode, error) {
	var c GiftCode
	if slug == "" {
		return c, errGiftCodeEmpty
	}
	err := s.db.QueryRow(ctx, `SELECT c.slug, c.user_id, c.channel_id, c.giveaway_id IS NOT NULL, coalesce(g.post_id, 0), c.days, c.created_at
		FROM gift_codes c LEFT JOIN giveaways g ON g.id = c.giveaway_id WHERE c.slug=$1`, strings.ToLower(slug)).
		Scan(&c.Slug, &c.User, &c.Channel, &c.Giveaway, &c.Post, &c.Days, &c.Date)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, errGiftCodeEmpty
	}
	return c, err
}
