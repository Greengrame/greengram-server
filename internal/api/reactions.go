package api

import (
	"context"
	"slices"
	"strconv"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/messages"
)

// Reactions aren't journaled: updateMessageReactions has no pts, and an
// app that missed one asks getMessagesReactions for what it shows.
func (a *API) registerReactions() {
	handle(a, tlprofile.SemanticMethodMessagesSendReaction, a.sendReaction)
	handle(a, tlprofile.SemanticMethodMessagesSetDefaultReaction, a.setDefaultReaction)
	handle(a, tlprofile.SemanticMethodMessagesGetMessagesReactions, a.getMessagesReactions)
	handle(a, tlprofile.SemanticMethodMessagesGetMessageReactionsList, a.reactionsList)
	handle(a, tlprofile.SemanticMethodMessagesReadReactions, a.readReactions)
	handle(a, tlprofile.SemanticMethodMessagesGetUnreadReactions, a.unreadReactions)
	handle(a, tlprofile.SemanticMethodMessagesGetAvailableReactions, a.availableReactions)
	handle(a, tlprofile.SemanticMethodMessagesGetMessageReadParticipants, a.readParticipants)
	handle(a, tlprofile.SemanticMethodMessagesGetMessagesViews, a.messagesViews)
}

func (a *API) availableReactions(ctx context.Context, r *tg.MessagesGetAvailableReactionsRequest) (any, error) {
	if a.media == nil {
		return &tg.MessagesAvailableReactions{Reactions: []tg.AvailableReaction{}}, nil
	}
	list, hash, err := a.media.AvailableReactions(ctx)
	if err != nil {
		return nil, err
	}
	if r.Hash != 0 && r.Hash == hash {
		return &tg.MessagesAvailableReactionsNotModified{}, nil
	}
	return &tg.MessagesAvailableReactions{Hash: hash, Reactions: list}, nil
}

// maxPaidReaction is the most Stars one send of a star reaction can carry
// (Telegram's stars_paid_reaction_amount_max).
const maxPaidReaction = 10000

// Telegram's reactions_user_max_default and _premium.
func reactionsMax(u account.User) int {
	if u.Premium(nowTime()) {
		return 3
	}
	return 1
}

func (a *API) setDefaultReaction(ctx context.Context, r *tg.MessagesSetDefaultReactionRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	keys, err := messages.ReactionKeys([]tg.ReactionClass{r.Reaction}, 1)
	if err != nil || len(keys) != 1 {
		return nil, messages.ErrReactionInvalid
	}
	if err := a.accounts.SetDefaultReaction(ctx, me.ID, keys[0]); err != nil {
		return nil, err
	}
	// the user's other apps read it from the config
	a.deliver([]messages.Event{{UserID: me.ID, Update: &tg.UpdateConfig{}}}, except(ctx))
	return true, nil
}

// quickReaction is help.getConfig's reactions_default. That call comes before
// and after sign-in; a failure only means the default shows.
func (a *API) quickReaction(ctx context.Context) tg.ReactionClass {
	c := edge.CallFrom(ctx)
	if c == nil {
		return nil
	}
	u, _, ok, err := a.accounts.KeyUser(ctx, c.PermKeyID)
	if err != nil || !ok {
		return nil
	}
	key, err := a.accounts.DefaultReaction(ctx, u.ID)
	if err != nil || key == "" {
		return nil
	}
	return messages.ReactionTL(key)
}

func (a *API) sendReaction(ctx context.Context, r *tg.MessagesSendReactionRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	keys, err := messages.ReactionKeys(r.Reaction, reactionsMax(me))
	if err != nil {
		return nil, err
	}
	// custom emoji reactions are a Premium thing
	if !me.Premium(nowTime()) && slices.ContainsFunc(keys, messages.Custom) {
		return nil, messages.ErrReactionInvalid
	}
	if to.channel != 0 {
		return a.channelReact(ctx, me, to.channel, r.MsgID, keys, r.Big)
	}
	if err := a.checkBlocked(ctx, me.ID, to.user); err != nil {
		return nil, err
	}
	res, err := a.msgs.React(ctx, me.ID, to.user, r.MsgID, keys, r.Big)
	if err != nil {
		return nil, err
	}
	return a.privateReacted(ctx, me, to.user, r.MsgID, res)
}

