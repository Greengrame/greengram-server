package api

import (
	"context"
	"strconv"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/messages"
)

// Methods the Android app needs that Web A got by without: editing asks
// getMessageEditData first, the sticker panel searches and reorders, mini
// apps ask for write access, and so on.
func (a *API) registerAndroid() {
	handle(a, tlprofile.SemanticMethodMessagesGetMessageEditData, a.getMessageEditData)
	handle(a, tlprofile.SemanticMethodChannelsExportMessageLink, a.exportMessageLink)
	handle(a, tlprofile.SemanticMethodContactsAcceptContact, a.acceptContact)

	handle(a, tlprofile.SemanticMethodMessagesGetFeaturedStickers, a.getFeaturedStickers)
	handle(a, tlprofile.SemanticMethodMessagesSearchStickerSets, func(ctx context.Context, r *tg.MessagesSearchStickerSetsRequest) (any, error) {
		return a.searchSets(ctx, media.SetStickers, r.Q, r.Hash)
	})
	handle(a, tlprofile.SemanticMethodMessagesSearchEmojiStickerSets, func(ctx context.Context, r *tg.MessagesSearchEmojiStickerSetsRequest) (any, error) {
		return a.searchSets(ctx, media.SetEmoji, r.Q, r.Hash)
	})
	handle(a, tlprofile.SemanticMethodMessagesSearchStickers, a.searchStickers)
	handle(a, tlprofile.SemanticMethodMessagesToggleStickerSets, a.toggleStickerSets)
	handle(a, tlprofile.SemanticMethodMessagesReorderStickerSets, a.reorderStickerSets)
	// stickers on photos (the "attached stickers" of a photo) aren't stored here
	handle(a, tlprofile.SemanticMethodMessagesGetAttachedStickers, func(ctx context.Context, _ *tg.MessagesGetAttachedStickersRequest) (any, error) {
		if _, err := a.me(ctx); err != nil {
			return nil, err
		}
		return []tg.StickerSetCoveredClass{}, nil
	})
	handle(a, tlprofile.SemanticMethodAccountGetDefaultBackgroundEmojis, a.getDefaultBackgroundEmojis)

	handle(a, tlprofile.SemanticMethodPaymentsGetStarsSubscriptions, a.getStarsSubscriptions)
	// no saved cards or shipping info are kept, so there is nothing to clear
	handle(a, tlprofile.SemanticMethodPaymentsClearSavedInfo, func(ctx context.Context, _ *tg.PaymentsClearSavedInfoRequest) (any, error) {
		if _, err := a.me(ctx); err != nil {
			return nil, err
		}
		return true, nil
	})

	handle(a, tlprofile.SemanticMethodBotsCanSendMessage, a.botCanSendMessage)
	handle(a, tlprofile.SemanticMethodBotsAllowSendMessage, a.botAllowSendMessage)
	handle(a, tlprofile.SemanticMethodMessagesSendWebViewData, a.sendWebViewData)
	// no login widget: Telegram's answer for a URL without one is "open it"
	handle(a, tlprofile.SemanticMethodMessagesRequestURLAuth, func(ctx context.Context, _ *tg.MessagesRequestURLAuthRequest) (any, error) {
		if _, err := a.me(ctx); err != nil {
			return nil, err
		}
		return &tg.URLAuthResultDefault{}, nil
	})

	handle(a, tlprofile.SemanticMethodContactsToggleTopPeers, func(ctx context.Context, r *tg.ContactsToggleTopPeersRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		return true, a.accounts.SetTopPeers(ctx, me.ID, r.Enabled)
	})
	handle(a, tlprofile.SemanticMethodAccountUpdatePersonalChannel, a.updatePersonalChannel)
	handle(a, tlprofile.SemanticMethodChannelsGetAdminedPublicChannels, a.getAdminedPublicChannels)

	// every group here is a supergroup: there is no basic chat to add to
	handle(a, tlprofile.SemanticMethodMessagesAddChatUser, func(ctx context.Context, _ *tg.MessagesAddChatUserRequest) (any, error) {
		if _, err := a.me(ctx); err != nil {
			return nil, err
		}
		return nil, tgerr.New(400, "CHAT_ID_INVALID")
	})
	// a creator who leaves stays the creator, nobody takes over
	handle(a, tlprofile.SemanticMethodMessagesGetFutureChatCreatorAfterLeave, func(ctx context.Context, r *tg.MessagesGetFutureChatCreatorAfterLeaveRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := a.peer(ctx, me, r.Peer); err != nil {
			return nil, err
		}
		return &tg.UserEmpty{}, nil
	})
}

