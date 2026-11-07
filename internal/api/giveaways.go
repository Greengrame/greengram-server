package api

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"slices"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/messages"
	"github.com/Greengrame/greengram-server/internal/stars"
)

// Giveaways with a draw at their end date (stars/draw.go): what the buyer
// picked, the channel posts, the draw on a timer, the prize messages and the
// info sheet.

func (a *API) registerGiveaways() {
	a.stars.Countries = a.inCountries
	handle(a, tlprofile.SemanticMethodPaymentsGetGiveawayInfo, a.getGiveawayInfo)
	handle(a, tlprofile.SemanticMethodPaymentsCheckGiftCode, a.checkGiftCode)
}

// giveawayOptions turns a purpose's fields into options. Every additional
// channel has to be one the buyer runs, like the boost channel.
func (a *API) giveawayOptions(ctx context.Context, me account.User, extra []tg.InputPeerClass, countries []string, prize string,
	onlyNew, visible bool, until int) (stars.GiveawayOptions, error) {
	o := stars.GiveawayOptions{Countries: countries, Prize: prize, OnlyNew: onlyNew, WinnersVisible: visible, Until: until}
	for _, p := range extra {
		id, err := a.giveawayChannel(ctx, me, p)
		if err != nil {
			return o, err
		}
		o.Additional = append(o.Additional, id)
	}
	return o, nil
}

func giveawayMedia(g stars.Giveaway) *tg.MessageMediaGiveaway {
	m := &tg.MessageMediaGiveaway{OnlyNewSubscribers: g.OnlyNew, WinnersAreVisible: g.WinnersVisible, Channels: g.Channels(),
		PrizeDescription: g.Prize, Quantity: g.Quantity, UntilDate: g.Until}
	if len(g.Countries) > 0 {
		m.CountriesISO2 = g.Countries
	}
	if g.Kind == "stars" {
		m.Stars = g.Stars
	} else {
		m.Months = g.Months
	}
	return m
}

// launchPost puts a paid giveaway in its channel: the launch message, then
// the post with everything the buyer picked. events get what to deliver
// once the payment is in.
func (a *API) launchPost(buyer int64, events *[]channels.Event) stars.LaunchPost {
	return func(ctx context.Context, tx pgx.Tx, g stars.Giveaway) (int, int, error) {
		act := &tg.MessageActionGiveawayLaunch{}
		if g.Kind == "stars" {
			act.Stars = g.Stars
		}
		launch, e1, err := a.chans.PostTx(ctx, tx, g.Channel, buyer, act, media.Ref{}, 0)
		if err != nil {
			return 0, 0, err
		}
		post, e2, err := a.chans.PostTx(ctx, tx, g.Channel, buyer, nil, media.Ref{TL: giveawayMedia(g)}, 0)
		if err != nil {
			return 0, 0, err
		}
		*events = append(*events, e1, e2)
		return launch.ID, post.ID, nil
	}
}

// drawPosts is how a draw ends in the channel: the results message always,
// as a reply to the giveaway, and the list of winners when they're public.
func (a *API) drawPosts(events map[int64][]channels.Event) stars.DrawPosts {
	return func(ctx context.Context, tx pgx.Tx, g stars.Giveaway, winners []int64) (int, int, error) {
		act := &tg.MessageActionGiveawayResults{Stars: g.Kind == "stars", WinnersCount: len(winners), UnclaimedCount: g.Quantity - len(winners)}
		res, e, err := a.chans.PostTx(ctx, tx, g.Channel, g.Buyer, act, media.Ref{}, g.PostID)
		if err != nil {
			return 0, 0, err
		}
		list := []channels.Event{e}
		post := 0
		if g.WinnersVisible && len(winners) > 0 {
			r := &tg.MessageMediaGiveawayResults{OnlyNewSubscribers: g.OnlyNew, ChannelID: g.Channel, AdditionalPeersCount: len(g.Additional),
				LaunchMsgID: g.PostID, WinnersCount: len(winners), UnclaimedCount: g.Quantity - len(winners), PrizeDescription: g.Prize, UntilDate: g.Until}
			for _, w := range winners {
				r.Winners = append(r.Winners, clientID(w))
			}
			if g.Kind == "stars" {
				r.Stars = g.Stars
			} else {
				r.Months = g.Months
			}
			m, e, err := a.chans.PostTx(ctx, tx, g.Channel, g.Buyer, nil, media.Ref{TL: r}, 0)
			if err != nil {
				return 0, 0, err
			}
			post = m.ID
			list = append(list, e)
		}
		events[g.ID] = list
		return res.ID, post, nil
	}
}