// privateReacted tells both sides of a private chat about a changed set.
func (a *API) privateReacted(ctx context.Context, me account.User, peer int64, msgID int, res messages.Reacted) (any, error) {
	// td writes flags while encoding, so the reply and every push job
	// get objects of their own
	view := func(viewer, peer int64, id int) tg.UpdateClass {
		return &tg.UpdateMessageReactions{Peer: &tg.PeerUser{UserID: clientID(peer)}, MsgID: id,
			Reactions: res.MsgReactions.TL(viewer, true)}
	}
	users, err := a.usersFor(ctx, me.ID, append(res.Users(), me.ID, peer))
	if err != nil {
		return nil, err
	}
	events := []messages.Event{{UserID: me.ID, Update: view(me.ID, peer, msgID)}}
	if id, ok := res.IDs[peer]; ok && peer != me.ID {
		events = append(events, messages.Event{UserID: peer, Update: view(peer, me.ID, id)})
	}
	a.deliver(events, except(ctx))
	return &tg.Updates{Updates: []tg.UpdateClass{view(me.ID, peer, msgID)}, Users: users, Chats: []tg.ChatClass{}, Date: now()}, nil
}

// channelReact tells the reactor and the author their own view; everyone
// else gets the min one, a full view per member would cost a query each.
func (a *API) channelReact(ctx context.Context, me account.User, channel int64, id int, keys []string, big bool) (any, error) {
	res, err := a.chans.React(ctx, me.ID, channel, id, keys, big)
	if err != nil {
		return nil, err
	}
	return a.channelReacted(ctx, me, channel, id, res)
}

func (a *API) channelReacted(ctx context.Context, me account.User, channel int64, id int, res channels.Reacted) (any, error) {
	view := func(viewer int64) tg.UpdateClass {
		return &tg.UpdateMessageReactions{Peer: &tg.PeerChannel{ChannelID: channel}, MsgID: id,
			Reactions: res.MsgReactions.TL(viewer, res.Channel.Megagroup)}
	}
	members, err := a.chans.MemberIDs(ctx, channel)
	if err != nil {
		return nil, err
	}
	// one event per member: deliverChannel hands an event's update to
	// every recipient's job, and td writes flags while encoding
	events := []channels.Event{{Users: []int64{me.ID}, Update: view(me.ID)}}
	for _, u := range members {
		switch {
		case u == me.ID || !a.hub.online(u):
		case u == res.Author:
			events = append(events, channels.Event{Users: []int64{u}, Update: view(u)})
		default:
			events = append(events, channels.Event{Users: []int64{u}, Update: view(0)})
		}
	}
	a.deliverChannel(events, except(ctx))
	users, err := a.usersFor(ctx, me.ID, append(res.Users(), me.ID))
	if err != nil {
		return nil, err
	}
	chats, err := a.chatsFor(ctx, me.ID, []int64{channel})
	if err != nil {
		return nil, err
	}
	return &tg.Updates{Updates: []tg.UpdateClass{view(me.ID)}, Users: users, Chats: chats, Date: now()}, nil
}

// reactionsUpdate is one message's reactions for viewer, empty when there
// are none so the app drops what it had.
func reactionsUpdate(peer tg.PeerClass, id int, set messages.MsgReactions, viewer int64, canSee bool) *tg.UpdateMessageReactions {
	return &tg.UpdateMessageReactions{Peer: peer, MsgID: id, Reactions: set.TL(viewer, canSee)}
}