func init() {
	for _, m := range []tlprofile.SemanticID{
		tlprofile.SemanticMethodMessagesGetMessageEditData,
		tlprofile.SemanticMethodChannelsExportMessageLink,
		tlprofile.SemanticMethodMessagesGetFeaturedStickers,
		tlprofile.SemanticMethodMessagesSearchStickerSets,
		tlprofile.SemanticMethodMessagesSearchEmojiStickerSets,
		tlprofile.SemanticMethodMessagesSearchStickers,
		tlprofile.SemanticMethodMessagesGetAttachedStickers,
		tlprofile.SemanticMethodAccountGetDefaultBackgroundEmojis,
		tlprofile.SemanticMethodPaymentsGetStarsSubscriptions,
		tlprofile.SemanticMethodBotsCanSendMessage,
		tlprofile.SemanticMethodMessagesGetFutureChatCreatorAfterLeave,
	} {
		frozenOK[m] = true
	}
}

// getMessageEditData: the Android app asks before every edit and drops the
// edit on an error, so it refuses only what editMessage would refuse.
func (a *API) getMessageEditData(ctx context.Context, r *tg.MessagesGetMessageEditDataRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	var m media.Ref
	if to.channel != 0 {
		msg, err := a.chans.Editable(ctx, me.ID, to.channel, r.ID)
		if err != nil {
			return nil, err
		}
		m = msg.Media
	} else {
		list, err := a.msgs.Get(ctx, me.ID, []int{r.ID})
		if err != nil {
			return nil, err
		}
		var found *messages.Msg
		for i := range list {
			if list[i].Peer == to.user {
				found = &list[i]
			}
		}
		if found == nil {
			return nil, tgerr.New(400, "MESSAGE_ID_INVALID")
		}
		if !found.Out || found.Action != nil || !found.Fwd.Empty() {
			return nil, tgerr.New(400, "MESSAGE_AUTHOR_REQUIRED")
		}
		m = found.Media
	}
	out := &tg.MessagesMessageEditData{}
	switch m.TL.(type) {
	case *tg.MessageMediaPhoto, *tg.MessageMediaDocument:
		out.Caption = true
	}
	return out, nil
}

// exportMessageLink makes the links the apps build themselves from
// me_url_prefix: <base>/u/<username>/<id>, or /u/c/<channel>/<id> for a
// private chat, with the topic in between for a forum.
func (a *API) exportMessageLink(ctx context.Context, r *tg.ChannelsExportMessageLinkRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	c, _, err := a.chans.Access(ctx, id, me.ID)
	if err != nil {
		return nil, err
	}
	list, err := a.chans.Messages(ctx, id, []int{r.ID})
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, tgerr.New(400, "MESSAGE_ID_INVALID")
	}
	m := list[0]
	link := a.in.BaseURL + "/u/"
	if c.Username != "" {
		link += c.Username
	} else {
		link += "c/" + strconv.FormatInt(c.ID, 10)
	}
	if r.Thread && c.Forum && m.Topic != 0 && m.Topic != m.ID {
		link += "/" + strconv.Itoa(m.Topic)
	}
	link += "/" + strconv.Itoa(m.ID)
	if m.Grouped != 0 && !r.Grouped {
		link += "?single"
	}
	return &tg.ExportedMessageLink{Link: link}, nil
}

// acceptContact is the "add contact" bar: the person goes into my contacts
// under their own name, and the bar goes away.
func (a *API) acceptContact(ctx context.Context, r *tg.ContactsAcceptContactRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := a.inputUser(ctx, me, r.ID)
	if err != nil {
		return nil, err
	}
	u, ok, err := a.accounts.User(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, tgerr.New(400, "USER_ID_INVALID")
	}
	first, last := splitName(u.Name)
	if err := a.msgs.AddContact(ctx, me.ID, messages.Contact{UserID: id, FirstName: first, LastName: last}); err != nil {
		return nil, err
	}
	settings, err := a.peerSettings(ctx, me, id)
	if err != nil {
		return nil, err
	}
	users, err := a.usersFor(ctx, me.ID, []int64{id})
	if err != nil {
		return nil, err
	}
	up := &tg.UpdatePeerSettings{Peer: &tg.PeerUser{UserID: clientID(id)}, Settings: settings}
	return &tg.Updates{Updates: []tg.UpdateClass{up}, Users: users, Chats: []tg.ChatClass{}, Date: now()}, nil
}