// drawLoop draws what is due about once a minute, and right away at start:
// a giveaway that ended while the server was down is drawn then.
func (a *API) drawLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		a.DrawGiveaways(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// DrawGiveaways draws every giveaway past its end date and sends the prize
// messages that are still owed.
func (a *API) DrawGiveaways(ctx context.Context) {
	if a.stars == nil {
		return
	}
	events := map[int64][]channels.Event{}
	drawn, err := a.stars.DrawDue(ctx, a.drawPosts(events))
	if err != nil && ctx.Err() == nil {
		slog.Warn("giveaways: draw", "err", err)
	}
	// only what committed: a failed draw's posts were rolled back with it
	for _, d := range drawn {
		a.deliverChannel(events[d.Giveaway.ID], edge.Session{})
		if d.Giveaway.Refunded > 0 {
			a.pushBalance(d.Giveaway.Buyer)
		}
		slog.Info("giveaways: drawn", "id", d.Giveaway.ID, "channel", d.Giveaway.Channel, "winners", len(d.Winners), "refunded", d.Giveaway.Refunded, "gone", d.Gone)
	}
	a.sendPrizes(ctx)
}

// prizeRandomID makes a prize message once only: the same giveaway and
// winner always give the same id, and Send refuses it the second time.
func prizeRandomID(giveaway, user int64) int64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "giveaway-prize:%d:%d", giveaway, user)
	return int64(h.Sum64()>>1) | 1
}

func monthsText(months int) string {
	if months == 1 {
		return "🎁 Premium · 1 month"
	}
	return fmt.Sprintf("🎁 Premium · %d months", months)
}

// sendPrizes tells winners what they won, from the service account
// (777000) like Telegram's gift codes: the prize is the channel's, not the
// buyer's. The draw has already paid it; this is only the message.
func (a *API) sendPrizes(ctx context.Context) {
	from := ids.Announcements()
	if from == 0 {
		return
	}
	list, err := a.stars.PendingPrizes(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("giveaways: prizes", "err", err)
		}
		return
	}
	for _, p := range list {
		var d messages.Draft
		channel := &tg.PeerChannel{ChannelID: p.Channel}
		if p.Kind == "stars" {
			d = messages.Draft{Text: fmt.Sprintf("⭐️ %d Stars", p.Stars),
				Action: &tg.MessageActionPrizeStars{Stars: p.Stars, TransactionID: p.TransactionID, BoostPeer: channel, GiveawayMsgID: p.Post}}
		} else {
			d = messages.Draft{Text: monthsText(p.Months), Action: &tg.MessageActionGiftCode{ViaGiveaway: true, BoostPeer: channel, Days: p.Days, Slug: p.Slug}}
		}
		d.RandomID = prizeRandomID(p.Giveaway, p.User)
		s, err := a.msgs.Send(ctx, from, p.User, d)
		switch {
		case err == nil:
			a.deliver(s.Events, edge.Session{})
		case tgerr.Is(err, "RANDOM_ID_DUPLICATE"):
			// sent before a crash took the mark with it
		default:
			slog.Warn("giveaways: prize message", "giveaway", p.Giveaway, "user", p.User, "err", err)
			continue
		}
		if err := a.stars.PrizeSent(ctx, p.Giveaway, p.User); err != nil {
			slog.Warn("giveaways: prize sent", "err", err)
		}
		if p.Kind == "stars" {
			a.pushBalance(p.User)
		} else {
			a.pushUserChanged(p.User)
		}
	}
}

// userCountries is where the server can place users: the countries their
// phone number's calling code belongs to, all of them when several share it
// (+1, +7). No number, or one no country has, places them nowhere.
func (a *API) userCountries(ctx context.Context, users []int64) (map[int64][]string, error) {
	out := map[int64][]string{}
	list, err := a.countryList(ctx)
	if err != nil || len(list) == 0 {
		return out, err
	}
	codes := map[string][]string{}
	for _, c := range list {
		if c.ISO2 != "" && !slices.Contains(codes[c.Code], c.ISO2) {
			codes[c.Code] = append(codes[c.Code], c.ISO2)
		}
	}
	us, err := a.accounts.Users(ctx, users)
	if err != nil {
		return nil, err
	}
	for id, u := range us {
		d := ""
		for _, r := range u.Phone {
			if r >= '0' && r <= '9' {
				d += string(r)
			}
		}
		for n := len(d); n > 0; n-- {
			if cs, ok := codes[d[:n]]; ok {
				out[id] = cs
				break
			}
		}
	}
	return out, nil
}

