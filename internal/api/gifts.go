package api

import (
	"context"
	"hash/crc32"
	"log/slog"
	"strconv"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/messages"
	"github.com/Greengrame/greengram-server/internal/stars"
)

func (a *API) registerGifts() {
	handle(a, tlprofile.SemanticMethodPaymentsGetStarGifts, a.getStarGifts)
	handle(a, tlprofile.SemanticMethodPaymentsCheckCanSendGift, a.checkCanSendGift)
	handle(a, tlprofile.SemanticMethodPaymentsGetSavedStarGifts, a.getSavedStarGifts)
	handle(a, tlprofile.SemanticMethodPaymentsGetSavedStarGift, a.getSavedStarGift)
	handle(a, tlprofile.SemanticMethodPaymentsSaveStarGift, a.saveStarGift)
	handle(a, tlprofile.SemanticMethodPaymentsConvertStarGift, a.convertStarGift)
	handle(a, tlprofile.SemanticMethodPaymentsTransferStarGift, a.transferStarGift)
	handle(a, tlprofile.SemanticMethodPaymentsToggleStarGiftsPinnedToTop, a.toggleStarGiftsPinnedToTop)
	a.registerCollectibles()
	a.registerMarket()
}

// giftArtwork finds what the gifts show. Clients can't draw a gift without a
// sticker (Web A dereferences it), so documentEmpty never goes out.
func (a *API) giftArtwork(ctx context.Context, list []stars.Gift) (map[int64]tg.DocumentClass, error) {
	in := make([]media.GiftArt, len(list))
	for i, g := range list {
		in[i] = media.GiftArt{Gift: g.ID, Sticker: g.StickerID, Emoji: g.Emoji}
	}
	docs, err := a.media.GiftStickers(ctx, in)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]tg.DocumentClass, len(docs))
	for id, d := range docs {
		out[id] = a.media.DocumentTL(d)
	}
	return out, nil
}

func starGift(g stars.Gift, art map[int64]tg.DocumentClass) *tg.StarGift {
	out := &tg.StarGift{ID: g.ID, Sticker: art[g.ID], Stars: g.Stars, ConvertStars: g.ConvertStars}
	out.SetTitle(g.Title)
	if g.PremiumOnly {
		out.SetRequirePremium(true)
	}
	if g.Limited {
		out.SetLimited(true)
		out.SetAvailabilityRemains(g.Stock)
		out.SetAvailabilityTotal(g.StockTotal)
		if g.Stock == 0 {
			out.SetSoldOut(true)
		}
	}
	if g.Upgradable() {
		out.SetUpgradeStars(g.UpgradeStars)
	}
	return out
}

// starGiftActions are the two copies of a gift's service message. Only the
// recipient's says it's on their profile; a gift to yourself is that copy.
func (a *API) starGiftActions(ctx context.Context, res stars.GiftResult) (mine, theirs tg.MessageActionClass, err error) {
	art, err := a.giftArtwork(ctx, []stars.Gift{res.Gift})
	if err != nil {
		return nil, nil, err
	}
	build := func(saved bool) *tg.MessageActionStarGift {
		act := &tg.MessageActionStarGift{Gift: starGift(res.Gift, art), NameHidden: res.Form.HideName, Saved: saved}
		if res.Gift.ConvertStars > 0 {
			act.SetConvertStars(res.Gift.ConvertStars)
		}
		// the recipient's copy says they may upgrade; one paid for says how much
		if saved && res.Gift.UpgradeSet != 0 {
			act.CanUpgrade = true
		}
		if res.Form.UpgradeStars > 0 {
			act.SetUpgradeStars(res.Form.UpgradeStars)
		}
		if res.Form.Message != "" {
			act.SetMessage(tg.TextWithEntities{Text: res.Form.Message, Entities: []tg.MessageEntityClass{}})
		}
		return act
	}
	theirs = build(true)
	if res.Form.Recipient == res.Form.User {
		return theirs, theirs, nil
	}
	return build(false), theirs, nil
}

