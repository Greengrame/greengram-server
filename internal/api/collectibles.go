package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/messages"
	"github.com/Greengrame/greengram-server/internal/stars"
)

// Collectible gifts: upgrading a gift into a numbered piece of a collection,
// moving one to a person or a channel, and its page by slug.

// previewSample is how many of each attribute the upgrade sheet cycles
// through; "view all" asks for the rest.
const previewSample = 12

func (a *API) registerCollectibles() {
	handle(a, tlprofile.SemanticMethodPaymentsGetStarGiftUpgradePreview, a.getStarGiftUpgradePreview)
	handle(a, tlprofile.SemanticMethodPaymentsGetStarGiftUpgradeAttributes, a.getStarGiftUpgradeAttributes)
	handle(a, tlprofile.SemanticMethodPaymentsUpgradeStarGift, a.upgradeStarGift)
	handle(a, tlprofile.SemanticMethodPaymentsGetUniqueStarGift, a.getUniqueStarGift)
}

func holderPeer(h stars.Holder) tg.PeerClass {
	if h.Channel != 0 {
		return &tg.PeerChannel{ChannelID: h.Channel}
	}
	return &tg.PeerUser{UserID: clientID(h.User)}
}

func rarity(permille int) tg.StarGiftAttributeRarityClass {
	return &tg.StarGiftAttributeRarity{Permille: permille}
}

// docsFor renders the documents by id; a missing one is documentEmpty, which
// Web A drops along with its attribute.
func (a *API) docsFor(ctx context.Context, ids []int64) (map[int64]tg.DocumentClass, error) {
	out := map[int64]tg.DocumentClass{}
	if len(ids) == 0 {
		return out, nil
	}
	docs, err := a.media.Documents(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		out[d.ID] = a.media.DocumentTL(d)
	}
	for _, id := range ids {
		if out[id] == nil {
			out[id] = &tg.DocumentEmpty{ID: id}
		}
	}
	return out, nil
}

func uniqueDocIDs(list ...*stars.Unique) []int64 {
	var ids []int64
	for _, u := range list {
		if u != nil {
			ids = append(ids, u.Model.Document, u.Pattern.Document)
		}
	}
	return ids
}

func backdropTL(b stars.Backdrop) *tg.StarGiftAttributeBackdrop {
	return &tg.StarGiftAttributeBackdrop{Name: b.Name, BackdropID: b.ID, CenterColor: b.Center, EdgeColor: b.Edge, PatternColor: b.Pattern,
		TextColor: b.Text, Rarity: rarity(b.Permille)}
}

// uniqueTL: an unlimited collection has a total of 0. Our Web A says "N issued"
// for it; there's no number that reads right as a total.
func uniqueTL(u stars.Unique, docs map[int64]tg.DocumentClass) *tg.StarGiftUnique {
	out := &tg.StarGiftUnique{ID: u.ID, GiftID: u.GiftID, Title: u.Title, Slug: u.Slug, Num: u.Num,
		AvailabilityIssued: u.Issued, AvailabilityTotal: u.Supply}
	if !u.Holder.IsZero() {
		out.SetOwnerID(holderPeer(u.Holder))
	}
	// on sale: a listing's price or an offer's; unset otherwise, or Web A
	// offers to take it off sale
	if u.Resale > 0 {
		out.SetResellAmount(resellAmount(u.Resale))
	}
	out.Attributes = []tg.StarGiftAttributeClass{
		&tg.StarGiftAttributeModel{Name: u.Model.Name, Document: docs[u.Model.Document], Rarity: rarity(u.Model.Permille)},
		&tg.StarGiftAttributePattern{Name: u.Pattern.Name, Document: docs[u.Pattern.Document], Rarity: rarity(u.Pattern.Permille)},
		backdropTL(u.Backdrop),
	}
	if u.Original && (u.OriginalRecipient != 0 || u.OriginalChannel != 0) {
		var to tg.PeerClass = &tg.PeerUser{UserID: clientID(u.OriginalRecipient)}
		if u.OriginalChannel != 0 {
			to = &tg.PeerChannel{ChannelID: u.OriginalChannel}
		}
		od := &tg.StarGiftAttributeOriginalDetails{RecipientID: to, Date: u.OriginalDate}
		if u.OriginalSender != 0 {
			od.SetSenderID(&tg.PeerUser{UserID: clientID(u.OriginalSender)})
		}
		if u.OriginalMessage != "" {
			od.SetMessage(tg.TextWithEntities{Text: u.OriginalMessage, Entities: []tg.MessageEntityClass{}})
		}
		out.Attributes = append(out.Attributes, od)
	}
	return out
}

