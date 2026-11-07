package api

import (
	"context"
	"strings"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/messages"
)

func init() {
	for _, m := range []tlprofile.SemanticID{
		tlprofile.SemanticMethodMessagesGetCommonChats,
		tlprofile.SemanticMethodContactsResolvePhone,
		tlprofile.SemanticMethodUsersGetRequirementsToContact,
		tlprofile.SemanticMethodContactsGetSponsoredPeers,
		tlprofile.SemanticMethodChannelsCheckSearchPostsFlood,
		tlprofile.SemanticMethodChannelsSearchPosts,
	} {
		frozenOK[m] = true
	}
}

// registerPeople is finding people and chats: search, top peers, common
// chats, birthdays, the stranger bar.
func (a *API) registerPeople() {
	handle(a, tlprofile.SemanticMethodContactsSearch, a.contactsSearch)
	handle(a, tlprofile.SemanticMethodMessagesSearchGlobal, a.searchGlobal)
	// no daily quota here. Without this Web A's Posts tab asks for Premium
	// and hashtag search spins forever.
	handle(a, tlprofile.SemanticMethodChannelsCheckSearchPostsFlood, func(context.Context, *tg.ChannelsCheckSearchPostsFloodRequest) (any, error) {
		return &tg.SearchPostsFlood{QueryIsFree: true, TotalDaily: searchPostsDaily, Remains: searchPostsDaily}, nil
	})
	handle(a, tlprofile.SemanticMethodChannelsSearchPosts, a.searchPosts)
	handle(a, tlprofile.SemanticMethodMessagesGetCommonChats, a.commonChats)
	handle(a, tlprofile.SemanticMethodContactsGetTopPeers, a.topPeers)
	handle(a, tlprofile.SemanticMethodContactsResetTopPeerRating, func(ctx context.Context, r *tg.ContactsResetTopPeerRatingRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		cat := topCategory(r.Category)
		if cat == "" {
			return true, nil
		}
		to, err := a.peer(ctx, me, r.Peer)
		if err != nil {
			return nil, err
		}
		peer := to.user
		if to.channel != 0 {
			peer = -to.channel
		}
		return true, a.msgs.ResetTopPeer(ctx, me.ID, cat, peer)
	})
	handle(a, tlprofile.SemanticMethodContactsGetBirthdays, a.birthdays)
	handle(a, tlprofile.SemanticMethodMessagesGetPeerSettings, func(ctx context.Context, r *tg.MessagesGetPeerSettingsRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		to, err := a.peer(ctx, me, r.Peer)
		if err != nil {
			return nil, err
		}
		out := &tg.MessagesPeerSettings{Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
		if to.user != 0 {
			if out.Settings, err = a.peerSettings(ctx, me, to.user); err != nil {
				return nil, err
			}
			out.Users, err = a.usersFor(ctx, me.ID, []int64{to.user})
		}
		return out, err
	})
	handle(a, tlprofile.SemanticMethodMessagesHidePeerSettingsBar, func(ctx context.Context, r *tg.MessagesHidePeerSettingsBarRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		to, err := a.peer(ctx, me, r.Peer)
		if err != nil || to.user == 0 {
			return true, nil
		}
		return true, a.msgs.HidePeerSettings(ctx, me.ID, to.user)
	})
	handle(a, tlprofile.SemanticMethodContactsResolvePhone, func(ctx context.Context, r *tg.ContactsResolvePhoneRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		u, ok, err := a.accounts.UserByPhone(ctx, r.Phone)
		if err != nil {
			return nil, err
		}
		// "who can find me by my number" decides, like importContacts
		if !ok || u.Disabled || u.IsSystem || !a.canSee(ctx, u.ID, me.ID, "addByPhone") {
			return nil, tgerr.New(400, "PHONE_NOT_OCCUPIED")
		}
		users, err := a.usersFor(ctx, me.ID, []int64{u.ID})
		if err != nil {
			return nil, err
		}
		return &tg.ContactsResolvedPeer{Peer: &tg.PeerUser{UserID: tgUserID(u)}, Users: users, Chats: []tg.ChatClass{}}, nil
	})
	// nobody here charges for messages or wants premium to be written to
	handle(a, tlprofile.SemanticMethodUsersGetRequirementsToContact, func(_ context.Context, r *tg.UsersGetRequirementsToContactRequest) (any, error) {
		out := make([]tg.RequirementToContactClass, len(r.ID))
		for i := range out {
			out[i] = &tg.RequirementToContactEmpty{}
		}
		return out, nil
	})
	handle(a, tlprofile.SemanticMethodContactsGetSponsoredPeers, func(context.Context, *tg.ContactsGetSponsoredPeersRequest) (any, error) {
		return &tg.ContactsSponsoredPeersEmpty{}, nil
	})
}

// peerSettings is the bar over a private chat: someone not in the book
// who wrote first gets "add contact" and "block", plus "report spam"
// until the owner answers. Bots and system accounts get none.
func (a *API) peerSettings(ctx context.Context, me account.User, peer int64) (tg.PeerSettings, error) {
	var out tg.PeerSettings
	if peer == me.ID {
		return out, nil
	}
	all, err := a.accounts.Users(ctx, []int64{peer})
	if err != nil {
		return out, err
	}
	u, ok := all[peer]
	if !ok || u.IsBot || u.IsSystem {
		return out, nil
	}
	st, err := a.msgs.StrangerState(ctx, me.ID, peer)
	if err != nil || st.Contact || st.Hidden || !st.Incoming {
		return out, err
	}
	blocked, _, err := a.msgs.Blocks(ctx, me.ID, peer)
	if err != nil || blocked {
		return out, err
	}
	out.AddContact, out.BlockContact = true, true
	out.ReportSpam = !st.Replied
	return out, nil
}

func (a *API) contactsSearch(ctx context.Context, r *tg.ContactsSearchRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	out := &tg.ContactsFound{MyResults: []tg.PeerClass{}, Results: []tg.PeerClass{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
	limit := min(max(r.Limit, 1), 50)
	q := strings.TrimSpace(r.Q)
	known, err := a.msgs.FindKnown(ctx, me.ID, q, limit)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{me.ID: true}
	var who []int64
	for _, id := range known {
		seen[id] = true
		who = append(who, id)
		out.MyResults = append(out.MyResults, &tg.PeerUser{UserID: clientID(id)})
	}
	mine, public, err := a.chans.FindChats(ctx, me.ID, q, limit)
	if err != nil {
		return nil, err
	}
	for _, id := range mine {
		out.MyResults = append(out.MyResults, &tg.PeerChannel{ChannelID: id})
	}
	found, err := a.accounts.SearchUsernames(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	for _, u := range found {
		if seen[u.ID] {
			continue
		}
		seen[u.ID] = true
		who = append(who, u.ID)
		out.Results = append(out.Results, &tg.PeerUser{UserID: tgUserID(u)})
	}
	for _, id := range public {
		out.Results = append(out.Results, &tg.PeerChannel{ChannelID: id})
	}
	if out.Users, err = a.usersFor(ctx, me.ID, who); err != nil {
		return nil, err
	}
	out.Chats, err = a.chatsFor(ctx, me.ID, append(mine, public...))
	return out, err
}

// searchGlobal merges the private box and the groups and channels the
// user is in, newest first, paged by (offset_rate, offset_peer, offset_id).
func (a *API) searchGlobal(ctx context.Context, r *tg.MessagesSearchGlobalRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	kinds, ok := filterKinds(r.Filter)
	if !ok || r.Q == "" && len(kinds) == 0 {
		return &tg.MessagesMessages{Messages: []tg.MessageClass{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}, nil
	}
	limit := min(max(r.Limit, 1), 100)
	pq := messages.GlobalQuery{Text: r.Q, Kinds: kinds, BeforeDate: r.OffsetRate, AfterID: r.OffsetID, MinDate: r.MinDate, MaxDate: r.MaxDate, Limit: limit}
	cq := channels.GlobalQuery{Text: r.Q, Kinds: kinds, BeforeDate: r.OffsetRate, AfterID: r.OffsetID, MinDate: r.MinDate, MaxDate: r.MaxDate, Limit: limit,
		Broadcasts: r.BroadcastsOnly, Groups: r.GroupsOnly}
	if r.OffsetPeer != nil {
		if to, err := a.peer(ctx, me, r.OffsetPeer); err == nil {
			pq.AfterPeer, cq.AfterChan = to.user, to.channel
		}
	}
	var (
		mine  []messages.Msg
		chans []channels.Msg
		total int
	)
	if !r.BroadcastsOnly && !r.GroupsOnly {
		var n int
		if mine, n, err = a.msgs.SearchGlobal(ctx, me.ID, pq); err != nil {
			return nil, err
		}
		total += n
	}
	if !r.UsersOnly {
		var n int
		if chans, n, err = a.chans.SearchGlobal(ctx, me.ID, cq); err != nil {
			return nil, err
		}
		total += n
	}
	// merge the two newest-first lists
	out := make([]tg.MessageClass, 0, limit)
	var users []int64
	var chatIDs []int64
	lastDate := 0
	for len(out) < limit && (len(mine) > 0 || len(chans) > 0) {
		if len(chans) == 0 || len(mine) > 0 && mine[0].Date >= chans[0].Date {
			m := mine[0]
			mine = mine[1:]
			out = append(out, m.TL())
			users = append(users, messages.Users([]messages.Msg{m})...)
			if m.Fwd.Chan != 0 {
				chatIDs = append(chatIDs, m.Fwd.Chan)
			}
			lastDate = m.Date
			continue
		}
		m := chans[0]
		chans = chans[1:]
		tl := channels.MarkOut(m.TL(), me.ID)
		u, c := mentioned(tl)
		users, chatIDs = append(users, u...), append(chatIDs, c...)
		out = append(out, tl)
		lastDate = m.Date
	}
	us, err := a.usersFor(ctx, me.ID, users)
	if err != nil {
		return nil, err
	}
	cs, err := a.chatsFor(ctx, me.ID, chatIDs)
	if err != nil {
		return nil, err
	}
	res := &tg.MessagesMessagesSlice{Count: total, Messages: out, Chats: cs, Users: us}
	if len(out) == limit {
		res.SetNextRate(lastDate)
	}
	return res, nil
}

// Only shown if a client ignores query_is_free.
const searchPostsDaily = 1000

// searchPosts is the Posts tab and "public posts" for a hashtag: every public
// channel, newest first, paged like searchGlobal.
func (a *API) searchPosts(ctx context.Context, r *tg.ChannelsSearchPostsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	text := r.Query
	if r.Hashtag != "" {
		text = "#" + r.Hashtag
	}
	if text == "" {
		return &tg.MessagesMessages{Messages: []tg.MessageClass{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}, nil
	}
	limit := min(max(r.Limit, 1), 100)
	q := channels.GlobalQuery{Text: text, BeforeDate: r.OffsetRate, AfterID: r.OffsetID, Limit: limit, Public: true}
	if r.OffsetPeer != nil {
		if to, err := a.peer(ctx, me, r.OffsetPeer); err == nil {
			q.AfterChan = to.channel
		}
	}
	list, total, err := a.chans.SearchGlobal(ctx, me.ID, q)
	if err != nil {
		return nil, err
	}
	out := make([]tg.MessageClass, 0, len(list))
	var users, chatIDs []int64
	for _, m := range list {
		tl := channels.MarkOut(m.TL(), me.ID)
		u, c := mentioned(tl)
		users, chatIDs = append(users, u...), append(chatIDs, c...)
		out = append(out, tl)
	}
	us, err := a.usersFor(ctx, me.ID, users)
	if err != nil {
		return nil, err
	}
	cs, err := a.chatsFor(ctx, me.ID, chatIDs)
	if err != nil {
		return nil, err
	}
	res := &tg.MessagesMessagesSlice{Count: total, Messages: out, Chats: cs, Users: us}
	if len(list) == limit {
		res.SetNextRate(list[len(list)-1].Date)
	}
	return res, nil
}

func (a *API) commonChats(ctx context.Context, r *tg.MessagesGetCommonChatsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := a.inputUser(ctx, me, r.UserID)
	if err != nil {
		return nil, err
	}
	list, total, err := a.chans.CommonChats(ctx, me.ID, id, r.MaxID, r.Limit)
	if err != nil {
		return nil, err
	}
	chats, err := a.chatsFor(ctx, me.ID, list)
	if err != nil {
		return nil, err
	}
	if total > len(list) {
		return &tg.MessagesChatsSlice{Count: total, Chats: chats}, nil
	}
	return &tg.MessagesChats{Chats: chats}, nil
}

func topCategory(c tg.TopPeerCategoryClass) string {
	switch c.(type) {
	case *tg.TopPeerCategoryCorrespondents:
		return messages.TopCorrespondents
	case *tg.TopPeerCategoryBotsPM:
		return messages.TopBotsPM
	case *tg.TopPeerCategoryGroups:
		return messages.TopGroups
	}
	return ""
}

func (a *API) topPeers(ctx context.Context, r *tg.ContactsGetTopPeersRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if off, err := a.accounts.TopPeersDisabled(ctx, me.ID); err != nil {
		return nil, err
	} else if off {
		return &tg.ContactsTopPeersDisabled{}, nil
	}
	out := &tg.ContactsTopPeers{Categories: []tg.TopPeerCategoryPeers{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
	var users, chans []int64
	add := func(want bool, cat tg.TopPeerCategoryClass) error {
		if !want {
			return nil
		}
		list, err := a.msgs.TopPeers(ctx, me.ID, topCategory(cat), r.Offset, r.Limit)
		if err != nil {
			return err
		}
		cp := tg.TopPeerCategoryPeers{Category: cat, Count: len(list), Peers: []tg.TopPeer{}}
		for _, p := range list {
			if p.Peer < 0 {
				chans = append(chans, -p.Peer)
				cp.Peers = append(cp.Peers, tg.TopPeer{Peer: &tg.PeerChannel{ChannelID: -p.Peer}, Rating: p.Rating})
			} else {
				users = append(users, p.Peer)
				cp.Peers = append(cp.Peers, tg.TopPeer{Peer: &tg.PeerUser{UserID: clientID(p.Peer)}, Rating: p.Rating})
			}
		}
		out.Categories = append(out.Categories, cp)
		return nil
	}
	for _, c := range []struct {
		want bool
		cat  tg.TopPeerCategoryClass
	}{
		{r.Correspondents, &tg.TopPeerCategoryCorrespondents{}},
		{r.BotsPm, &tg.TopPeerCategoryBotsPM{}},
		{r.Groups, &tg.TopPeerCategoryGroups{}},
	} {
		if err := add(c.want, c.cat); err != nil {
			return nil, err
		}
	}
	if out.Users, err = a.usersFor(ctx, me.ID, users); err != nil {
		return nil, err
	}
	out.Chats, err = a.chatsFor(ctx, me.ID, chans)
	return out, err
}

// birthdays is contacts.getBirthdays: contacts whose birthday is within a
// day of today and who let the caller see it.
func (a *API) birthdays(ctx context.Context, _ *tg.ContactsGetBirthdaysRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	out := &tg.ContactsContactBirthdays{Contacts: []tg.ContactBirthday{}, Users: []tg.UserClass{}}
	book, err := a.msgs.Contacts(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	found, err := a.accounts.BirthdaysAround(ctx, contactIDs(book), time.Now())
	if err != nil || len(found) == 0 {
		return out, err
	}
	var ids []int64
	for id := range found {
		ids = append(ids, id)
	}
	au, err := a.audienceFor(ctx, me.ID, ids, "birthday")
	if err != nil {
		return nil, err
	}
	var who []int64
	for _, id := range ids {
		if !au.can(id, "birthday") {
			continue
		}
		who = append(who, id)
		out.Contacts = append(out.Contacts, tg.ContactBirthday{ContactID: clientID(id), Birthday: birthdayTL(found[id])})
	}
	out.Users, err = a.usersFor(ctx, me.ID, who)
	return out, err
}

func birthdayTL(b account.Birthday) tg.Birthday {
	out := tg.Birthday{Day: b.Day, Month: b.Month}
	if b.Year != 0 {
		out.SetYear(b.Year)
	}
	return out
}