// channelGiftPost posts a channel's gift in the payment's transaction: a
// post of the channel's, so it names the buyer only in the action, and not
// when they hid their name. Admins act on the gift from the post.
func (a *API) channelGiftPost(events *[]channels.Event) stars.GiftPost {
	return func(ctx context.Context, tx pgx.Tx, res stars.GiftResult) (int, error) {
		art, err := a.giftArtwork(ctx, []stars.Gift{res.Gift})
		if err != nil {
			return 0, err
		}
		act := &tg.MessageActionStarGift{Gift: starGift(res.Gift, art), NameHidden: res.Form.HideName, Saved: true,
			Peer: &tg.PeerChannel{ChannelID: res.Form.Channel}, SavedID: res.ReceiptID}
		if !res.Form.HideName {
			act.FromID = &tg.PeerUser{UserID: clientID(res.Form.User)}
		}
		if res.Gift.UpgradeSet != 0 {
			act.CanUpgrade = true
		}
		if res.Form.UpgradeStars > 0 {
			act.SetUpgradeStars(res.Form.UpgradeStars)
		}
		if res.Form.Message != "" {
			act.SetMessage(tg.TextWithEntities{Text: res.Form.Message, Entities: []tg.MessageEntityClass{}})
		}
		m, e, err := a.chans.PostTx(ctx, tx, res.Form.Channel, res.Form.User, act, media.Ref{}, 0)
		if err != nil {
			return 0, err
		}
		*events = append(*events, e)
		return m.ID, nil
	}
}

// changeGiftMessage records what the owner did with a gift on its message,
// so the bubble and a reload agree: in the owner's box, or a channel's post.
// A gift that came as plain text has nothing to change, and the money
// already moved, so a failure here is only logged.
func (a *API) changeGiftMessage(ctx context.Context, me account.User, h stars.Holder, msgID int, change func(*tg.MessageActionStarGift)) {
	if msgID == 0 {
		return
	}
	if h.Channel != 0 {
		ev, err := a.chans.ChangeAction(ctx, h.Channel, msgID, func(ac tg.MessageActionClass) bool {
			act, ok := ac.(*tg.MessageActionStarGift)
			if !ok {
				return false
			}
			change(act)
			act.Flags = 0
			return true
		})
		if err != nil {
			slog.Warn("api: channel gift message", "err", err)
			return
		}
		a.deliverChannel(ev, edge.Session{})
		return
	}
	if h.User != me.ID {
		return
	}
	list, err := a.msgs.Get(ctx, me.ID, []int{msgID})
	if err != nil || len(list) == 0 {
		return
	}
	act, ok := list[0].Action.(*tg.MessageActionStarGift)
	if !ok {
		return
	}
	change(act)
	// td's Encode only ever sets flag bits, a flag going false has to start over
	act.Flags = 0
	u, err := a.msgs.SetAction(ctx, me.ID, msgID, act)
	if err != nil {
		slog.Warn("api: gift message", "err", err)
		return
	}
	a.deliver([]messages.Event{{UserID: me.ID, Update: u}}, edge.Session{})
}

