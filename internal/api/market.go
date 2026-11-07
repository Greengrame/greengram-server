package api

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strconv"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/messages"
	"github.com/Greengrame/greengram-server/internal/stars"
)

// The collectible market: a collection's resale screen (listings and the
// instance's offers), buying off it, and owners listing what they hold.

func (a *API) registerMarket() {
	handle(a, tlprofile.SemanticMethodPaymentsGetResaleStarGifts, a.getResaleStarGifts)
	handle(a, tlprofile.SemanticMethodPaymentsUpdateStarGiftPrice, a.updateStarGiftPrice)
}

// resellAmount is a price as Web A reads it: Stars, no nanos, so the form
// it gets later compares equal.
func resellAmount(stars int64) []tg.StarsAmountClass {
	return []tg.StarsAmountClass{&tg.StarsAmount{Amount: stars}}
}

// attributesHash says whether the client's filter lists are current. It
// is never 0, which is what a client without them sends.
func attributesHash(set int64, p stars.ResalePage) int64 {
	h := fnv.New64a()
	fmt.Fprint(h, set)
	for _, m := range p.Models {
		fmt.Fprintf(h, "|m%s:%d:%d", m.Name, m.Document, m.Permille)
	}
	for _, m := range p.Patterns {
		fmt.Fprintf(h, "|p%s:%d:%d", m.Name, m.Document, m.Permille)
	}
	for _, b := range p.Backdrops {
		fmt.Fprintf(h, "|b%s:%d:%d", b.Name, b.ID, b.Permille)
	}
	return int64(h.Sum64()>>1) | 1
}

