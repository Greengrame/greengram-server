package api

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/messages"
	"github.com/Greengrame/greengram-server/internal/stars"
	"github.com/Greengrame/greengram-server/internal/wallet"
)

// RegisterStars installs Stars, Premium, star gifts and boosts. Payments
// are Stars only: getPaymentForm prices, sendStarsForm pays from the
// balance. Top-ups come from the wallet (crypto, see RegisterTopup) or an
// admin.
func (a *API) RegisterStars(s *stars.Service) {
	a.stars = s
	handle(a, tlprofile.SemanticMethodPaymentsGetStarsStatus, a.getStarsStatus)
	handle(a, tlprofile.SemanticMethodPaymentsGetStarsTransactions, a.getStarsTransactions)
	handle(a, tlprofile.SemanticMethodPaymentsGetStarsTopupOptions, a.getStarsTopupOptions)
	handle(a, tlprofile.SemanticMethodPaymentsSendPaymentForm, a.sendPaymentForm)
	handle(a, tlprofile.SemanticMethodPaymentsGetStarsGiftOptions, func(context.Context, *tg.PaymentsGetStarsGiftOptionsRequest) (any, error) {
		out := []tg.StarsGiftOption{}
		for _, n := range []int64{50, 100, 250, 500, 1000, 2500, 5000, 10000, 25000, 50000, 100000} {
			out = append(out, tg.StarsGiftOption{Stars: n, Currency: "XTR", Amount: n})
		}
		return out, nil
	})
	handle(a, tlprofile.SemanticMethodPaymentsGetPaymentForm, a.getPaymentForm)
	handle(a, tlprofile.SemanticMethodPaymentsSendStarsForm, a.sendStarsForm)
	handle(a, tlprofile.SemanticMethodPaymentsGetPremiumGiftCodeOptions, a.getPremiumGiftCodeOptions)
	handle(a, tlprofile.SemanticMethodPaymentsGetStarsGiveawayOptions, a.getStarsGiveawayOptions)
	handle(a, tlprofile.SemanticMethodHelpGetPremiumPromo, a.getPremiumPromo)
	a.registerGifts()
	a.registerBoosts()
	a.registerGiveaways()
}

// selfPeer is the only Stars balance a user has.
func (a *API) selfPeer(ctx context.Context, p tg.InputPeerClass) (account.User, error) {
	me, err := a.me(ctx)
	if err != nil {
		return me, err
	}
	switch v := p.(type) {
	case *tg.InputPeerSelf:
		return me, nil
	case *tg.InputPeerUser:
		if serverID(v.UserID) == me.ID {
			return me, nil
		}
	}
	return me, errPeerInvalid
}