func (a *API) getMessagesReactions(ctx context.Context, r *tg.MessagesGetMessagesReactionsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	out := &tg.Updates{Updates: []tg.UpdateClass{}, Chats: []tg.ChatClass{}, Date: now()}
	var users []int64
	if to.channel != 0 {
		c, _, err := a.chans.Access(ctx, to.channel, me.ID)
		if err != nil {
			return nil, err
		}
		sets, err := a.chans.Reactions(ctx, to.channel, r.ID)
		if err != nil {
			return nil, err
		}
		for _, id := range r.ID {
			out.Updates = append(out.Updates, reactionsUpdate(&tg.PeerChannel{ChannelID: c.ID}, id, sets[id], me.ID, c.Megagroup))
			users = append(users, sets[id].Users()...)
		}
		if out.Chats, err = a.chatsFor(ctx, me.ID, []int64{c.ID}); err != nil {
			return nil, err
		}
	} else {
		sets, err := a.msgs.Reactions(ctx, me.ID, r.ID)
		if err != nil {
			return nil, err
		}
		for _, id := range r.ID {
			out.Updates = append(out.Updates, reactionsUpdate(&tg.PeerUser{UserID: clientID(to.user)}, id, sets[id], me.ID, true))
			users = append(users, sets[id].Users()...)
		}
	}
	out.Users, err = a.usersFor(ctx, me.ID, users)
	return out, err
}