func (a *API) getResaleStarGifts(ctx context.Context, r *tg.PaymentsGetResaleStarGiftsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	out := &tg.PaymentsResaleStarGifts{Gifts: []tg.StarGiftClass{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
	// nothing is crafted here
	if r.ForCraft {
		return out, nil
	}
	q := stars.ResaleQuery{GiftID: r.GiftID, SortByPrice: r.SortByPrice, SortByNum: r.SortByNum, Offset: r.Offset, Limit: r.Limit}
	for _, at := range r.Attributes {
		switch v := at.(type) {
		case *tg.StarGiftAttributeIDModel:
			q.Models = append(q.Models, v.DocumentID)
		case *tg.StarGiftAttributeIDPattern:
			q.Patterns = append(q.Patterns, v.DocumentID)
		case *tg.StarGiftAttributeIDBackdrop:
			q.Backdrops = append(q.Backdrops, v.BackdropID)
		}
	}
	page, err := a.stars.ResaleGifts(ctx, q)
	if err != nil {
		return nil, err
	}
	hash := attributesHash(r.GiftID, page)
	withAttrs := r.AttributesHash != hash
	var ids []int64
	for i := range page.Gifts {
		ids = append(ids, uniqueDocIDs(&page.Gifts[i])...)
	}
	if withAttrs {
		for _, m := range page.Models {
			ids = append(ids, m.Document)
		}
		for _, p := range page.Patterns {
			ids = append(ids, p.Document)
		}
	}
	docs, err := a.docsFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	out.Count = page.Count
	var users, chats []int64
	for i := range page.Gifts {
		out.Gifts = append(out.Gifts, uniqueTL(page.Gifts[i], docs))
		users, chats = uniquePeers(&page.Gifts[i], users, chats)
	}
	if page.Next != "" {
		out.SetNextOffset(page.Next)
	}
	if withAttrs {
		attrs := []tg.StarGiftAttributeClass{}
		for _, m := range page.Models {
			attrs = append(attrs, &tg.StarGiftAttributeModel{Name: m.Name, Document: docs[m.Document], Rarity: rarity(m.Permille)})
		}
		for _, p := range page.Patterns {
			attrs = append(attrs, &tg.StarGiftAttributePattern{Name: p.Name, Document: docs[p.Document], Rarity: rarity(p.Permille)})
		}
		for _, b := range page.Backdrops {
			attrs = append(attrs, backdropTL(b))
		}
		out.SetAttributes(attrs)
		out.SetAttributesHash(hash)
	}
	// every time, filtered or not: Web A builds its menus from these
	counters := []tg.StarGiftAttributeCounter{}
	for i, m := range page.Models {
		counters = append(counters, tg.StarGiftAttributeCounter{Attribute: &tg.StarGiftAttributeIDModel{DocumentID: m.Document}, Count: page.ModelCount[i]})
	}
	for i, p := range page.Patterns {
		counters = append(counters, tg.StarGiftAttributeCounter{Attribute: &tg.StarGiftAttributeIDPattern{DocumentID: p.Document}, Count: page.PatternCount[i]})
	}
	for i, b := range page.Backdrops {
		counters = append(counters, tg.StarGiftAttributeCounter{Attribute: &tg.StarGiftAttributeIDBackdrop{BackdropID: b.ID}, Count: page.BackdropCount[i]})
	}
	out.SetCounters(counters)
	if out.Users, err = a.usersFor(ctx, me.ID, users); err != nil {
		return nil, err
	}
	if out.Chats, err = a.chatsFor(ctx, me.ID, chats); err != nil {
		return nil, err
	}
	return out, nil
}

// updateStarGiftPrice lists one of my collectibles, or with 0 takes it off.
func (a *API) updateStarGiftPrice(ctx context.Context, r *tg.PaymentsUpdateStarGiftPriceRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	var price int64
	switch v := r.ResellAmount.(type) {
	case *tg.StarsAmount:
		if v.Nanos != 0 || v.Amount < 0 {
			return nil, tgerr.New(400, "STARGIFT_RESELL_AMOUNT_INVALID")
		}
		price = v.Amount
	case *tg.StarsTonAmount:
		// taking a TON listing off is the only TON there is, and there are none
		if v.Amount != 0 {
			return nil, stars.ErrResaleCurrency
		}
	default:
		return nil, stars.ErrResaleCurrency
	}
	g, err := a.ownedGift(ctx, me, r.Stargift)
	if err != nil {
		return nil, err
	}
	res, err := a.stars.SetResalePrice(ctx, g.Holder(), g.ID, price)
	if err != nil {
		return nil, err
	}
	if res.StatusCleared {
		a.pushUserChanged(me.ID)
	}
	return emptyUpdates(), nil
}

// resaleForm is getPaymentForm for a collectible off the market, for me or
// for someone I'm giving it to.
func (a *API) resaleForm(ctx context.Context, me account.User, inv *tg.InputInvoiceStarGiftResale) (any, error) {
	if inv.Ton {
		return nil, stars.ErrResaleCurrency
	}
	to, err := a.giftDestination(ctx, me, inv.ToID)
	if err != nil {
		return nil, err
	}
	f, u, err := a.stars.ResaleForm(ctx, me.ID, to, inv.Slug, inv.Message.Text, !inv.ShowName)
	if err != nil {
		return nil, err
	}
	return &tg.PaymentsPaymentFormStarGift{FormID: f.ID, Invoice: xtr(u.Title, f.Stars)}, nil
}

// payResale is sendStarsForm for a resale form: the money and the gift
// move together, then everyone involved hears about it.
func (a *API) payResale(ctx context.Context, me account.User, formID int64, up *tg.Updates) (int64, []int64, []int64, error) {
	res, err := a.stars.PayResale(ctx, me.ID, formID)
	if err != nil {
		return 0, nil, nil, err
	}
	users, chats := a.resold(ctx, me, res, up)
	if res.Seller != 0 {
		a.pushBalance(res.Seller)
		if res.StatusCleared {
			a.pushUserChanged(res.Seller)
		}
	}
	return res.Balance, users, chats, nil
}

// resold sends a purchase's service messages. From a seller, the buyer's
// chat with them says what each side paid and got (Web A: "You paid" and
// "You received ... from selling"). The gift then reaches whoever it was
// for: the buyer's own copy of that message, Saved Messages for an offer
// bought for themselves, the recipient's chat, or the channel's history.
// The money already moved, so a message that can't go out is only logged.
func (a *API) resold(ctx context.Context, me account.User, res stars.ResaleResult, up *tg.Updates) ([]int64, []int64) {
	r := res.Receipt
	f := res.Form
	docs, err := a.docsFor(ctx, uniqueDocIDs(&res.Unique))
	if err != nil {
		slog.Warn("api: resale message", "err", err)
		return []int64{me.ID}, nil
	}
	gift := func() tg.StarGiftClass { return uniqueTL(res.Unique, docs) }
	text := fmt.Sprintf("🎁 %s #%d", res.Unique.Title, res.Unique.Num)
	held := func(act *tg.MessageActionStarGiftUnique) *tg.MessageActionStarGiftUnique {
		act.Saved = true
		act.SetCanTransferAt(r.Date)
		act.SetCanResellAt(r.Date)
		if r.Gift.TransferStars > 0 {
			act.SetTransferStars(r.Gift.TransferStars)
		}
		return act
	}
	users, chats := []int64{me.ID}, []int64(nil)
	link := func(msgID int) {
		if err := a.stars.SetGiftMessage(ctx, r.ID, msgID); err != nil {
			slog.Warn("api: resale message id", "err", err)
		}
	}
	send := func(to int64, d messages.Draft) (int, int, bool) {
		s, err := a.msgs.Send(ctx, me.ID, to, d)
		if err != nil {
			slog.Warn("api: resale message", "err", err)
			return 0, 0, false
		}
		theirs := s.Msg.ID
		for _, e := range s.Events {
			if nm, ok := e.Update.(*tg.UpdateNewMessage); ok && e.UserID == to {
				theirs = nm.Message.GetID()
			}
		}
		if list, err := a.usersFor(ctx, me.ID, messages.Users([]messages.Msg{s.Msg})); err == nil {
			a.echo(ctx, me, []tg.UpdateClass{s.Update}, list)
		}
		a.deliver(s.Events, except(ctx))
		up.Updates = append(up.Updates, s.Update)
		return s.Msg.ID, theirs, true
	}
	paid := &tg.MessageActionStarGiftUnique{Gift: gift()}
	paid.SetResaleAmount(&tg.StarsAmount{Amount: f.Stars})
	forMe := res.To == stars.Holder{User: me.ID}
	// Web A's gift sheet takes From from here, else from whoever sent the
	// message: the buyer. The gift still names who gave it.
	if forMe && r.HideName {
		paid.NameHidden = true
	} else if forMe && r.Sender != 0 {
		paid.SetFromID(&tg.PeerUser{UserID: clientID(r.Sender)})
		users = append(users, r.Sender)
	}
	if res.Seller != 0 {
		users = append(users, res.Seller)
		got := &tg.MessageActionStarGiftUnique{Gift: gift()}
		got.SetResaleAmount(&tg.StarsAmount{Amount: res.Net})
		if forMe {
			held(paid)
		}
		if mine, _, ok := send(res.Seller, messages.Draft{Text: text, Action: paid, PeerAction: got}); ok && forMe {
			link(mine)
		}
		if forMe {
			return users, chats
		}
		// the buyer's side is told; what reaches the recipient is a gift
		paid = &tg.MessageActionStarGiftUnique{Transferred: true, Gift: gift()}
	} else if forMe {
		if mine, _, ok := send(me.ID, messages.Draft{Text: text, Action: held(paid)}); ok {
			link(mine)
		}
		return users, chats
	}
	given := held(&tg.MessageActionStarGiftUnique{Transferred: true, Gift: gift()})
	if !f.HideName {
		given.SetFromID(&tg.PeerUser{UserID: clientID(me.ID)})
	}
	if f.Message != "" {
		given.SetMessage(tg.TextWithEntities{Text: f.Message, Entities: []tg.MessageEntityClass{}})
	}
	if res.To.Channel != 0 {
		given.SetPeer(&tg.PeerChannel{ChannelID: res.To.Channel})
		given.SetSavedID(r.ID)
		chats = append(chats, res.To.Channel)
		ev, err := a.chans.PostService(ctx, res.To.Channel, me.ID, given)
		if err != nil {
			slog.Warn("api: resale channel message", "err", err)
		} else {
			a.deliverChannel(ev, edge.Session{})
		}
		return users, chats
	}
	users = append(users, res.To.User)
	if _, theirs, ok := send(res.To.User, messages.Draft{Text: text, Action: paid, PeerAction: given}); ok {
		link(theirs)
	}
	return users, chats
}

// marketTiles marks the shop's gifts that have a market: how many are on
// sale and from what price. A gift sold only as variants shows as sold out,
// so Web A opens its resale screen and nothing else.
func (a *API) marketTiles(ctx context.Context, list []stars.Gift, out []tg.StarGiftClass) (string, error) {
	tiles, err := a.stars.Tiles(ctx, list)
	if err != nil {
		return "", err
	}
	sum := ""
	for i, g := range list {
		t, ok := tiles[g.ID]
		if !ok {
			continue
		}
		sg := out[i].(*tg.StarGift)
		if t.Count > 0 {
			sg.SetAvailabilityResale(t.Count)
			sg.SetResellMinStars(t.MinStars)
		}
		if t.Variants {
			total := g.StockTotal
			if !g.Limited {
				total = max(int(t.Count), 1)
			}
			sg.SetLimited(true)
			sg.SetSoldOut(true)
			sg.SetAvailabilityRemains(0)
			sg.SetAvailabilityTotal(total)
		}
		sum += strconv.FormatInt(g.ID, 10) + ":" + strconv.FormatInt(t.Count, 10) + ":" + strconv.FormatInt(t.MinStars, 10) + ";"
	}
	return sum, nil
}

// offerUnique is getUniqueStarGift for a slug no collectible has yet: one
// of the market's offers, while it's on offer.
func (a *API) offerUnique(ctx context.Context, slug string) (stars.Unique, error) {
	u, err := a.stars.ResaleItem(ctx, slug)
	if err != nil || !u.Offer {
		return u, stars.ErrUniqueNotFound
	}
	return u, nil
}

// marketAppConfig is what Web A's listing sheet reads: the price range and
// the seller's share in permille (it shows "you'll receive" from it).
func (a *API) marketAppConfig(ctx context.Context) []tg.JSONObjectValue {
	if a.stars == nil {
		return nil
	}
	cut, err := a.stars.Commission(ctx)
	if err != nil {
		slog.Warn("api: market commission", "err", err)
		return nil
	}
	return []tg.JSONObjectValue{
		{Key: "stars_stargift_resale_amount_min", Value: &tg.JSONNumber{Value: stars.ResaleMinStars}},
		{Key: "stars_stargift_resale_amount_max", Value: &tg.JSONNumber{Value: stars.ResaleMaxStars}},
		{Key: "stars_stargift_resale_commission_permille", Value: &tg.JSONNumber{Value: float64(1000 - cut)}},
	}
}