func starsStatus(balance int64) *tg.PaymentsStarsStatus {
	return &tg.PaymentsStarsStatus{Balance: &tg.StarsAmount{Amount: balance}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
}

func (a *API) getStarsStatus(ctx context.Context, r *tg.PaymentsGetStarsStatusRequest) (any, error) {
	me, err := a.selfPeer(ctx, r.Peer)
	if err != nil {
		return nil, err
	}
	if r.Ton {
		return &tg.PaymentsStarsStatus{Balance: &tg.StarsTonAmount{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}, nil
	}
	// the status carries the first page, as on Telegram: it's the only
	// history Web A's balance modal asks for before it has something to show
	return a.starsPage(ctx, me, stars.HistoryQuery{Limit: 20})
}

func (a *API) getStarsTransactions(ctx context.Context, r *tg.PaymentsGetStarsTransactionsRequest) (any, error) {
	me, err := a.selfPeer(ctx, r.Peer)
	if err != nil {
		return nil, err
	}
	if r.Ton {
		return &tg.PaymentsStarsStatus{Balance: &tg.StarsTonAmount{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}, nil
	}
	if r.Inbound && r.Outbound {
		return nil, tgerr.New(400, "INPUT_REQUEST_INVALID")
	}
	if r.SubscriptionID != "" {
		return nil, tgerr.New(400, "SUBSCRIPTION_ID_INVALID")
	}
	return a.starsPage(ctx, me, stars.HistoryQuery{Inbound: r.Inbound, Outbound: r.Outbound, Offset: r.Offset, Limit: r.Limit})
}

func (a *API) starsPage(ctx context.Context, me account.User, q stars.HistoryQuery) (any, error) {
	list, next, err := a.stars.History(ctx, me.ID, q)
	if err != nil {
		return nil, err
	}
	bal, err := a.stars.Balance(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	out := starsStatus(bal)
	var peers, chans []int64
	hist := make([]tg.StarsTransaction, 0, len(list))
	for _, t := range list {
		hist = append(hist, starsTransaction(t))
		if t.Peer != 0 {
			peers = append(peers, t.Peer)
		}
		if t.Channel != 0 {
			chans = append(chans, t.Channel)
		}
	}
	if len(hist) > 0 {
		out.SetHistory(hist)
	}
	// the last page must not carry next_offset, or clients page forever
	if next != "" {
		out.SetNextOffset(next)
	}
	if out.Users, err = a.usersFor(ctx, me.ID, peers); err != nil {
		return nil, err
	}
	if out.Chats, err = a.chatsFor(ctx, me.ID, chans); err != nil {
		return nil, err
	}
	return out, nil
}

func starsTransaction(t stars.Txn) tg.StarsTransaction {
	out := tg.StarsTransaction{ID: t.TransactionID, Amount: &tg.StarsAmount{Amount: t.Amount}, Date: t.Date, Peer: &tg.StarsTransactionPeerPremiumBot{}}
	if t.Peer != 0 {
		out.Peer = &tg.StarsTransactionPeer{Peer: &tg.PeerUser{UserID: clientID(t.Peer)}}
	}
	if t.Channel != 0 {
		out.Peer = &tg.StarsTransactionPeer{Peer: &tg.PeerChannel{ChannelID: t.Channel}}
	}
	if t.Reason == "stars_giveaway_prize" && t.Post != 0 {
		out.SetGiveawayPostID(t.Post)
	}
	if t.Title != "" {
		out.SetTitle(t.Title)
	}
	if t.Note != "" {
		out.SetDescription(t.Note)
	}
	switch t.Reason {
	case "gift_purchase", "gift_conversion", "transfer_in", "transfer_out":
		out.Gift = true
	case "gift_resale", "gift_market":
		out.StargiftResale = true
	case "paid_reaction", "channel_reaction_earnings":
		out.Reaction = true
	case "premium_purchase", "premium_gift":
		if t.Months > 0 {
			out.SetPremiumGiftMonths(t.Months)
		}
	}
	return out
}

// premiumBot is who Stars payments for Premium are made out to.
func (a *API) premiumBot(ctx context.Context, viewer int64) (int64, []tg.UserClass, error) {
	return a.paymentBot(ctx, viewer, stars.PremiumAccount)
}

func (a *API) paymentBot(ctx context.Context, viewer int64, sys stars.System) (int64, []tg.UserClass, error) {
	id, err := a.stars.SystemUser(ctx, sys)
	if err != nil {
		return 0, nil, err
	}
	users, err := a.usersFor(ctx, viewer, []int64{id})
	return id, users, err
}

func xtr(label string, amount int64) tg.Invoice {
	return tg.Invoice{Currency: "XTR", Prices: []tg.LabeledPrice{{Label: label, Amount: amount}}}
}

// acceptsGift is account-settings.js acceptsGift(): the owner's "gifts"
// privacy rule. The disallowed-gift-kinds switches aren't stored here yet.
func (a *API) acceptsGift(ctx context.Context, owner, sender int64) bool {
	if owner == sender {
		return true
	}
	return a.canSee(ctx, owner, sender, "gifts")
}

var errGiftPrivacy = tgerr.New(403, "USER_PRIVACY_RESTRICTED")

func (a *API) getPaymentForm(ctx context.Context, r *tg.PaymentsGetPaymentFormRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	premiumForm := func(to int64, months int, message string) (any, error) {
		if to != me.ID && !a.acceptsGift(ctx, to, me.ID) {
			return nil, errGiftPrivacy
		}
		f, err := a.stars.PremiumForm(ctx, me.ID, to, months, message)
		if err != nil {
			return nil, err
		}
		bot, users, err := a.premiumBot(ctx, me.ID)
		if err != nil {
			return nil, err
		}
		title := fmt.Sprintf("Premium for %d months", f.Months)
		if f.Months == 1 {
			title = "Premium for 1 month"
		}
		return &tg.PaymentsPaymentFormStars{FormID: f.ID, BotID: clientID(bot), Title: title, Description: "Greengram Premium",
			Invoice: xtr(title, f.Stars), Users: users}, nil
	}
	switch inv := r.Invoice.(type) {
	case *tg.InputInvoicePremiumGiftCode:
		return a.giveawayForm(ctx, me, inv)
	case *tg.InputInvoicePremiumGiftStars:
		// the web let you buy any plan for yourself the same way
		to, err := a.inputUser(ctx, me, inv.UserID)
		if err != nil {
			return nil, err
		}
		return premiumForm(to, inv.Months, inv.Message.Text)
	case *tg.InputInvoiceStars:
		switch p := inv.Purpose.(type) {
		case *tg.InputStorePaymentPremiumSubscription:
			if p.Restore {
				return nil, tgerr.New(400, "INVOICE_INVALID")
			}
			// the store purpose has no length: upgrade is the yearly one
			months := 1
			if p.Upgrade {
				months = 12
			}
			return premiumForm(me.ID, months, "")
		case *tg.InputStorePaymentStarsGiveaway:
			return a.starsGiveawayForm(ctx, me, p)
		case *tg.InputStorePaymentStarsTopup:
			return a.topupForm(ctx, me, p, r.ThemeParams)
		case *tg.InputStorePaymentStarsGift:
			to, err := a.inputUser(ctx, me, p.UserID)
			if err != nil {
				return nil, err
			}
			if !a.acceptsGift(ctx, to, me.ID) {
				return nil, errGiftPrivacy
			}
			f, err := a.stars.StarsGiftForm(ctx, me.ID, to, p.Stars)
			if err != nil {
				return nil, err
			}
			bot, users, err := a.paymentBot(ctx, me.ID, stars.StarsAccount)
			if err != nil {
				return nil, err
			}
			title := fmt.Sprintf("%d Stars", f.Stars)
			return &tg.PaymentsPaymentFormStars{FormID: f.ID, BotID: clientID(bot), Title: title, Description: "Gift Stars to a friend",
				Invoice: xtr(title, f.Stars), Users: users}, nil
		}
		return nil, tgerr.New(400, "INVOICE_INVALID")
	case *tg.InputInvoiceStarGift:
		// a person who takes gifts from me, or a broadcast channel I can see
		to, err := a.giftHolder(ctx, me, inv.Peer)
		if err != nil {
			return nil, err
		}
		if to.User != 0 && !a.acceptsGift(ctx, to.User, me.ID) {
			return nil, errGiftPrivacy
		}
		f, g, err := a.stars.GiftForm(ctx, me.ID, to, inv.GiftID, inv.Message.Text, inv.HideName, me.Premium(time.Now()), inv.IncludeUpgrade)
		if err != nil {
			return nil, err
		}
		return &tg.PaymentsPaymentFormStarGift{FormID: f.ID, Invoice: xtr(g.Title, f.Stars)}, nil
	case *tg.InputInvoiceStarGiftUpgrade:
		return a.upgradeForm(ctx, me, inv)
	case *tg.InputInvoiceStarGiftTransfer:
		return a.transferForm(ctx, me, inv)
	case *tg.InputInvoiceStarGiftResale:
		return a.resaleForm(ctx, me, inv)
	}
	return nil, tgerr.New(400, "INVOICE_INVALID")
}

func (a *API) sendStarsForm(ctx context.Context, r *tg.PaymentsSendStarsFormRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if r.FormID == 0 {
		return nil, tgerr.New(400, "FORM_ID_EMPTY")
	}
	kind, err := a.stars.FormKind(ctx, me.ID, r.FormID)
	if err != nil {
		return nil, err
	}
	up := &tg.Updates{Updates: []tg.UpdateClass{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: now()}
	var balance int64
	var users, chats []int64
	switch kind {
	case stars.KindGiftUpgrade:
		if balance, users, chats, err = a.payUpgrade(ctx, me, r.FormID, up); err != nil {
			return nil, err
		}
	case stars.KindGiftTransfer:
		if balance, users, chats, err = a.payTransfer(ctx, me, r.FormID, up); err != nil {
			return nil, err
		}
	case stars.KindGiftResale:
		if balance, users, chats, err = a.payResale(ctx, me, r.FormID, up); err != nil {
			return nil, err
		}
	case stars.KindGiveaway:
		var events []channels.Event
		res, err := a.stars.PayGiveaway(ctx, me.ID, r.FormID, a.launchPost(me.ID, &events))
		if err != nil {
			return nil, err
		}
		balance = res.Balance
		users = append(users, me.ID)
		if res.Launched != nil {
			// the payer's reply doesn't carry the posts, so their session gets them too
			a.deliverChannel(events, edge.Session{})
			break
		}
		// chosen people: theirs now, from the service account, as the
		// channel's gift codes
		users = append(users, res.Winners...)
		if from := ids.Announcements(); from != 0 {
			for _, w := range res.Winners {
				act := &tg.MessageActionGiftCode{BoostPeer: &tg.PeerChannel{ChannelID: res.Channel}, Days: res.Form.Days, Slug: res.Slugs[w]}
				s, err := a.msgs.Send(ctx, from, w, messages.Draft{Text: monthsText(res.Form.Months), Action: act})
				if err != nil {
					slog.Warn("giveaway: gift code message", "err", err)
				} else {
					a.deliver(s.Events, edge.Session{})
				}
				a.pushUserChanged(w)
			}
		}
		if ev, err := a.chans.PostService(ctx, res.Channel, me.ID, &tg.MessageActionGiveawayResults{WinnersCount: len(res.Winners)}); err != nil {
			slog.Warn("giveaway: channel message", "err", err)
		} else {
			a.deliverChannel(ev, edge.Session{})
		}
	case stars.KindStarsGiveaway:
		var events []channels.Event
		res, err := a.stars.PayStarsGiveaway(ctx, me.ID, r.FormID, a.launchPost(me.ID, &events))
		if err != nil {
			return nil, err
		}
		balance = res.Balance
		users = append(users, me.ID)
		a.deliverChannel(events, edge.Session{})
	case stars.KindPremium:
		res, err := a.stars.PayPremium(ctx, me.ID, r.FormID)
		if err != nil {
			return nil, err
		}
		balance = res.Balance
		users = append(users, me.ID, res.Form.Recipient)
		if res.Form.Recipient != me.ID {
			text := fmt.Sprintf("🎁 Premium · %d months", res.Form.Months)
			if res.Form.Months == 1 {
				text = "🎁 Premium · 1 month"
			}
			act := &tg.MessageActionGiftPremium{Currency: "XTR", Amount: res.Form.Stars, Days: res.Form.Days}
			if res.Form.Message != "" {
				text += "\n" + res.Form.Message
				act.SetMessage(tg.TextWithEntities{Text: res.Form.Message, Entities: []tg.MessageEntityClass{}})
			}
			list, _, err := a.notice(ctx, me.ID, res.Form.Recipient, messages.Draft{Text: text, Action: act})
			if err != nil {
				return nil, err
			}
			up.Updates = append(up.Updates, list...)
		}
		// the recipient's apps learn they're Premium now
		a.pushUserChanged(res.Form.Recipient)
	case stars.KindStarGift:
		var events []channels.Event
		res, err := a.stars.PayGift(ctx, me.ID, r.FormID, me.Premium(time.Now()), a.channelGiftPost(&events))
		if err != nil {
			return nil, err
		}
		balance = res.Balance
		users = append(users, me.ID, res.Form.Recipient)
		if res.Form.Channel != 0 {
			// the buyer's reply doesn't carry the post, so their session gets it too
			a.deliverChannel(events, edge.Session{})
			chats = append(chats, res.Form.Channel)
			break
		}
		mine, theirs, err := a.starGiftActions(ctx, res)
		if err != nil {
			return nil, err
		}
		list, msgID, err := a.notice(ctx, me.ID, res.Form.Recipient, messages.Draft{Text: stars.GiftText(res.Gift, res.Form.Message), Action: mine, PeerAction: theirs})
		if err != nil {
			return nil, err
		}
		if err := a.stars.SetGiftMessage(ctx, res.ReceiptID, msgID); err != nil {
			return nil, err
		}
		up.Updates = append(up.Updates, list...)
	case stars.KindStarsGift:
		res, err := a.stars.PayStarsGift(ctx, me.ID, r.FormID)
		if err != nil {
			return nil, err
		}
		balance = res.Balance
		users = append(users, me.ID, res.Form.Recipient)
		act := &tg.MessageActionGiftStars{Currency: "XTR", Amount: res.Form.Stars, Stars: res.Form.Stars}
		act.SetTransactionID(res.TransactionID)
		list, _, err := a.notice(ctx, me.ID, res.Form.Recipient, messages.Draft{Text: fmt.Sprintf("⭐️ %d Stars", res.Form.Stars), Action: act})
		if err != nil {
			return nil, err
		}
		up.Updates = append(up.Updates, list...)
		a.pushBalance(res.Form.Recipient)
	default:
		return nil, tgerr.New(400, "FORM_ID_EMPTY")
	}
	up.Updates = append(up.Updates, &tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: balance}})
	if up.Users, err = a.usersFor(ctx, me.ID, users); err != nil {
		return nil, err
	}
	if up.Chats, err = a.chatsFor(ctx, me.ID, chats); err != nil {
		return nil, err
	}
	a.echo(ctx, me, []tg.UpdateClass{&tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: balance}}}, []tg.UserClass{})
	return &tg.PaymentsPaymentResult{Updates: up}, nil
}

// notice sends what a payment bought from one user to another. It returns
// the sender's journal entry and the message id in the recipient's box.
func (a *API) notice(ctx context.Context, from, to int64, d messages.Draft) ([]tg.UpdateClass, int, error) {
	s, err := a.msgs.Send(ctx, from, to, d)
	if err != nil {
		return nil, 0, err
	}
	id := s.Msg.ID
	for _, e := range s.Events {
		if nm, ok := e.Update.(*tg.UpdateNewMessage); ok && e.UserID == to {
			id = nm.Message.GetID()
		}
	}
	users, err := a.usersFor(ctx, from, messages.Users([]messages.Msg{s.Msg}))
	if err != nil {
		return nil, 0, err
	}
	a.echo(ctx, account.User{ID: from}, []tg.UpdateClass{s.Update}, users)
	a.deliver(s.Events, except(ctx))
	return []tg.UpdateClass{s.Update}, id, nil
}

// Notify is the wallet transfer's service message, outside an RPC: every
// session of the sender gets their copy.
func (a *API) Notify(ctx context.Context, t wallet.Transfer) error {
	act := &tg.MessageActionGiftTon{Currency: "USD", Amount: t.USDCents, CryptoCurrency: t.Asset, CryptoAmount: t.Nanos}
	act.SetTransactionID(t.ID)
	s, err := a.msgs.Send(ctx, t.From, t.To, messages.Draft{Text: t.Text, Action: act})
	if err != nil {
		return err
	}
	a.deliver(append(s.Events, messages.Event{UserID: t.From, Update: s.Update}), except(ctx))
	return nil
}

// PushStarsBalance is pushBalance for callers outside an RPC (buying Stars
// with the wallet happens over HTTP).
func (a *API) PushStarsBalance(user int64) { a.pushBalance(user) }

func (a *API) pushBalance(user int64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		bal, err := a.stars.Balance(ctx, user)
		if err != nil {
			slog.Warn("api: stars balance push", "err", err)
			return
		}
		a.push(ctx, user, &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: bal}}},
			Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: now()}, except(context.Background()))
	}()
}

