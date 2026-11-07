package api

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/messages"
)

func (a *API) registerPins() {
	handle(a, tlprofile.SemanticMethodMessagesUpdatePinnedMessage, a.updatePinned)
	handle(a, tlprofile.SemanticMethodMessagesUnpinAllMessages, a.unpinAll)
}

// updatePinned pins like Telegram: in a private chat for both sides with
// a service message, or only for yourself (pm_oneside, Saved Messages, or
// when the other side blocked you, so they hear nothing of it).
func (a *API) updatePinned(ctx context.Context, r *tg.MessagesUpdatePinnedMessageRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if to.channel != 0 {
		events, err := a.chans.PinMessage(ctx, me.ID, to.channel, r.ID, !r.Unpin, r.Silent)
		if err != nil {
			return nil, err
		}
		return a.channelOp(ctx, me, to.channel, events)
	}
	oneSide := r.PmOneside || to.user == me.ID
	if !oneSide && !r.Unpin {
		if _, theyBlocked, err := a.msgs.Blocks(ctx, me.ID, to.user); err != nil {
			return nil, err
		} else if theyBlocked {
			oneSide = true
		}
	}
	u, events, err := a.msgs.PinMessage(ctx, me.ID, to.user, r.ID, !r.Unpin, oneSide)
	if err != nil {
		return nil, err
	}
	list := []tg.UpdateClass{u}
	if !r.Unpin && !oneSide {
		s, err := a.msgs.Send(ctx, me.ID, to.user, messages.Draft{Action: &tg.MessageActionPinMessage{}, ReplyTo: r.ID, Silent: r.Silent})
		if err != nil {
			return nil, err
		}
		list = append(list, s.Update)
		events = append(events, messages.Event{UserID: me.ID, Update: s.Update})
		events = append(events, s.Events...)
	}
	return a.privateOp(ctx, me, to.user, list, events)
}

func (a *API) unpinAll(ctx context.Context, r *tg.MessagesUnpinAllMessagesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if to.channel != 0 {
		res, events, err := a.chans.UnpinAll(ctx, me.ID, to.channel)
		if err != nil {
			return nil, err
		}
		a.deliverChannel(a.oneEach(events), except(ctx))
		return &res, nil
	}
	res, events, err := a.msgs.UnpinAll(ctx, me.ID, to.user)
	if err != nil {
		return nil, err
	}
	a.deliver(cloneEvents(events), except(ctx))
	return &res, nil
}

// privateOp answers with the caller's journal entries and pushes events,
// the caller's other sessions included, each push with its own copy.
func (a *API) privateOp(ctx context.Context, me account.User, peer int64, list []tg.UpdateClass, events []messages.Event) (*tg.Updates, error) {
	a.deliver(cloneEvents(events), except(ctx))
	var who []int64
	for _, u := range list {
		who = append(who, updateUsers(u)...)
	}
	users, err := a.usersFor(ctx, me.ID, append(who, me.ID, peer))
	if err != nil {
		return nil, err
	}
	return &tg.Updates{Updates: list, Users: users, Chats: []tg.ChatClass{}, Date: now()}, nil
}

// channelOp is opUpdates without sharing objects between the answer and
// the pushes.
func (a *API) channelOp(ctx context.Context, me account.User, channel int64, events []channels.Event) (*tg.Updates, error) {
	mine := make([]channels.Event, 0, len(events))
	for _, e := range events {
		for _, u := range e.Users {
			if u == me.ID {
				mine = append(mine, channels.Event{Users: []int64{me.ID}, Update: cloneUpdate(e.Update)})
				break
			}
		}
	}
	a.deliverChannel(a.oneEach(events), except(ctx))
	out := &tg.Updates{Updates: []tg.UpdateClass{}, Date: now()}
	var users []int64
	for _, e := range mine {
		out.Updates = append(out.Updates, channels.SetOut(e.Update, me.ID))
		if m := updateMessage(e.Update); m != nil {
			us, _ := mentioned(m)
			users = append(users, us...)
		}
	}
	var err error
	if out.Users, err = a.usersFor(ctx, me.ID, append(users, me.ID)); err != nil {
		return nil, err
	}
	if out.Chats, err = a.chatsFor(ctx, me.ID, []int64{channel}); err != nil {
		return nil, err
	}
	return out, nil
}

func cloneEvents(list []messages.Event) []messages.Event {
	out := make([]messages.Event, len(list))
	for i, e := range list {
		out[i] = messages.Event{UserID: e.UserID, Update: cloneUpdate(e.Update)}
	}
	return out
}