// inCountries is the draw's country filter (stars.CountryFilter).
func (a *API) inCountries(ctx context.Context, users []int64, allowed []string) ([]int64, error) {
	// at start the list may not be in yet: draw later rather than refund
	if a.countries.Load() == nil {
		return nil, errNoCountries
	}
	where, err := a.userCountries(ctx, users)
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, u := range users {
		for _, c := range where[u] {
			if slices.Contains(allowed, c) {
				out = append(out, u)
				break
			}
		}
	}
	return out, nil
}

var (
	errGiveawayMsg = tgerr.New(400, "MESSAGE_ID_INVALID")
	errNoCountries = errors.New("giveaways: no country list yet")
)

func (a *API) getGiveawayInfo(ctx context.Context, r *tg.PaymentsGetGiveawayInfoRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	v, ok := r.Peer.(*tg.InputPeerChannel)
	if !ok {
		return nil, errChannelInvalid
	}
	if _, _, err := a.chans.Access(ctx, v.ChannelID, me.ID); err != nil {
		return nil, err
	}
	g, ok, err := a.stars.GiveawayAt(ctx, v.ChannelID, r.MsgID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errGiveawayMsg
	}
	if g.State != "running" {
		out := &tg.PaymentsGiveawayInfoResults{StartDate: g.Started, FinishDate: g.DrawnAt, WinnersCount: g.Winners, Refunded: g.State == "refunded"}
		w, won, err := a.stars.WonIn(ctx, g, me.ID)
		if err != nil {
			return nil, err
		}
		if won {
			out.Winner = true
			if g.Kind == "stars" {
				out.StarsPrize = w.Stars
			} else {
				out.GiftCodeSlug = w.Slug
			}
		}
		// Premium is given at the draw: every code is used
		if g.Kind == "premium" && g.Winners > 0 {
			out.ActivatedCount = g.Winners
		}
		return out, nil
	}
	out := &tg.PaymentsGiveawayInfo{StartDate: g.Started, PreparingResults: g.Until <= a.stars.Now()}
	e, err := a.stars.Eligibility(ctx, g, me.ID)
	if err != nil {
		return nil, err
	}
	// the app shows one reason; the first that applies
	switch {
	case e.AdminOf != 0:
		out.AdminDisallowedChatID = e.AdminOf
	case e.JoinedTooEarly != 0:
		out.JoinedTooEarlyDate = e.JoinedTooEarly
	case len(g.Countries) > 0 && !a.fromCountries(ctx, me.ID, g.Countries, out):
	default:
		out.Participating = e.InPool && me.ID != g.Buyer
	}
	return out, nil
}

// fromCountries says whether user is from one of countries; if not, it
// names where they are on out. Unknown is "XX", the code for no country.
func (a *API) fromCountries(ctx context.Context, user int64, countries []string, out *tg.PaymentsGiveawayInfo) bool {
	where, err := a.userCountries(ctx, []int64{user})
	if err != nil {
		slog.Warn("giveaways: country", "err", err)
	}
	for _, c := range where[user] {
		if slices.Contains(countries, c) {
			return true
		}
	}
	out.DisallowedCountry = "XX"
	if cs := where[user]; len(cs) > 0 {
		out.DisallowedCountry = cs[0]
	}
	return false
}

// checkGiftCode shows a gift code a channel gave. It was applied when it was
// given, so it's always used, and the app offers nothing to redeem.
func (a *API) checkGiftCode(ctx context.Context, r *tg.PaymentsCheckGiftCodeRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	c, err := a.stars.GiftCode(ctx, r.Slug)
	if err != nil {
		return nil, err
	}
	out := &tg.PaymentsCheckedGiftCode{ViaGiveaway: c.Giveaway, FromID: &tg.PeerChannel{ChannelID: c.Channel}, GiveawayMsgID: c.Post,
		ToID: clientID(c.User), Date: c.Date, Days: c.Days, UsedDate: c.Date}
	if out.Chats, err = a.chatsFor(ctx, me.ID, []int64{c.Channel}); err != nil {
		return nil, err
	}
	if out.Users, err = a.usersFor(ctx, me.ID, []int64{c.User}); err != nil {
		return nil, err
	}
	return out, nil
}