// pushUserChanged makes a user's apps refetch themselves (Premium on/off).
func (a *API) pushUserChanged(user int64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		users, err := a.usersFor(ctx, user, []int64{user})
		if err != nil {
			return
		}
		a.push(ctx, user, &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateUser{UserID: clientID(user)}}, Users: users, Chats: []tg.ChatClass{}, Date: now()}, except(context.Background()))
	}()
}

func (a *API) getPremiumGiftCodeOptions(ctx context.Context, _ *tg.PaymentsGetPremiumGiftCodeOptionsRequest) (any, error) {
	if _, err := a.me(ctx); err != nil {
		return nil, err
	}
	out := []tg.PremiumGiftCodeOption{}
	// the plan's price times the people; a channel's "boosts via gifts" sheet
	// offers these counts, a plain gift only asks for one
	for _, users := range []int{1, 3, 5, 10, 25, 50, 100} {
		for _, p := range a.stars.Plans(ctx) {
			out = append(out, tg.PremiumGiftCodeOption{Users: users, Months: p.Months, Currency: "XTR", Amount: p.Stars * int64(users)})
		}
	}
	return out, nil
}

// giveawayChannel is the channel a giveaway is for, if me is an admin of it.
func (a *API) giveawayChannel(ctx context.Context, me account.User, peer tg.InputPeerClass) (int64, error) {
	v, ok := peer.(*tg.InputPeerChannel)
	if !ok {
		return 0, errChannelInvalid
	}
	_, m, err := a.chans.Access(ctx, v.ChannelID, me.ID)
	if err != nil {
		return 0, err
	}
	if m.Status != channels.Creator && m.Status != channels.Admin {
		return 0, tgerr.New(400, "CHAT_ADMIN_REQUIRED")
	}
	return v.ChannelID, nil
}