var errNoMedia = tgerr.New(400, "MEDIA_INVALID")

// coveredSets shows each set with its first stickers, as Telegram's
// featured and search results do.
func (a *API) coveredSets(ctx context.Context, sets []media.Set) ([]tg.StickerSetCoveredClass, error) {
	ids := make([]int64, len(sets))
	for i, s := range sets {
		ids[i] = s.ID
	}
	covers, err := a.media.Covers(ctx, ids, 5)
	if err != nil {
		return nil, err
	}
	out := make([]tg.StickerSetCoveredClass, 0, len(sets))
	for _, s := range sets {
		docs := []tg.DocumentClass{}
		for _, d := range covers[s.ID] {
			docs = append(docs, a.media.DocumentTL(d))
		}
		out = append(out, &tg.StickerSetMultiCovered{Set: a.media.SetTL(s), Covers: docs})
	}
	return out, nil
}

// setsHash changes whenever a set comes, goes or changes.
func setsHash(sets []media.Set) int64 {
	vals := make([]int64, 0, 2*len(sets))
	for _, s := range sets {
		vals = append(vals, s.ID, int64(s.Hash))
	}
	return media.ListHash(vals)
}

// getFeaturedStickers is the trending tab: the instance's public packs,
// most installed first. The Android app asks every second while this fails.
func (a *API) getFeaturedStickers(ctx context.Context, r *tg.MessagesGetFeaturedStickersRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if a.media == nil {
		return &tg.MessagesFeaturedStickers{Sets: []tg.StickerSetCoveredClass{}, Unread: []int64{}}, nil
	}
	sets, err := a.media.Featured(ctx, me.ID, media.SetStickers, 100)
	if err != nil {
		return nil, err
	}
	h := setsHash(sets)
	if r.Hash != 0 && r.Hash == h {
		return &tg.MessagesFeaturedStickersNotModified{Count: len(sets)}, nil
	}
	covered, err := a.coveredSets(ctx, sets)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesFeaturedStickers{Hash: h, Count: len(sets), Sets: covered, Unread: []int64{}}, nil
}

func (a *API) searchSets(ctx context.Context, kind, q string, hash int64) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if a.media == nil {
		return &tg.MessagesFoundStickerSets{Sets: []tg.StickerSetCoveredClass{}}, nil
	}
	sets, err := a.media.SearchSets(ctx, me.ID, kind, q, 50)
	if err != nil {
		return nil, err
	}
	h := setsHash(sets)
	if hash != 0 && hash == h {
		return &tg.MessagesFoundStickerSetsNotModified{}, nil
	}
	covered, err := a.coveredSets(ctx, sets)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesFoundStickerSets{Hash: h, Sets: covered}, nil
}

// searchStickers matches the emoji the app derived from the query (its
// keyword lists) and pack titles against q.
func (a *API) searchStickers(ctx context.Context, r *tg.MessagesSearchStickersRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if a.media == nil {
		return &tg.MessagesFoundStickers{Stickers: []tg.DocumentClass{}}, nil
	}
	kind := media.SetStickers
	if r.Emojis {
		kind = media.SetEmoji
	}
	limit := r.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if r.Offset < 0 {
		return nil, tgerr.New(400, "OFFSET_INVALID")
	}
	items, more, err := a.media.SearchStickers(ctx, me.ID, kind, r.Q, r.Emoticon, r.Offset, limit)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(items))
	docs := make([]tg.DocumentClass, len(items))
	for i, it := range items {
		ids[i] = it.Doc.ID
		docs[i] = a.media.DocumentTL(it.Doc)
	}
	h := media.ListHash(ids)
	if r.Hash != 0 && r.Hash == h {
		out := &tg.MessagesFoundStickersNotModified{}
		if more {
			out.SetNextOffset(r.Offset + len(items))
		}
		return out, nil
	}
	out := &tg.MessagesFoundStickers{Hash: h, Stickers: docs}
	if more {
		out.SetNextOffset(r.Offset + len(items))
	}
	return out, nil
}