// reactionsList is who reacted. Broadcast channels keep that to
// themselves, like Telegram.
func (a *API) reactionsList(ctx context.Context, r *tg.MessagesGetMessageReactionsListRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	key := ""
	if r.Reaction != nil {
		keys, err := messages.ReactionKeys([]tg.ReactionClass{r.Reaction}, 1)
		if err != nil {
			return nil, err
		}
		if len(keys) > 0 {
			key = keys[0]
		}
	}
	offset, _ := strconv.Atoi(r.Offset)
	offset = max(offset, 0)
	out := &tg.MessagesMessageReactionsList{Reactions: []tg.MessagePeerReaction{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
	var list []messages.Reaction
	if to.channel != 0 {
		c, _, err := a.chans.Access(ctx, to.channel, me.ID)
		if err != nil {
			return nil, err
		}
		if !c.Megagroup {
			return out, nil
		}
		list, out.Count, err = a.chans.ReactionList(ctx, c.ID, r.ID, key, offset, r.Limit)
		if err != nil {
			return nil, err
		}
	} else {
		list, out.Count, err = a.msgs.ReactionList(ctx, me.ID, r.ID, key, offset, r.Limit)
		if err != nil {
			return nil, err
		}
	}
	for _, x := range list {
		pr := tg.MessagePeerReaction{PeerID: &tg.PeerUser{UserID: clientID(x.User)}, Date: x.Date, Reaction: messages.ReactionTL(x.Key), Big: x.Big}
		pr.My = x.User == me.ID
		out.Reactions = append(out.Reactions, pr)
	}
	if next := offset + len(list); len(list) > 0 && next < out.Count {
		out.SetNextOffset(strconv.Itoa(next))
	}
	out.Users, err = a.usersFor(ctx, me.ID, messages.Reactors(list))
	return out, err
}

// readReactions moves no pts; the app clears its badge itself.
func (a *API) readReactions(ctx context.Context, r *tg.MessagesReadReactionsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if to.channel != 0 {
		c, _, err := a.chans.Access(ctx, to.channel, me.ID)
		if err != nil {
			return nil, err
		}
		if err := a.chans.ReadReactions(ctx, me.ID, c.ID); err != nil {
			return nil, err
		}
		return &tg.MessagesAffectedHistory{Pts: c.Pts}, nil
	}
	if err := a.msgs.ReadReactions(ctx, me.ID, to.user); err != nil {
		return nil, err
	}
	st, err := a.msgs.State(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesAffectedHistory{Pts: st.Pts}, nil
}

func (a *API) unreadReactions(ctx context.Context, r *tg.MessagesGetUnreadReactionsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	return a.filtered(ctx, me, to, messages.UnreadReactions, r.OffsetID, r.AddOffset, r.Limit, r.MaxID, r.MinID)
}

// filtered is a history page of only some messages (unread reactions,
// unread mentions), in either kind of chat.
func (a *API) filtered(ctx context.Context, me account.User, to peer, f messages.Filter, offsetID, addOffset, limit, maxID, minID int) (any, error) {
	if to.channel != 0 {
		c, _, err := a.chans.Access(ctx, to.channel, me.ID)
		if err != nil {
			return nil, err
		}
		list, total, err := a.chans.History(ctx, c.ID, channels.HistoryQuery{OffsetID: offsetID, AddOffset: addOffset, Limit: limit, MaxID: maxID, MinID: minID,
			Filter: f, Viewer: me.ID})
		if err != nil {
			return nil, err
		}
		msgs, users, chats, err := a.channelBody(ctx, me, c, list)
		if err != nil {
			return nil, err
		}
		return &tg.MessagesChannelMessages{Pts: c.Pts, Count: total, Messages: msgs, Topics: []tg.ForumTopicClass{}, Chats: chats, Users: users}, nil
	}
	list, total, err := a.msgs.History(ctx, me.ID, to.user, messages.HistoryQuery{OffsetID: offsetID, AddOffset: addOffset, Limit: limit, MaxID: maxID, MinID: minID, Filter: f})
	if err != nil {
		return nil, err
	}
	msgs, users, chats, err := a.messagesBody(ctx, me, list)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesMessagesSlice{Count: total, Messages: msgs, Chats: chats, Users: users}, nil
}

// withReactions puts each message's reactions on it, as viewer sees them.
func withReactions(list []tg.MessageClass, sets map[int]messages.MsgReactions, viewer int64, canSee bool) []int64 {
	var users []int64
	for _, m := range list {
		msg, ok := m.(*tg.Message)
		if !ok {
			continue
		}
		if set, ok := sets[msg.ID]; ok {
			msg.SetReactions(set.TL(viewer, canSee))
			users = append(users, set.Users()...)
		}
	}
	return users
}

// reactionUsers are the reactors an updateMessageReactions shows.
func reactionUsers(u tg.UpdateClass) []int64 {
	v, ok := u.(*tg.UpdateMessageReactions)
	if !ok {
		return nil
	}
	var out []int64
	if p, ok := v.Peer.(*tg.PeerUser); ok {
		out = append(out, serverID(p.UserID))
	}
	for _, r := range v.Reactions.RecentReactions {
		if p, ok := r.PeerID.(*tg.PeerUser); ok {
			out = append(out, serverID(p.UserID))
		}
	}
	for _, r := range v.Reactions.TopReactors {
		if p, ok := r.PeerID.(*tg.PeerUser); ok {
			out = append(out, serverID(p.UserID))
		}
	}
	return out
}

// readParticipants is "seen by": a group's author asks who read their message.
func (a *API) readParticipants(ctx context.Context, r *tg.MessagesGetMessageReadParticipantsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if to.channel == 0 {
		return nil, errPeerInvalid
	}
	if _, _, err := a.chans.Access(ctx, to.channel, me.ID); err != nil {
		return nil, err
	}
	list, err := a.chans.ReadParticipants(ctx, me.ID, to.channel, r.MsgID)
	if err != nil {
		return nil, err
	}
	out := make([]tg.ReadParticipantDate, 0, len(list))
	for _, d := range list {
		out = append(out, tg.ReadParticipantDate{UserID: clientID(d.User), Date: d.Date})
	}
	return out, nil
}

// messagesViews is the eye counter on channel posts; other chats have none.
func (a *API) messagesViews(ctx context.Context, r *tg.MessagesGetMessagesViewsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	out := &tg.MessagesMessageViews{Views: make([]tg.MessageViews, len(r.ID)), Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
	if to.channel == 0 {
		return out, nil
	}
	if _, _, err := a.chans.Access(ctx, to.channel, me.ID); err != nil {
		return nil, err
	}
	views, err := a.chans.Views(ctx, to.channel, r.ID)
	if err != nil {
		return nil, err
	}
	for i, id := range r.ID {
		out.Views[i].SetViews(views[id])
	}
	return out, nil
}
