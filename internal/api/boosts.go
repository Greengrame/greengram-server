package api

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
)

func (a *API) registerBoosts() {
	handle(a, tlprofile.SemanticMethodPremiumGetBoostsStatus, a.getBoostsStatus)
	handle(a, tlprofile.SemanticMethodPremiumGetMyBoosts, a.getMyBoosts)
	handle(a, tlprofile.SemanticMethodPremiumApplyBoost, a.applyBoost)
	handle(a, tlprofile.SemanticMethodPremiumGetBoostsList, a.getBoostsList)
}

var errPremiumRequired = tgerr.New(403, "PREMIUM_ACCOUNT_REQUIRED")

// boostChannel is any channel or supergroup the viewer can see. Access
// keeps kicked users and outsiders of private chats out; a public one can
// be boosted without joining, like on Telegram.
func (a *API) boostChannel(ctx context.Context, p tg.InputPeerClass) (account.User, channels.Channel, channels.Membership, error) {
	me, err := a.me(ctx)
	if err != nil {
		return me, channels.Channel{}, channels.Membership{}, err
	}
	v, ok := p.(*tg.InputPeerChannel)
	if !ok {
		return me, channels.Channel{}, channels.Membership{}, errChannelInvalid
	}
	c, m, err := a.chans.Access(ctx, v.ChannelID, me.ID)
	return me, c, m, err
}

func (a *API) boostURL(c channels.Channel) string {
	if c.Username != "" {
		return a.in.BaseURL + "/u/" + c.Username + "?boost"
	}
	return a.in.BaseURL + "/c/" + strconv.FormatInt(c.ID, 10) + "/1?boost"
}

func (a *API) getBoostsStatus(ctx context.Context, r *tg.PremiumGetBoostsStatusRequest) (any, error) {
	me, c, m, err := a.boostChannel(ctx, r.Peer)
	if err != nil {
		return nil, err
	}
	st, err := a.stars.ChannelBoosts(ctx, me.ID, c.ID)
	if err != nil {
		return nil, err
	}
	out := &tg.PremiumBoostsStatus{Level: st.Level, CurrentLevelBoosts: st.Current, Boosts: st.Boosts, BoostURL: a.boostURL(c)}
	if st.Next != 0 {
		out.SetNextLevelBoosts(st.Next)
	}
	if st.GiftBoosts > 0 {
		out.SetGiftBoosts(st.GiftBoosts)
	}
	if len(st.Mine) > 0 {
		out.SetMyBoost(true)
		out.SetMyBoostSlots(st.Mine)
	}
	// the stats page's premium subscribers, for admins as on Telegram
	if m.Status == channels.Creator || m.Status == channels.Admin {
		if n, err := a.chans.PremiumMembers(ctx, c.ID); err == nil {
			out.SetPremiumAudience(tg.StatsPercentValue{Part: float64(n), Total: float64(c.Count)})
		}
	}
	return out, nil
}

func (a *API) myBoosts(ctx context.Context, me account.User) (*tg.PremiumMyBoosts, error) {
	list, err := a.stars.MyBoosts(ctx, me.ID, me.PremiumUntil, me.Premium(time.Now()))
	if err != nil {
		return nil, err
	}
	out := &tg.PremiumMyBoosts{MyBoosts: []tg.MyBoost{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
	var chans []int64
	for _, b := range list {
		mb := tg.MyBoost{Slot: b.Slot, Date: b.Date, Expires: int(min(b.Expires, 1<<31-1))}
		if b.Channel != 0 {
			mb.SetPeer(&tg.PeerChannel{ChannelID: b.Channel})
			chans = append(chans, b.Channel)
		}
		if b.Cooldown > now() {
			mb.SetCooldownUntilDate(b.Cooldown)
		}
		out.MyBoosts = append(out.MyBoosts, mb)
	}
	if out.Chats, err = a.chatsFor(ctx, me.ID, chans); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *API) getMyBoosts(ctx context.Context, _ *tg.PremiumGetMyBoostsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	return a.myBoosts(ctx, me)
}

func (a *API) applyBoost(ctx context.Context, r *tg.PremiumApplyBoostRequest) (any, error) {
	me, c, _, err := a.boostChannel(ctx, r.Peer)
	if err != nil {
		return nil, err
	}
	if !me.Premium(time.Now()) {
		return nil, errPremiumRequired
	}
	slots := r.Slots
	if len(slots) == 0 {
		// no slots means "the first free one", like the apps' one-tap boost
		list, err := a.stars.MyBoosts(ctx, me.ID, me.PremiumUntil, true)
		if err != nil {
			return nil, err
		}
		for _, b := range list {
			if b.Channel == 0 {
				slots = []int{b.Slot}
				break
			}
		}
		if len(slots) == 0 {
			return nil, tgerr.New(400, "BOOSTS_EMPTY")
		}
	}
	if err := a.stars.ApplyBoosts(ctx, me.ID, c.ID, slots); err != nil {
		return nil, err
	}
	return a.myBoosts(ctx, me)
}

func (a *API) getBoostsList(ctx context.Context, r *tg.PremiumGetBoostsListRequest) (any, error) {
	me, c, m, err := a.boostChannel(ctx, r.Peer)
	if err != nil {
		return nil, err
	}
	// who boosted is for admins, as on Telegram
	if m.Status != channels.Creator && m.Status != channels.Admin {
		return nil, tgerr.New(400, "CHAT_ADMIN_REQUIRED")
	}
	out := &tg.PremiumBoostsList{Boosts: []tg.Boost{}, Users: []tg.UserClass{}}
	st, err := a.stars.ChannelBoosts(ctx, me.ID, c.ID)
	if err != nil {
		return nil, err
	}
	// Telegram's list is every boost and gifts=true narrows it to the ones
	// the chat gave away; count is in boosts, a multiplier counts as many
	var users []int64
	if !r.Gifts {
		for _, b := range st.List {
			out.Boosts = append(out.Boosts, tg.Boost{ID: strconv.FormatInt(b.User, 10) + ":" + strconv.Itoa(b.Slot), UserID: clientID(b.User),
				Date: b.Date, Expires: int(min(b.Expires, 1<<31-1)), Multiplier: 1})
			users = append(users, b.User)
		}
	}
	for _, g := range st.Gifts {
		b := tg.Boost{ID: "gift:" + strconv.FormatInt(g.User, 10) + ":" + strconv.Itoa(g.Date), Date: g.Date, Expires: int(min(g.Expires, 1<<31-1)), Multiplier: g.Multiplier}
		b.SetGift(!g.Drawn)
		b.SetGiveaway(g.Drawn)
		if g.Stars > 0 {
			b.SetStars(g.Stars)
		}
		if g.Post != 0 {
			b.SetGiveawayMsgID(g.Post)
		}
		// a running giveaway's: the app shows it as still to be given
		if g.User == 0 {
			b.ID = "giveaway:" + strconv.FormatInt(g.ID, 10)
		} else {
			b.SetUserID(clientID(g.User))
			users = append(users, g.User)
		}
		out.Boosts = append(out.Boosts, b)
	}
	sort.SliceStable(out.Boosts, func(i, j int) bool { return out.Boosts[i].Date > out.Boosts[j].Date })
	out.Count = st.GiftBoosts
	if !r.Gifts {
		out.Count = st.Boosts
	}
	if out.Users, err = a.usersFor(ctx, me.ID, users); err != nil {
		return nil, err
	}
	return out, nil
}
