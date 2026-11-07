package api

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/messages"
)

// Notification settings live here so every app of a user mutes the same
// chats; the apps decide themselves whether to make a sound.
func (a *API) registerNotify() {
	handle(a, tlprofile.SemanticMethodAccountUpdateNotifySettings, a.updateNotify)
	handle(a, tlprofile.SemanticMethodAccountGetNotifySettings, a.getNotify)
	handle(a, tlprofile.SemanticMethodAccountGetNotifyExceptions, a.notifyExceptions)
	handle(a, tlprofile.SemanticMethodAccountResetNotifySettings, a.resetNotify)
}

func (a *API) notifyKey(ctx context.Context, me account.User, in tg.InputNotifyPeerClass) (messages.NotifyKey, tg.NotifyPeerClass, error) {
	switch v := in.(type) {
	case *tg.InputNotifyUsers:
		return messages.NotifyKey{Scope: "users"}, &tg.NotifyUsers{}, nil
	case *tg.InputNotifyChats:
		return messages.NotifyKey{Scope: "chats"}, &tg.NotifyChats{}, nil
	case *tg.InputNotifyBroadcasts:
		return messages.NotifyKey{Scope: "broadcasts"}, &tg.NotifyBroadcasts{}, nil
	case *tg.InputNotifyPeer:
		to, err := a.peer(ctx, me, v.Peer)
		if err != nil {
			return messages.NotifyKey{}, nil, err
		}
		if to.channel != 0 {
			if _, _, err := a.chans.Access(ctx, to.channel, me.ID); err != nil {
				return messages.NotifyKey{}, nil, err
			}
			return messages.NotifyKey{Scope: "peer", Channel: to.channel}, &tg.NotifyPeer{Peer: to.tl()}, nil
		}
		return messages.NotifyKey{Scope: "peer", User: to.user}, &tg.NotifyPeer{Peer: to.tl()}, nil
	}
	// no forum topics here
	return messages.NotifyKey{}, nil, errPeerInvalid
}

func notifyPeerTL(k messages.NotifyKey) tg.NotifyPeerClass {
	switch k.Scope {
	case "users":
		return &tg.NotifyUsers{}
	case "chats":
		return &tg.NotifyChats{}
	case "broadcasts":
		return &tg.NotifyBroadcasts{}
	}
	return &tg.NotifyPeer{Peer: peer{user: k.User, channel: k.Channel}.tl()}
}

// peerNotify is what the apps get for what they sent: one sound for all
// platforms, and only the fields they set.
func peerNotify(in tg.InputPeerNotifySettings) tg.PeerNotifySettings {
	var out tg.PeerNotifySettings
	if v, ok := in.GetShowPreviews(); ok {
		out.SetShowPreviews(v)
	}
	if v, ok := in.GetSilent(); ok {
		out.SetSilent(v)
	}
	if v, ok := in.GetMuteUntil(); ok {
		out.SetMuteUntil(v)
	}
	if v, ok := in.GetSound(); ok {
		out.SetIosSound(v)
		out.SetAndroidSound(v)
		out.SetOtherSound(v)
	}
	if v, ok := in.GetStoriesMuted(); ok {
		out.SetStoriesMuted(v)
	}
	if v, ok := in.GetStoriesHideSender(); ok {
		out.SetStoriesHideSender(v)
	}
	if v, ok := in.GetStoriesSound(); ok {
		out.SetStoriesIosSound(v)
		out.SetStoriesAndroidSound(v)
		out.SetStoriesOtherSound(v)
	}
	return out
}

// scopeDefault is a scope nobody changed: everything on, like Telegram.
func scopeDefault() tg.PeerNotifySettings {
	var out tg.PeerNotifySettings
	out.SetShowPreviews(true)
	out.SetSilent(false)
	out.SetMuteUntil(0)
	out.SetOtherSound(&tg.NotificationSoundDefault{})
	return out
}