// toggleStickerSets is the sticker settings' bulk remove and archive.
// Nothing is kept archived here, so archiving removes the set too.
func (a *API) toggleStickerSets(ctx context.Context, r *tg.MessagesToggleStickerSetsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if a.media == nil {
		return nil, errNoMedia
	}
	on := r.Unarchive && !r.Uninstall && !r.Archive
	if !on && !r.Uninstall && !r.Archive {
		return nil, tgerr.New(400, "INPUT_REQUEST_INVALID")
	}
	for _, in := range r.Stickersets {
		set, _, ok, err := a.media.StickerSet(ctx, me.ID, in)
		if err != nil {
			return nil, err
		}
		if !ok || set.Builtin != "" {
			continue
		}
		if err := a.media.Install(ctx, me.ID, set.ID, on); err != nil {
			return nil, err
		}
	}
	return true, nil
}

func (a *API) reorderStickerSets(ctx context.Context, r *tg.MessagesReorderStickerSetsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if a.media == nil || r.Masks { // no mask sets here
		return true, nil
	}
	kind := media.SetStickers
	if r.Emojis {
		kind = media.SetEmoji
	}
	return true, a.media.Reorder(ctx, me.ID, kind, r.Order)
}

// getDefaultBackgroundEmojis offers the default emoji packs' covers as name
// and reply backgrounds, as the status picker does.
func (a *API) getDefaultBackgroundEmojis(ctx context.Context, r *tg.AccountGetDefaultBackgroundEmojisRequest) (any, error) {
	if _, err := a.me(ctx); err != nil {
		return nil, err
	}
	if a.media == nil {
		return &tg.EmojiList{DocumentID: []int64{}}, nil
	}
	set, items, err := a.media.DefaultStatusSet(ctx)
	if err != nil {
		return nil, err
	}
	h := int64(set.Hash)
	if r.Hash != 0 && r.Hash == h {
		return &tg.EmojiListNotModified{}, nil
	}
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.Doc.ID
	}
	return &tg.EmojiList{Hash: h, DocumentID: ids}, nil
}

// getStarsSubscriptions: nobody subscribes with Stars here yet, so it's
// the balance and an empty, finished list.
func (a *API) getStarsSubscriptions(ctx context.Context, r *tg.PaymentsGetStarsSubscriptionsRequest) (any, error) {
	me, err := a.selfPeer(ctx, r.Peer)
	if err != nil {
		return nil, err
	}
	var bal int64
	if a.stars != nil {
		if bal, err = a.stars.Balance(ctx, me.ID); err != nil {
			return nil, err
		}
	}
	out := starsStatus(bal)
	out.SetSubscriptions([]tg.StarsSubscription{})
	return out, nil
}

// botMayWrite is the Bot API's rule (botapi writable): a bot writes to
// people who have a chat with it.
func (a *API) botMayWrite(ctx context.Context, me account.User, in tg.InputUserClass) (int64, bool, error) {
	if a.bots == nil {
		return 0, false, errBotInvalid
	}
	b, err := a.bot(ctx, me, in)
	if err != nil {
		return 0, false, err
	}
	if b.BuiltIn() {
		return b.UserID, true, nil
	}
	ok, err := a.msgs.HasDialog(ctx, b.UserID, me.ID)
	return b.UserID, ok, err
}

func (a *API) botCanSendMessage(ctx context.Context, r *tg.BotsCanSendMessageRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	_, ok, err := a.botMayWrite(ctx, me, r.Bot)
	return ok, err
}

// botAllowSendMessage is a mini app's requestWriteAccess: the note in the
// chat opens it for the bot, as on Telegram.
func (a *API) botAllowSendMessage(ctx context.Context, r *tg.BotsAllowSendMessageRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	bot, ok, err := a.botMayWrite(ctx, me, r.Bot)
	if err != nil {
		return nil, err
	}
	if ok {
		return emptyUpdates(), nil
	}
	s, err := a.msgs.Send(ctx, me.ID, bot, messages.Draft{Action: &tg.MessageActionBotAllowed{FromRequest: true}})
	if err != nil {
		return nil, err
	}
	events := append([]messages.Event{{UserID: me.ID, Update: s.Update}}, s.Events...)
	return a.privateOp(ctx, me, bot, []tg.UpdateClass{s.Update}, events)
}