func (a *API) getStarsGiveawayOptions(ctx context.Context, _ *tg.PaymentsGetStarsGiveawayOptionsRequest) (any, error) {
	out := []tg.StarsGiveawayOption{}
	for _, o := range a.stars.StarsGiveawayOptions(ctx) {
		opt := tg.StarsGiveawayOption{Stars: o.Stars, YearlyBoosts: o.Boosts, Currency: "XTR", Amount: o.Stars, Extended: o.Stars > 10000}
		for _, n := range o.Winners {
			opt.Winners = append(opt.Winners, tg.StarsGiveawayWinnersOption{Users: n, PerUserStars: o.Stars / int64(n)})
		}
		out = append(out, opt)
	}
	return out, nil
}

// starsGiveawayForm is the payment form of a Stars giveaway. The prize comes
// out of the buyer's Stars, so unlike Telegram's the price is in XTR too.
func (a *API) starsGiveawayForm(ctx context.Context, me account.User, p *tg.InputStorePaymentStarsGiveaway) (any, error) {
	channel, err := a.giveawayChannel(ctx, me, p.BoostPeer)
	if err != nil {
		return nil, err
	}
	if p.Currency != "XTR" || p.Amount != p.Stars {
		return nil, tgerr.New(400, "INVOICE_INVALID")
	}
	opts, err := a.giveawayOptions(ctx, me, p.AdditionalPeers, p.CountriesISO2, p.PrizeDescription, p.OnlyNewSubscribers, p.WinnersAreVisible, p.UntilDate)
	if err != nil {
		return nil, err
	}
	f, err := a.stars.StarsGiveawayForm(ctx, me.ID, channel, p.Stars, p.Users, opts)
	if err != nil {
		return nil, err
	}
	bot, users, err := a.paymentBot(ctx, me.ID, stars.StarsAccount)
	if err != nil {
		return nil, err
	}
	// the payment sheet reads "buy <title> in <bot> for <price>"
	title := fmt.Sprintf("a giveaway for %d winners", p.Users)
	if p.Users == 1 {
		title = "a giveaway for 1 winner"
	}
	return &tg.PaymentsPaymentFormStars{FormID: f.ID, BotID: clientID(bot), Title: title, Description: "Stars giveaway, with boosts for your channel",
		Invoice: xtr(title, f.Stars), Users: users}, nil
}