// uniquePeers is who a collectible names: its holder and the people in its
// original details.
func uniquePeers(u *stars.Unique, users, chats []int64) ([]int64, []int64) {
	if u == nil {
		return users, chats
	}
	if u.Holder.Channel != 0 {
		chats = append(chats, u.Holder.Channel)
	} else if u.Holder.User != 0 {
		users = append(users, u.Holder.User)
	}
	if u.Original {
		for _, id := range []int64{u.OriginalSender, u.OriginalRecipient} {
			if id != 0 {
				users = append(users, id)
			}
		}
		if u.OriginalChannel != 0 {
			chats = append(chats, u.OriginalChannel)
		}
	}
	return users, chats
}

// managesChannel is who may act for a channel's gifts: Web A shows the
// buttons to admins who can post.
func (a *API) managesChannel(ctx context.Context, me account.User, channel int64) bool {
	c, m, err := a.chans.Access(ctx, channel, me.ID)
	if err != nil || c.Megagroup {
		return false
	}
	return m.Status == channels.Creator || m.Status == channels.Admin && m.AdminRights.PostMessages
}

func (a *API) manages(ctx context.Context, me account.User, h stars.Holder) bool {
	if h.Channel != 0 {
		return a.managesChannel(ctx, me, h.Channel)
	}
	return h.User == me.ID
}

// giftHolder is the profile a gifts request names: a person or a channel.
func (a *API) giftHolder(ctx context.Context, me account.User, p tg.InputPeerClass) (stars.Holder, error) {
	switch v := p.(type) {
	case *tg.InputPeerChannel:
		return a.channelHolder(ctx, me, v.ChannelID)
	case *tg.InputPeerChannelFromMessage:
		return a.channelHolder(ctx, me, v.ChannelID)
	}
	id, err := a.peerUser(ctx, me, p)
	return stars.Holder{User: id}, err
}

func (a *API) channelHolder(ctx context.Context, me account.User, id int64) (stars.Holder, error) {
	c, _, err := a.chans.Access(ctx, id, me.ID)
	if err != nil {
		return stars.Holder{}, err
	}
	if c.Megagroup {
		return stars.Holder{}, errPeerInvalid
	}
	return stars.Holder{Channel: id}, nil
}

// giftDestination is where a transfer may go: a person who takes gifts from
// me, or a broadcast channel I can see.
func (a *API) giftDestination(ctx context.Context, me account.User, p tg.InputPeerClass) (stars.Holder, error) {
	h, err := a.giftHolder(ctx, me, p)
	if err != nil {
		return h, err
	}
	if h.User != 0 && h.User != me.ID && !a.acceptsGift(ctx, h.User, me.ID) {
		return h, errGiftPrivacy
	}
	return h, nil
}

func (a *API) getUniqueStarGift(ctx context.Context, r *tg.PaymentsGetUniqueStarGiftRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	u, err := a.stars.UniqueBySlug(ctx, r.Slug)
	if errors.Is(err, stars.ErrUniqueNotFound) {
		u, err = a.offerUnique(ctx, r.Slug)
	}
	if err != nil {
		return nil, err
	}
	docs, err := a.docsFor(ctx, uniqueDocIDs(&u))
	if err != nil {
		return nil, err
	}
	users, chats := uniquePeers(&u, nil, nil)
	out := &tg.PaymentsUniqueStarGift{Gift: uniqueTL(u, docs)}
	if out.Users, err = a.usersFor(ctx, me.ID, users); err != nil {
		return nil, err
	}
	if out.Chats, err = a.chatsFor(ctx, me.ID, chats); err != nil {
		return nil, err
	}
	return out, nil
}