// sendWebViewData is a keyboard button's mini app handing the bot data:
// the chat shows the button's text, the bot gets the data (web_app_data).
func (a *API) sendWebViewData(ctx context.Context, r *tg.MessagesSendWebViewDataRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if a.bots == nil {
		return nil, errBotInvalid
	}
	b, err := a.bot(ctx, me, r.Bot)
	if err != nil {
		return nil, err
	}
	if !b.Usable(me.ID) {
		return nil, errBotRestricted
	}
	if r.Data == "" || len(r.Data) > 4096 || !utf8.ValidString(r.Data) {
		return nil, tgerr.New(400, "DATA_INVALID")
	}
	if r.ButtonText == "" || utf8.RuneCountInString(r.ButtonText) > 256 {
		return nil, tgerr.New(400, "BUTTON_TEXT_INVALID")
	}
	if blocked, _, err := a.msgs.Blocks(ctx, me.ID, b.UserID); err != nil {
		return nil, err
	} else if blocked {
		return nil, tgerr.New(400, "YOU_BLOCKED_USER")
	}
	s, err := a.msgs.Send(ctx, me.ID, b.UserID, messages.Draft{RandomID: r.RandomID,
		Action:     &tg.MessageActionWebViewDataSent{Text: r.ButtonText},
		PeerAction: &tg.MessageActionWebViewDataSentMe{Text: r.ButtonText, Data: r.Data}})
	if err != nil {
		return nil, err
	}
	events := append([]messages.Event{{UserID: me.ID, Update: s.Update}}, s.Events...)
	up, err := a.privateOp(ctx, me, b.UserID, []tg.UpdateClass{s.Update}, events)
	if err != nil {
		return nil, err
	}
	if r.RandomID != 0 {
		up.Updates = append([]tg.UpdateClass{&tg.UpdateMessageID{ID: s.Msg.ID, RandomID: r.RandomID}}, up.Updates...)
	}
	return up, nil
}

// updatePersonalChannel puts one of my public broadcast channels on my
// profile, or with inputChannelEmpty takes it off.
func (a *API) updatePersonalChannel(ctx context.Context, r *tg.AccountUpdatePersonalChannelRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if _, empty := r.Channel.(*tg.InputChannelEmpty); empty {
		return true, a.accounts.SetPersonalChannel(ctx, me.ID, 0)
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	c, ok, err := a.chans.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok || c.Deleted || c.Megagroup || c.Creator != me.ID || c.Username == "" {
		return nil, errChannelInvalid
	}
	return true, a.accounts.SetPersonalChannel(ctx, me.ID, id)
}

// personalChannel fills the profile's channel while it is still public and
// still theirs, and returns it for the reply's chats.
func (a *API) personalChannel(ctx context.Context, viewer, user int64, full *tg.UserFull) ([]tg.ChatClass, error) {
	id, err := a.accounts.PersonalChannel(ctx, user)
	if err != nil || id == 0 {
		return []tg.ChatClass{}, err
	}
	c, ok, err := a.chans.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok || c.Deleted || c.Megagroup || c.Creator != user || c.Username == "" {
		return []tg.ChatClass{}, nil
	}
	chats, err := a.chatsFor(ctx, viewer, []int64{id})
	if err != nil || len(chats) == 0 {
		return []tg.ChatClass{}, err
	}
	full.SetPersonalChannelID(id)
	if c.TopID > 0 {
		full.SetPersonalChannelMessage(c.TopID)
	}
	return chats, nil
}

// getAdminedPublicChannels: the public chats I made. The personal channel
// picker asks with for_personal, for broadcast channels only.
func (a *API) getAdminedPublicChannels(ctx context.Context, r *tg.ChannelsGetAdminedPublicChannelsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if r.ByLocation || r.ForCommunityPeer {
		return &tg.MessagesChats{Chats: []tg.ChatClass{}}, nil
	}
	list, err := a.chans.OwnedPublic(ctx, me.ID, r.ForPersonal)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(list))
	for i, c := range list {
		ids[i] = c.ID
	}
	chats, err := a.chatsFor(ctx, me.ID, ids)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesChats{Chats: chats}, nil
}