func (a *API) getStarGifts(ctx context.Context, r *tg.PaymentsGetStarGiftsRequest) (any, error) {
	if _, err := a.me(ctx); err != nil {
		return nil, err
	}
	list, err := a.stars.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	out := &tg.PaymentsStarGifts{Gifts: []tg.StarGiftClass{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
	art, err := a.giftArtwork(ctx, list)
	if err != nil {
		return nil, err
	}
	h := crc32.NewIEEE()
	for _, g := range list {
		out.Gifts = append(out.Gifts, starGift(g, art))
		h.Write([]byte(strconv.FormatInt(g.ID, 10) + ":" + strconv.Itoa(g.Stock) + ";"))
	}
	tiles, err := a.marketTiles(ctx, list, out.Gifts)
	if err != nil {
		return nil, err
	}
	h.Write([]byte(tiles))
	// always the full list: a client that lost its cache but kept the hash
	// would otherwise never see the catalog again
	out.Hash = int(h.Sum32() >> 1)
	return out, nil
}

func (a *API) checkCanSendGift(ctx context.Context, r *tg.PaymentsCheckCanSendGiftRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := a.stars.CheckGift(ctx, r.GiftID, me.Premium(time.Now())); err != nil {
		if e, ok := tgerr.As(err); ok {
			return &tg.PaymentsCheckCanSendGiftResultFail{Reason: tg.TextWithEntities{Text: e.Message, Entities: []tg.MessageEntityClass{}}}, nil
		}
		return nil, err
	}
	return &tg.PaymentsCheckCanSendGiftResultOk{}, nil
}

// savedStarGift is one gift on a profile. Whoever manages the holder gets
// what they need to act on it: ids, what it converts to, upgrade and
// transfer prices.
func savedStarGift(manage bool, r stars.Receipt, art, docs map[int64]tg.DocumentClass) tg.SavedStarGift {
	var gift tg.StarGiftClass = starGift(r.Gift, art)
	if r.Unique != nil {
		gift = uniqueTL(*r.Unique, docs)
	}
	out := tg.SavedStarGift{Date: r.Date, Gift: gift}
	// gifts.js hides the sender from everyone, owner included
	if !r.HideName {
		out.SetFromID(&tg.PeerUser{UserID: clientID(r.Sender)})
	}
	out.SetNameHidden(r.HideName)
	if r.Body != "" {
		out.SetMessage(tg.TextWithEntities{Text: r.Body, Entities: []tg.MessageEntityClass{}})
	}
	// everyone sees the pins: Web A floats them around the profile photo
	if r.Pinned != 0 {
		out.SetPinnedToTop(true)
	}
	if manage {
		if r.MessageID != 0 && r.Owner != 0 {
			out.SetMsgID(r.MessageID)
		}
		out.SetSavedID(r.ID)
		out.SetUnsaved(!r.Displayed)
		if r.Unique == nil {
			// a channel has no Stars balance to convert into
			if r.ConvertStars > 0 && r.Owner != 0 {
				out.SetConvertStars(r.ConvertStars)
			}
			if r.CanUpgrade() {
				out.SetCanUpgrade(true)
			}
			if r.UpgradePrepaid > 0 {
				out.SetUpgradeStars(r.UpgradePrepaid)
			}
		} else {
			if r.Gift.TransferStars > 0 {
				out.SetTransferStars(r.Gift.TransferStars)
			}
			out.SetCanResellAt(r.Date)
		}
		out.SetCanTransferAt(r.Date)
	}
	return out
}

// savedGifts renders receipts with their art and who they name.
func (a *API) savedGifts(ctx context.Context, me account.User, manage bool, list []stars.Receipt, out *tg.PaymentsSavedStarGifts) error {
	gifts := make([]stars.Gift, len(list))
	var uniques []*stars.Unique
	for i, g := range list {
		gifts[i] = g.Gift
		uniques = append(uniques, g.Unique)
	}
	art, err := a.giftArtwork(ctx, gifts)
	if err != nil {
		return err
	}
	docs, err := a.docsFor(ctx, uniqueDocIDs(uniques...))
	if err != nil {
		return err
	}
	var users, chats []int64
	for _, g := range list {
		out.Gifts = append(out.Gifts, savedStarGift(manage, g, art, docs))
		if !g.HideName {
			users = append(users, g.Sender)
		}
		users, chats = uniquePeers(g.Unique, users, chats)
	}
	if out.Users, err = a.usersFor(ctx, me.ID, users); err != nil {
		return err
	}
	out.Chats, err = a.chatsFor(ctx, me.ID, chats)
	return err
}

func (a *API) getSavedStarGifts(ctx context.Context, r *tg.PaymentsGetSavedStarGiftsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	h, err := a.giftHolder(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	out := &tg.PaymentsSavedStarGifts{Gifts: []tg.SavedStarGift{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
	// collections don't exist here
	if r.CollectionID != 0 {
		return out, nil
	}
	manage := a.manages(ctx, me, h)
	list, count, next, err := a.stars.SavedGifts(ctx, h, manage, stars.SavedQuery{ExcludeSaved: r.ExcludeSaved, ExcludeUnsaved: r.ExcludeUnsaved,
		ExcludeUnique: r.ExcludeUnique, ExcludeUnlimited: r.ExcludeUnlimited, ExcludeUpgradable: r.ExcludeUpgradable, ExcludeNotUpgradable: r.ExcludeUnupgradable,
		Offset: r.Offset, Limit: r.Limit})
	if err != nil {
		return nil, err
	}
	out.Count = count
	if err := a.savedGifts(ctx, me, manage, list, out); err != nil {
		return nil, err
	}
	if next != "" {
		out.SetNextOffset(next)
	}
	return out, nil
}

// ownedGift resolves an inputSavedStarGift to a gift me may act on: by the
// gift message in my box, by saved id on my profile or a channel's I manage,
// or a collectible by its slug.
func (a *API) ownedGift(ctx context.Context, me account.User, in tg.InputSavedStarGiftClass) (stars.Receipt, error) {
	switch v := in.(type) {
	case *tg.InputSavedStarGiftUser:
		return a.stars.OwnedReceipt(ctx, me.ID, 0, v.MsgID)
	case *tg.InputSavedStarGiftChat:
		h, err := a.giftHolder(ctx, me, v.Peer)
		if err != nil || !a.manages(ctx, me, h) {
			return stars.Receipt{}, errPeerInvalid
		}
		return a.stars.HeldReceipt(ctx, h, v.SavedID, 0)
	case *tg.InputSavedStarGiftSlug:
		r, err := a.stars.ReceiptBySlug(ctx, v.Slug)
		if err != nil {
			return r, err
		}
		if !a.manages(ctx, me, r.Holder()) {
			return stars.Receipt{}, tgerr.New(400, "SAVED_ID_EMPTY")
		}
		return r, nil
	}
	return stars.Receipt{}, tgerr.New(400, "SAVED_ID_EMPTY")
}

func (a *API) getSavedStarGift(ctx context.Context, r *tg.PaymentsGetSavedStarGiftRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	out := &tg.PaymentsSavedStarGifts{Gifts: []tg.SavedStarGift{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
	var list []stars.Receipt
	for _, in := range r.Stargift {
		g, err := a.ownedGift(ctx, me, in)
		if err != nil {
			continue
		}
		list = append(list, g)
	}
	if err := a.savedGifts(ctx, me, true, list, out); err != nil {
		return nil, err
	}
	out.Count = len(out.Gifts)
	return out, nil
}

func (a *API) saveStarGift(ctx context.Context, r *tg.PaymentsSaveStarGiftRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	g, err := a.ownedGift(ctx, me, r.Stargift)
	if err != nil {
		return nil, err
	}
	if err := a.stars.DisplayGift(ctx, g.Holder(), g.ID, !r.Unsave); err != nil {
		return nil, err
	}
	a.changeGiftMessage(ctx, me, g.Holder(), g.MessageID, func(act *tg.MessageActionStarGift) { act.Saved = !r.Unsave })
	return true, nil
}

func (a *API) convertStarGift(ctx context.Context, r *tg.PaymentsConvertStarGiftRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	g, err := a.ownedGift(ctx, me, r.Stargift)
	if err != nil {
		return nil, err
	}
	if g.UniqueID != 0 || g.Owner != me.ID {
		return nil, tgerr.New(400, "STARGIFT_CONVERT_UNAVAILABLE")
	}
	bal, err := a.stars.ConvertGift(ctx, me.ID, g.ID)
	if err != nil {
		return nil, err
	}
	a.echo(ctx, me, []tg.UpdateClass{&tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: bal}}}, []tg.UserClass{})
	a.changeGiftMessage(ctx, me, g.Holder(), g.MessageID, func(act *tg.MessageActionStarGift) { act.Converted, act.Saved = true, false })
	return true, nil
}

func (a *API) transferStarGift(ctx context.Context, r *tg.PaymentsTransferStarGiftRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	g, err := a.ownedGift(ctx, me, r.Stargift)
	if err != nil {
		return nil, err
	}
	if g.UniqueID != 0 {
		return a.transferUnique(ctx, me, g, r.ToID)
	}
	if g.Owner != me.ID {
		return nil, errPeerInvalid
	}
	to, err := a.peerUser(ctx, me, r.ToID)
	if err != nil {
		return nil, err
	}
	if !a.acceptsGift(ctx, to, me.ID) {
		return nil, errGiftPrivacy
	}
	if err := a.stars.TransferGift(ctx, me.ID, g.ID, to); err != nil {
		return nil, err
	}
	return emptyUpdates(), nil
}

// toggleStarGiftsPinnedToTop sets the profile's pinned gifts: the request
// lists all of them, in order, and the rest come off.
func (a *API) toggleStarGiftsPinnedToTop(ctx context.Context, r *tg.PaymentsToggleStarGiftsPinnedToTopRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	h, err := a.giftHolder(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if !a.manages(ctx, me, h) {
		return nil, errPeerInvalid
	}
	var ids []int64
	for _, in := range r.Stargift {
		g, err := a.ownedGift(ctx, me, in)
		if err != nil {
			return nil, err
		}
		if g.Holder() != h {
			return nil, tgerr.New(400, "SAVED_ID_EMPTY")
		}
		ids = append(ids, g.ID)
	}
	if err := a.stars.PinGifts(ctx, h, ids); err != nil {
		return nil, err
	}
	return true, nil
}