// upgradeAttributes is a catalog gift's collection, all of it or a sample.
func (a *API) upgradeAttributes(ctx context.Context, giftID int64, sample int) ([]tg.StarGiftAttributeClass, error) {
	g, err := a.stars.CatalogGift(ctx, giftID)
	if err != nil || g.UpgradeSet == 0 {
		return nil, tgerr.New(400, "STARGIFT_INVALID")
	}
	models, patterns, backdrops, err := a.stars.Attributes(ctx, g.UpgradeSet)
	if err != nil {
		return nil, err
	}
	if sample > 0 {
		rand.Shuffle(len(models), func(i, j int) { models[i], models[j] = models[j], models[i] })
		rand.Shuffle(len(patterns), func(i, j int) { patterns[i], patterns[j] = patterns[j], patterns[i] })
		rand.Shuffle(len(backdrops), func(i, j int) { backdrops[i], backdrops[j] = backdrops[j], backdrops[i] })
		models, patterns, backdrops = models[:min(sample, len(models))], patterns[:min(sample, len(patterns))], backdrops[:min(sample, len(backdrops))]
	}
	var ids []int64
	for _, m := range models {
		ids = append(ids, m.Document)
	}
	for _, p := range patterns {
		ids = append(ids, p.Document)
	}
	docs, err := a.docsFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := []tg.StarGiftAttributeClass{}
	for _, m := range models {
		out = append(out, &tg.StarGiftAttributeModel{Name: m.Name, Document: docs[m.Document], Rarity: rarity(m.Permille)})
	}
	for _, p := range patterns {
		out = append(out, &tg.StarGiftAttributePattern{Name: p.Name, Document: docs[p.Document], Rarity: rarity(p.Permille)})
	}
	for _, b := range backdrops {
		out = append(out, backdropTL(b))
	}
	return out, nil
}

// getStarGiftUpgradePreview: one fixed price, so no schedule of cheaper ones.
func (a *API) getStarGiftUpgradePreview(ctx context.Context, r *tg.PaymentsGetStarGiftUpgradePreviewRequest) (any, error) {
	if _, err := a.me(ctx); err != nil {
		return nil, err
	}
	attrs, err := a.upgradeAttributes(ctx, r.GiftID, previewSample)
	if err != nil {
		return nil, err
	}
	return &tg.PaymentsStarGiftUpgradePreview{SampleAttributes: attrs, Prices: []tg.StarGiftUpgradePrice{}, NextPrices: []tg.StarGiftUpgradePrice{}}, nil
}

func (a *API) getStarGiftUpgradeAttributes(ctx context.Context, r *tg.PaymentsGetStarGiftUpgradeAttributesRequest) (any, error) {
	if _, err := a.me(ctx); err != nil {
		return nil, err
	}
	attrs, err := a.upgradeAttributes(ctx, r.GiftID, 0)
	if err != nil {
		return nil, err
	}
	return &tg.PaymentsStarGiftUpgradeAttributes{Attributes: attrs}, nil
}