// giveawayForm is the payment form of "get boosts via gifts": chosen people or
// a draw, for an admin of the channel.
func (a *API) giveawayForm(ctx context.Context, me account.User, inv *tg.InputInvoicePremiumGiftCode) (any, error) {
	var peer tg.InputPeerClass
	var chosen []int64
	var opts stars.GiveawayOptions
	switch p := inv.Purpose.(type) {
	case *tg.InputStorePaymentPremiumGiveaway:
		peer = p.BoostPeer
		var err error
		if opts, err = a.giveawayOptions(ctx, me, p.AdditionalPeers, p.CountriesISO2, p.PrizeDescription, p.OnlyNewSubscribers, p.WinnersAreVisible, p.UntilDate); err != nil {
			return nil, err
		}
	case *tg.InputStorePaymentPremiumGiftCode:
		peer = p.BoostPeer
		for _, u := range p.Users {
			id, err := a.inputUser(ctx, me, u)
			if err != nil {
				return nil, err
			}
			chosen = append(chosen, id)
		}
	default:
		return nil, tgerr.New(400, "INVOICE_INVALID")
	}
	channel, err := a.giveawayChannel(ctx, me, peer)
	if err != nil {
		return nil, err
	}
	f, err := a.stars.GiveawayForm(ctx, me.ID, channel, inv.Option.Months, inv.Option.Users, chosen, opts)
	if err != nil {
		return nil, err
	}
	bot, users, err := a.premiumBot(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	title := fmt.Sprintf("Premium for %d people", inv.Option.Users)
	return &tg.PaymentsPaymentFormStars{FormID: f.ID, BotID: clientID(bot), Title: title, Description: "Greengram Premium, with boosts for your channel",
		Invoice: xtr(title, f.Stars), Users: users}, nil
}

func (a *API) getPremiumPromo(ctx context.Context, _ *tg.HelpGetPremiumPromoRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	out := &tg.HelpPremiumPromo{StatusText: "Premium is not active on this account.", StatusEntities: []tg.MessageEntityClass{},
		VideoSections: []string{}, Videos: []tg.DocumentClass{}, PeriodOptions: []tg.PremiumSubscriptionOption{}, Users: []tg.UserClass{}}
	if me.PremiumUntil >= stars.UnlimitedPremium {
		out.StatusText = "Premium is active on this account."
	} else if me.Premium(time.Now()) {
		out.StatusText = "Premium is active until " + time.Unix(me.PremiumUntil, 0).UTC().Format("2006-01-02") + "."
	}
	// Stars only: the plans are priced in XTR. Promo videos come with media.
	for _, p := range a.stars.Plans(ctx) {
		out.PeriodOptions = append(out.PeriodOptions, tg.PremiumSubscriptionOption{Months: p.Months, Currency: "XTR", Amount: p.Stars,
			BotURL: a.in.BaseURL + "/premium?months=" + strconv.Itoa(p.Months)})
	}
	return out, nil
}