func (a *API) updateNotify(ctx context.Context, r *tg.AccountUpdateNotifySettingsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	k, _, err := a.notifyKey(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	v := peerNotify(r.Settings)
	if err := a.msgs.SetNotify(ctx, me.ID, k, v); err != nil {
		return nil, err
	}
	// a fresh object for the push, see cloneUpdate
	a.deliver([]messages.Event{{UserID: me.ID, Update: &tg.UpdateNotifySettings{Peer: notifyPeerTL(k), NotifySettings: peerNotify(r.Settings)}}}, except(ctx))
	return true, nil
}

func (a *API) getNotify(ctx context.Context, r *tg.AccountGetNotifySettingsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	k, _, err := a.notifyKey(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	v, ok, err := a.msgs.Notify(ctx, me.ID, k)
	if err != nil {
		return nil, err
	}
	if !ok && k.Scope != "peer" {
		v = scopeDefault()
	}
	return &v, nil
}

// notifyExceptions are the chats with their own settings, as updates.
func (a *API) notifyExceptions(ctx context.Context, r *tg.AccountGetNotifyExceptionsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	list, err := a.msgs.NotifyExceptions(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	var users, chans []int64
	for _, e := range list {
		if e.Channel != 0 {
			chans = append(chans, e.Channel)
		} else {
			users = append(users, e.User)
		}
	}
	kinds, err := a.chans.GetMany(ctx, chans)
	if err != nil {
		return nil, err
	}
	only := ""
	if r.Peer != nil {
		switch v := r.Peer.(type) {
		case *tg.InputNotifyUsers:
			only = "users"
		case *tg.InputNotifyChats:
			only = "chats"
		case *tg.InputNotifyBroadcasts:
			only = "broadcasts"
		case *tg.InputNotifyPeer:
			k, _, err := a.notifyKey(ctx, me, v)
			if err != nil {
				return nil, err
			}
			only = "peer"
			kept := list[:0]
			for _, e := range list {
				if e.NotifyKey == k {
					kept = append(kept, e)
				}
			}
			list = kept
		}
	}
	out := &tg.Updates{Updates: []tg.UpdateClass{}, Date: now()}
	for _, e := range list {
		kind := "users"
		if e.Channel != 0 {
			c, ok := kinds[e.Channel]
			if !ok || c.Deleted {
				continue
			}
			kind = "broadcasts"
			if c.Megagroup {
				kind = "chats"
			}
		}
		if only != "" && only != "peer" && only != kind {
			continue
		}
		out.Updates = append(out.Updates, &tg.UpdateNotifySettings{Peer: notifyPeerTL(e.NotifyKey), NotifySettings: e.Settings})
	}
	if out.Users, err = a.usersFor(ctx, me.ID, users); err != nil {
		return nil, err
	}
	out.Chats, err = a.chatsFor(ctx, me.ID, chans)
	return out, err
}

func (a *API) resetNotify(ctx context.Context, _ *tg.AccountResetNotifySettingsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	return true, a.msgs.ResetNotify(ctx, me.ID)
}

// peerNotifyMap is the user's per-chat settings by chat, for dialogs.
func (a *API) peerNotifyMap(ctx context.Context, me int64) (map[peer]tg.PeerNotifySettings, error) {
	list, err := a.msgs.NotifyExceptions(ctx, me)
	if err != nil {
		return nil, err
	}
	out := make(map[peer]tg.PeerNotifySettings, len(list))
	for _, e := range list {
		out[peer{user: e.User, channel: e.Channel}] = e.Settings
	}
	return out, nil
}

// peerNotifyOf is one chat's settings, empty (the scope decides) if none.
func (a *API) peerNotifyOf(ctx context.Context, me int64, p peer) (tg.PeerNotifySettings, error) {
	v, _, err := a.msgs.Notify(ctx, me, messages.NotifyKey{Scope: "peer", User: p.user, Channel: p.channel})
	return v, err
}