// upgradeStarGift is the free upgrade: one the sender paid for with the gift.
func (a *API) upgradeStarGift(ctx context.Context, r *tg.PaymentsUpgradeStarGiftRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	// mine, or a channel's I manage (ownedGift checked)
	g, err := a.ownedGift(ctx, me, r.Stargift)
	if err != nil {
		return nil, err
	}
	res, err := a.stars.Upgrade(ctx, me.ID, g.Holder(), g.ID, r.KeepOriginalDetails)
	if err != nil {
		return nil, err
	}
	up := &tg.Updates{Updates: []tg.UpdateClass{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: now()}
	users, chats, err := a.upgraded(ctx, me, res, up)
	if err != nil {
		return nil, err
	}
	if up.Users, err = a.usersFor(ctx, me.ID, users); err != nil {
		return nil, err
	}
	if up.Chats, err = a.chatsFor(ctx, me.ID, chats); err != nil {
		return nil, err
	}
	return up, nil
}

// upgraded sends the upgrade's service message, in the chat the gift came
// from (a channel's in the channel), and points the old gift message at it.
// The money already moved, so a message that can't go out is only logged.
func (a *API) upgraded(ctx context.Context, me account.User, res stars.UpgradeResult, up *tg.Updates) ([]int64, []int64, error) {
	r := res.Receipt
	docs, err := a.docsFor(ctx, uniqueDocIDs(&res.Unique))
	if err != nil {
		return nil, nil, err
	}
	if r.OwnerChannel != 0 {
		return a.upgradedInChannel(ctx, me, res, docs)
	}
	mine := &tg.MessageActionStarGiftUnique{Upgrade: true, Gift: uniqueTL(res.Unique, docs), Saved: r.Displayed, CanTransferAt: r.Date}
	if r.Gift.TransferStars > 0 {
		mine.SetTransferStars(r.Gift.TransferStars)
	}
	if r.UpgradePrepaid > 0 {
		mine.PrepaidUpgrade = true
	}
	theirs := &tg.MessageActionStarGiftUnique{Upgrade: true, Gift: uniqueTL(res.Unique, docs), PrepaidUpgrade: r.UpgradePrepaid > 0}
	// who it came from, unless they hid their name
	if !r.HideName && r.Sender != 0 && r.Sender != me.ID {
		mine.SetFromID(&tg.PeerUser{UserID: clientID(r.Sender)})
		theirs.SetFromID(&tg.PeerUser{UserID: clientID(r.Sender)})
	}
	// Web A names who turned a prepaid gift into a collectible from peer, and
	// peer shares its flag with saved_id: the owner's saved gift
	if theirs.PrepaidUpgrade {
		theirs.Peer = &tg.PeerUser{UserID: clientID(me.ID)}
		theirs.SavedID = r.ID
	}
	text := fmt.Sprintf("🎁 %s #%d", res.Unique.Title, res.Unique.Num)
	to := r.Sender
	if to == 0 {
		to = me.ID
	}
	users := []int64{me.ID, to}
	s, err := a.msgs.Send(ctx, me.ID, to, messages.Draft{Text: text, Action: mine, PeerAction: theirs})
	if err != nil && to != me.ID {
		// the sender is gone or won't take messages: Saved Messages still
		// shows the owner what they got
		slog.Warn("api: upgrade message", "err", err)
		s, err = a.msgs.Send(ctx, me.ID, me.ID, messages.Draft{Text: text, Action: mine})
	}
	if err != nil {
		slog.Warn("api: upgrade message", "err", err)
		return users, nil, nil
	}
	if err := a.stars.SetGiftMessage(ctx, r.ID, s.Msg.ID); err != nil {
		slog.Warn("api: upgrade message id", "err", err)
	}
	a.changeGiftMessage(ctx, me, r.Holder(), r.MessageID, func(act *tg.MessageActionStarGift) {
		act.Upgraded, act.CanUpgrade = true, false
		act.SetUpgradeMsgID(s.Msg.ID)
	})
	list, err := a.usersFor(ctx, me.ID, messages.Users([]messages.Msg{s.Msg}))
	if err == nil {
		a.echo(ctx, me, []tg.UpdateClass{s.Update}, list)
	}
	a.deliver(s.Events, except(ctx))
	up.Updates = append(up.Updates, s.Update)
	return users, nil, nil
}

// upgradedInChannel is upgraded for a channel's gift: a post of the
// channel's, which its admins act on, and the gift's post marked upgraded.
func (a *API) upgradedInChannel(ctx context.Context, me account.User, res stars.UpgradeResult, docs map[int64]tg.DocumentClass) ([]int64, []int64, error) {
	r := res.Receipt
	act := &tg.MessageActionStarGiftUnique{Upgrade: true, Gift: uniqueTL(res.Unique, docs), Saved: r.Displayed, CanTransferAt: r.Date,
		PrepaidUpgrade: r.UpgradePrepaid > 0, Peer: &tg.PeerChannel{ChannelID: r.OwnerChannel}, SavedID: r.ID}
	if r.Gift.TransferStars > 0 {
		act.SetTransferStars(r.Gift.TransferStars)
	}
	// Web A says from_id turned it into a collectible: the sender when they
	// paid for that with the gift, else the admin who paid now. Without one
	// it names the channel and the card reads "Saved Gift".
	users, chats := []int64{me.ID}, []int64{r.OwnerChannel}
	act.FromID = &tg.PeerUser{UserID: clientID(me.ID)}
	if r.UpgradePrepaid > 0 && !r.HideName && r.Sender != 0 {
		act.FromID = &tg.PeerUser{UserID: clientID(r.Sender)}
		users = append(users, r.Sender)
	}
	m, ev, err := a.chans.PostAsChannel(ctx, r.OwnerChannel, me.ID, act)
	if err != nil {
		slog.Warn("api: channel upgrade message", "err", err)
		return users, chats, nil
	}
	a.deliverChannel(ev, edge.Session{})
	old := r.MessageID
	if err := a.stars.SetGiftMessage(ctx, r.ID, m.ID); err != nil {
		slog.Warn("api: channel upgrade message id", "err", err)
	}
	a.changeGiftMessage(ctx, me, r.Holder(), old, func(act *tg.MessageActionStarGift) {
		act.Upgraded, act.CanUpgrade = true, false
		act.SetUpgradeMsgID(m.ID)
	})
	return users, chats, nil
}

// transferred tells both sides: the new owner's chat with the sender, or the
// channel's history.
func (a *API) transferred(ctx context.Context, me account.User, res stars.GiftTransfer, up *tg.Updates) ([]int64, []int64, error) {
	r := res.Receipt
	docs, err := a.docsFor(ctx, uniqueDocIDs(&res.Unique))
	if err != nil {
		return nil, nil, err
	}
	users := []int64{me.ID}
	if res.To.Channel != 0 {
		act := &tg.MessageActionStarGiftUnique{Transferred: true, Gift: uniqueTL(res.Unique, docs), Saved: true, FromID: &tg.PeerUser{UserID: clientID(me.ID)},
			Peer: &tg.PeerChannel{ChannelID: res.To.Channel}, SavedID: r.ID, CanTransferAt: r.Date}
		if r.Gift.TransferStars > 0 {
			act.SetTransferStars(r.Gift.TransferStars)
		}
		ev, err := a.chans.PostService(ctx, res.To.Channel, me.ID, act)
		if err != nil {
			slog.Warn("api: transfer message", "err", err)
		} else {
			a.deliverChannel(ev, edge.Session{})
		}
		return users, []int64{res.To.Channel}, nil
	}
	theirs := &tg.MessageActionStarGiftUnique{Transferred: true, Gift: uniqueTL(res.Unique, docs), Saved: true, FromID: &tg.PeerUser{UserID: clientID(me.ID)},
		CanTransferAt: r.Date}
	if r.Gift.TransferStars > 0 {
		theirs.SetTransferStars(r.Gift.TransferStars)
	}
	mine := &tg.MessageActionStarGiftUnique{Transferred: true, Gift: uniqueTL(res.Unique, docs)}
	if res.To.User == me.ID {
		mine = theirs
	}
	users = append(users, res.To.User)
	list, msgID, err := a.notice(ctx, me.ID, res.To.User, messages.Draft{Text: fmt.Sprintf("🎁 %s #%d", res.Unique.Title, res.Unique.Num), Action: mine, PeerAction: theirs})
	if err != nil {
		slog.Warn("api: transfer message", "err", err)
		return users, nil, nil
	}
	if err := a.stars.SetGiftMessage(ctx, r.ID, msgID); err != nil {
		slog.Warn("api: transfer message id", "err", err)
	}
	up.Updates = append(up.Updates, list...)
	return users, nil, nil
}

// afterTransfer is what changes for others: a status taken off.
func (a *API) afterTransfer(res stars.GiftTransfer) {
	if res.StatusCleared {
		a.pushUserChanged(res.From.User)
	}
}

// transferUnique is transferStarGift for a collectible.
func (a *API) transferUnique(ctx context.Context, me account.User, g stars.Receipt, toPeer tg.InputPeerClass) (any, error) {
	to, err := a.giftDestination(ctx, me, toPeer)
	if err != nil {
		return nil, err
	}
	res, err := a.stars.TransferUnique(ctx, me.ID, g.Holder(), g.ID, to)
	if err != nil {
		return nil, err
	}
	up := &tg.Updates{Updates: []tg.UpdateClass{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: now()}
	users, chats, err := a.transferred(ctx, me, res, up)
	if err != nil {
		return nil, err
	}
	a.afterTransfer(res)
	if up.Users, err = a.usersFor(ctx, me.ID, users); err != nil {
		return nil, err
	}
	if up.Chats, err = a.chatsFor(ctx, me.ID, chats); err != nil {
		return nil, err
	}
	return up, nil
}

// upgradeForm and transferForm are getPaymentForm for the two invoices.
func (a *API) upgradeForm(ctx context.Context, me account.User, inv *tg.InputInvoiceStarGiftUpgrade) (any, error) {
	g, err := a.ownedGift(ctx, me, inv.Stargift)
	if err != nil {
		return nil, err
	}
	f, gift, err := a.stars.UpgradeForm(ctx, me.ID, g.Holder(), g.ID, inv.KeepOriginalDetails)
	if err != nil {
		return nil, err
	}
	return &tg.PaymentsPaymentFormStarGift{FormID: f.ID, Invoice: xtr(gift.Title, f.Stars)}, nil
}

func (a *API) transferForm(ctx context.Context, me account.User, inv *tg.InputInvoiceStarGiftTransfer) (any, error) {
	g, err := a.ownedGift(ctx, me, inv.Stargift)
	if err != nil {
		return nil, err
	}
	to, err := a.giftDestination(ctx, me, inv.ToID)
	if err != nil {
		return nil, err
	}
	f, gift, err := a.stars.TransferForm(ctx, me.ID, g.Holder(), g.ID, to)
	if err != nil {
		return nil, err
	}
	return &tg.PaymentsPaymentFormStarGift{FormID: f.ID, Invoice: xtr(gift.Title, f.Stars)}, nil
}

// payUpgrade and payTransfer are sendStarsForm's two cases. They add the
// payer's updates to up and return who and what they name.
func (a *API) payUpgrade(ctx context.Context, me account.User, formID int64, up *tg.Updates) (int64, []int64, []int64, error) {
	receipt, err := a.stars.FormReceipt(ctx, me.ID, formID)
	if err != nil {
		return 0, nil, nil, err
	}
	h, err := a.stars.ReceiptHolder(ctx, receipt)
	if err != nil {
		return 0, nil, nil, err
	}
	// a channel's admin who lost the right since the form can't pay it
	if !a.manages(ctx, me, h) {
		return 0, nil, nil, tgerr.New(400, "SAVED_ID_EMPTY")
	}
	res, err := a.stars.PayUpgrade(ctx, me.ID, formID, h)
	if err != nil {
		return 0, nil, nil, err
	}
	users, chats, err := a.upgraded(ctx, me, res, up)
	return res.Balance, users, chats, err
}

func (a *API) payTransfer(ctx context.Context, me account.User, formID int64, up *tg.Updates) (int64, []int64, []int64, error) {
	receipt, err := a.stars.FormReceipt(ctx, me.ID, formID)
	if err != nil {
		return 0, nil, nil, err
	}
	from, err := a.stars.ReceiptHolder(ctx, receipt)
	if err != nil {
		return 0, nil, nil, err
	}
	if !a.manages(ctx, me, from) {
		return 0, nil, nil, tgerr.New(400, "SAVED_ID_EMPTY")
	}
	res, err := a.stars.PayTransfer(ctx, me.ID, formID, from)
	if err != nil {
		return 0, nil, nil, err
	}
	users, chats, err := a.transferred(ctx, me, res, up)
	a.afterTransfer(res)
	return res.Balance, users, chats, err
}

// getCollectibleEmojiStatuses are the collectibles the user may wear: every
// one they hold.
func (a *API) getCollectibleEmojiStatuses(ctx context.Context, r *tg.AccountGetCollectibleEmojiStatusesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	out := &tg.AccountEmojiStatuses{Statuses: []tg.EmojiStatusClass{}}
	if a.stars == nil {
		return out, nil
	}
	list, err := a.stars.UniquesHeldBy(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, u := range list {
		// on sale, it can't be worn
		if u.Resale > 0 {
			continue
		}
		out.Statuses = append(out.Statuses, collectibleStatus(account.CollectibleStatus{ID: u.ID, Document: u.ModelEmoji, Pattern: u.PatternEmoji,
			Title: fmt.Sprintf("%s #%d", u.Title, u.Num), Slug: u.Slug, Center: u.Backdrop.Center, Edge: u.Backdrop.Edge, PatternColor: u.Backdrop.Pattern,
			Text: u.Backdrop.Text}))
		ids = append(ids, u.ID)
	}
	out.Hash = media.ListHash(ids)
	if r.Hash != 0 && r.Hash == out.Hash {
		return &tg.AccountEmojiStatusesNotModified{}, nil
	}
	return out, nil
}
