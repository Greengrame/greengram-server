package api

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/messages"
)

func init() {
	frozenOK[tlprofile.SemanticMethodMessagesGetPinnedSavedDialogs] = true
}

// Saved Messages by source: what Telegram calls saved dialogs. The
// messages stay in the Saved Messages chat; these group them.
func (a *API) registerSaved() {
	handle(a, tlprofile.SemanticMethodMessagesGetSavedDialogs, func(ctx context.Context, r *tg.MessagesGetSavedDialogsRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		if r.ParentPeer != nil {
			// channel direct messages (monoforums); there are none here
			return emptySaved(), nil
		}
		list, tops, total, err := a.msgs.SavedDialogs(ctx, me.ID, r.OffsetID, r.Limit, r.ExcludePinned)
		if err != nil {
			return nil, err
		}
		out, err := a.savedBody(ctx, me, list, tops)
		if err != nil || total <= len(list) {
			return out, err
		}
		return &tg.MessagesSavedDialogsSlice{Count: total, Dialogs: out.Dialogs, Messages: out.Messages, Chats: out.Chats, Users: out.Users}, nil
	})
	handle(a, tlprofile.SemanticMethodMessagesGetPinnedSavedDialogs, func(ctx context.Context, _ *tg.MessagesGetPinnedSavedDialogsRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		list, tops, err := a.msgs.PinnedSavedDialogs(ctx, me.ID)
		if err != nil {
			return nil, err
		}
		return a.savedBody(ctx, me, list, tops)
	})
	handle(a, tlprofile.SemanticMethodMessagesGetSavedHistory, func(ctx context.Context, r *tg.MessagesGetSavedHistoryRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		empty := &tg.MessagesMessages{Messages: []tg.MessageClass{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
		if r.ParentPeer != nil {
			return empty, nil
		}
		peer, err := a.savedPeer(ctx, me, r.Peer)
		if err != nil {
			return nil, err
		}
		list, total, err := a.msgs.SavedHistory(ctx, me.ID, peer, messages.HistoryQuery{
			OffsetID: r.OffsetID, OffsetDate: r.OffsetDate, AddOffset: r.AddOffset, Limit: r.Limit, MaxID: r.MaxID, MinID: r.MinID,
		})
		if err != nil {
			return nil, err
		}
		msgs, users, chats, err := a.messagesBody(ctx, me, list)
		if err != nil {
			return nil, err
		}
		return &tg.MessagesMessagesSlice{Count: total, Messages: msgs, Chats: chats, Users: users}, nil
	})
	handle(a, tlprofile.SemanticMethodMessagesToggleSavedDialogPin, func(ctx context.Context, r *tg.MessagesToggleSavedDialogPinRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		peer, err := a.savedDialogPeer(ctx, me, r.Peer)
		if err != nil {
			return nil, err
		}
		if ok, err := a.msgs.HasSaved(ctx, me.ID, peer); err != nil || !ok {
			if err == nil {
				err = errPeerInvalid
			}
			return nil, err
		}
		if err := a.msgs.PinSaved(ctx, me.ID, peer, r.Pinned); err != nil {
			return nil, err
		}
		up := &tg.UpdateSavedDialogPinned{Pinned: r.Pinned, Peer: &tg.DialogPeer{Peer: messages.SavedPeerTL(peer, me.ID)}}
		a.echo(ctx, me, []tg.UpdateClass{up}, []tg.UserClass{})
		return true, nil
	})
	handle(a, tlprofile.SemanticMethodMessagesReorderPinnedSavedDialogs, func(ctx context.Context, r *tg.MessagesReorderPinnedSavedDialogsRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		var order []int64
		for _, in := range r.Order {
			peer, err := a.savedDialogPeer(ctx, me, in)
			if err != nil {
				return nil, err
			}
			order = append(order, peer)
		}
		if err := a.msgs.ReorderSavedPins(ctx, me.ID, order, r.Force); err != nil {
			return nil, err
		}
		up := &tg.UpdatePinnedSavedDialogs{}
		peers := make([]tg.DialogPeerClass, 0, len(order))
		for _, p := range order {
			peers = append(peers, &tg.DialogPeer{Peer: messages.SavedPeerTL(p, me.ID)})
		}
		up.SetOrder(peers)
		a.echo(ctx, me, []tg.UpdateClass{up}, []tg.UserClass{})
		return true, nil
	})
	handle(a, tlprofile.SemanticMethodMessagesDeleteSavedHistory, func(ctx context.Context, r *tg.MessagesDeleteSavedHistoryRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		if r.ParentPeer != nil {
			return nil, errPeerInvalid
		}
		peer, err := a.savedPeer(ctx, me, r.Peer)
		if err != nil {
			return nil, err
		}
		ids, err := a.msgs.SavedIDs(ctx, me.ID, peer, r.MaxID, r.MinDate, r.MaxDate)
		if err != nil {
			return nil, err
		}
		// Telegram deletes in steps and the client repeats while offset > 0
		const step = 100
		more := len(ids) > step
		if more {
			ids = ids[len(ids)-step:]
		}
		res, events, err := a.msgs.Delete(ctx, me.ID, ids, false)
		if err != nil {
			return nil, err
		}
		a.deliver(events, except(ctx))
		out := &tg.MessagesAffectedHistory{Pts: res.Pts, PtsCount: res.PtsCount}
		if more {
			out.Offset = 1
		}
		return out, nil
	})
}

func emptySaved() *tg.MessagesSavedDialogs {
	return &tg.MessagesSavedDialogs{Dialogs: []tg.SavedDialogClass{}, Messages: []tg.MessageClass{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
}

// savedPeer maps a client peer to a saved_peer value (see Msg.SavedPeer).
func (a *API) savedPeer(ctx context.Context, me account.User, in tg.InputPeerClass) (int64, error) {
	if u, ok := in.(*tg.InputPeerUser); ok && u.UserID == messages.HiddenAuthor {
		return 0, nil
	}
	switch v := in.(type) {
	case *tg.InputPeerChannel:
		return -v.ChannelID, nil
	case *tg.InputPeerChannelFromMessage:
		return -v.ChannelID, nil
	case *tg.InputPeerUser, *tg.InputPeerUserFromMessage:
		// the author of a forward may since have gone; their id stays valid here
		u := in.(interface{ GetUserID() int64 })
		return serverID(u.GetUserID()), nil
	case *tg.InputPeerSelf:
		return me.ID, nil
	}
	return 0, errPeerInvalid
}

func (a *API) savedDialogPeer(ctx context.Context, me account.User, in tg.InputDialogPeerClass) (int64, error) {
	d, ok := in.(*tg.InputDialogPeer)
	if !ok {
		return 0, tgerr.New(400, "PEER_ID_INVALID")
	}
	return a.savedPeer(ctx, me, d.Peer)
}

// savedBody renders saved dialogs with their top messages and the users
// and chats they name, the hidden author included.
func (a *API) savedBody(ctx context.Context, me account.User, list []messages.SavedDialog, tops []messages.Msg) (*tg.MessagesSavedDialogs, error) {
	out := emptySaved()
	var users, chans []int64
	hidden := false
	for _, d := range list {
		sd := &tg.SavedDialog{Pinned: d.Pinned, Peer: messages.SavedPeerTL(d.Peer, me.ID), TopMessage: d.TopID}
		out.Dialogs = append(out.Dialogs, sd)
		switch {
		case d.Peer < 0:
			chans = append(chans, -d.Peer)
		case d.Peer == 0:
			hidden = true
		default:
			users = append(users, d.Peer)
		}
	}
	msgs, us, cs, err := a.messagesBody(ctx, me, tops)
	if err != nil {
		return nil, err
	}
	out.Messages = msgs
	more, err := a.usersFor(ctx, me.ID, users)
	if err != nil {
		return nil, err
	}
	out.Users = append(us, more...)
	if hidden {
		out.Users = append(out.Users, &tg.User{ID: messages.HiddenAuthor, AccessHash: accessHash(messages.HiddenAuthor), FirstName: "Author Hidden"})
	}
	ch, err := a.chatsFor(ctx, me.ID, chans)
	if err != nil {
		return nil, err
	}
	out.Chats = append(cs, ch...)
	return out, nil
}
