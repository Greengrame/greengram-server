package api

import (
	"context"
	"log/slog"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/media"
)

// peer is a resolved chat: a user (private chat) or a channel/group.
type peer struct {
	user    int64
	channel int64
}

func (a *API) peer(ctx context.Context, me account.User, p tg.InputPeerClass) (peer, error) {
	switch v := p.(type) {
	case *tg.InputPeerChannel:
		return peer{channel: v.ChannelID}, nil
	case *tg.InputPeerChannelFromMessage:
		return peer{channel: v.ChannelID}, nil
	}
	id, err := a.peerUser(ctx, me, p)
	return peer{user: id}, err
}

func inputChannel(c tg.InputChannelClass) (int64, error) {
	switch v := c.(type) {
	case *tg.InputChannel:
		return v.ChannelID, nil
	case *tg.InputChannelFromMessage:
		return v.ChannelID, nil
	}
	return 0, errChannelInvalid
}

var errChannelInvalid = channelErr("CHANNEL_INVALID")

// chatsFor renders channels as viewer sees them (left, admin rights...).
func (a *API) chatsFor(ctx context.Context, viewer int64, list []int64) ([]tg.ChatClass, error) {
	out := []tg.ChatClass{}
	if len(list) == 0 {
		return out, nil
	}
	chans, err := a.chans.GetMany(ctx, list)
	if err != nil {
		return nil, err
	}
	found := make([]int64, 0, len(chans))
	var outside []channels.Channel
	for id := range chans {
		found = append(found, id)
	}
	ms, err := a.chans.MemberOfMany(ctx, viewer, found)
	if err != nil {
		return nil, err
	}
	for id, c := range chans {
		if !channels.InChat(ms[id].Status) {
			outside = append(outside, c)
		}
	}
	open, err := a.chans.OpenMany(ctx, outside)
	if err != nil {
		return nil, err
	}
	var levels map[int64]int
	if a.stars != nil {
		if levels, err = a.stars.ChannelLevels(ctx, found); err != nil {
			return nil, err
		}
	}
	seen := map[int64]bool{}
	for _, id := range list {
		c, ok := chans[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		m := ms[id]
		// ids are guessable; a private chat's title and size are for its members
		// (former members keep the title for the leftover dialog)
		if !channels.InChat(m.Status) && !open[id] {
			f := &tg.ChannelForbidden{ID: c.ID, AccessHash: channels.AccessHash(c.ID), Megagroup: c.Megagroup, Broadcast: !c.Megagroup}
			if m.Date != 0 {
				f.Title = c.Title
			}
			out = append(out, f)
			continue
		}
		tl := c.TL(m)
		if ch, ok := tl.(*tg.Channel); ok {
			if c.PhotoID != 0 {
				ch.Photo = a.chatPhoto(c.PhotoID)
			}
			if l := levels[id]; l > 0 {
				ch.SetLevel(l)
			}
		}
		out = append(out, tl)
	}
	return out, nil
}

// mentioned is who and which chats a message or update refers to.
func mentioned(msgs ...tg.MessageClass) (users, chans []int64) {
	addPeer := func(p tg.PeerClass) {
		switch v := p.(type) {
		case *tg.PeerUser:
			users = append(users, serverID(v.UserID))
		case *tg.PeerChannel:
			chans = append(chans, v.ChannelID)
		}
	}
	for _, msg := range msgs {
		switch m := msg.(type) {
		case *tg.Message:
			addPeer(m.PeerID)
			if m.FromID != nil {
				addPeer(m.FromID)
			}
			if fwd, ok := m.GetFwdFrom(); ok && fwd.FromID != nil {
				addPeer(fwd.FromID)
			}
			if via, ok := m.GetViaBotID(); ok {
				users = append(users, serverID(via))
			}
			if rich, ok := m.GetRichMessage(); ok {
				for _, id := range media.RichMentions(rich) {
					users = append(users, serverID(id))
				}
			}
			// a giveaway names its channels, its results the winners
			switch md := m.Media.(type) {
			case *tg.MessageMediaGiveaway:
				chans = append(chans, md.Channels...)
			case *tg.MessageMediaGiveawayResults:
				chans = append(chans, md.ChannelID)
				for _, u := range md.Winners {
					users = append(users, serverID(u))
				}
			}
		case *tg.MessageService:
			addPeer(m.PeerID)
			if m.FromID != nil {
				addPeer(m.FromID)
			}
			switch act := m.Action.(type) {
			case *tg.MessageActionChatAddUser:
				for _, u := range act.Users {
					users = append(users, serverID(u))
				}
			case *tg.MessageActionChatDeleteUser:
				users = append(users, serverID(act.UserID))
			case *tg.MessageActionChatJoinedByLink:
				users = append(users, serverID(act.InviterID))
			case *tg.MessageActionPrizeStars:
				addPeer(act.BoostPeer)
			case *tg.MessageActionGiftCode:
				if act.BoostPeer != nil {
					addPeer(act.BoostPeer)
				}
			// a channel's gift post is the channel's and names the sender
			// only in the action
			case *tg.MessageActionStarGift:
				for _, p := range []tg.PeerClass{act.FromID, act.Peer} {
					if p != nil {
						addPeer(p)
					}
				}
			case *tg.MessageActionStarGiftUnique:
				for _, p := range []tg.PeerClass{act.FromID, act.Peer} {
					if p != nil {
						addPeer(p)
					}
				}
			}
		}
	}
	return
}

func updateMessage(u tg.UpdateClass) tg.MessageClass {
	switch v := u.(type) {
	case *tg.UpdateNewChannelMessage:
		return v.Message
	case *tg.UpdateEditChannelMessage:
		return v.Message
	case *tg.UpdateNewMessage:
		return v.Message
	case *tg.UpdateEditMessage:
		return v.Message
	}
	return nil
}

func updateChannelID(u tg.UpdateClass) int64 {
	switch v := u.(type) {
	case *tg.UpdateChannel:
		return v.ChannelID
	case *tg.UpdateDeleteChannelMessages:
		return v.ChannelID
	case *tg.UpdateReadChannelInbox:
		return v.ChannelID
	case *tg.UpdateReadChannelOutbox:
		return v.ChannelID
	case *tg.UpdateChannelUserTyping:
		return v.ChannelID
	case *tg.UpdatePinnedChannelMessages:
		return v.ChannelID
	case *tg.UpdateChannelReadMessagesContents:
		return v.ChannelID
	case *tg.UpdateDraftMessage:
		if p, ok := v.Peer.(*tg.PeerChannel); ok {
			return p.ChannelID
		}
	case *tg.UpdateNotifySettings:
		if n, ok := v.Peer.(*tg.NotifyPeer); ok {
			if p, ok := n.Peer.(*tg.PeerChannel); ok {
				return p.ChannelID
			}
		}
	case *tg.UpdateMessageReactions:
		if p, ok := v.Peer.(*tg.PeerChannel); ok {
			return p.ChannelID
		}
	}
	return 0
}

// deliverChannel pushes channel events: each recipient gets the update
// with their own out flags and the chat objects as they see them.
func (a *API) deliverChannel(events []channels.Event, except edge.Session) {
	for _, e := range events {
		var users, chans []int64
		if m := updateMessage(e.Update); m != nil {
			users, chans = mentioned(m)
		}
		if id := updateChannelID(e.Update); id != 0 {
			chans = append(chans, id)
		}
		users = append(users, reactionUsers(e.Update)...)
		if t, ok := e.Update.(*tg.UpdateChannelUserTyping); ok {
			if p, ok := t.FromID.(*tg.PeerUser); ok {
				users = append(users, serverID(p.UserID))
			}
		}
		// every viewer's job and the caller's reply would otherwise encode
		// the same message at once, and td's Encode writes flags as it goes:
		// encode it here, each job decodes its own copy
		var raw bin.Buffer
		if err := e.Update.Encode(&raw); err != nil {
			slog.Warn("api: channel deliver encode", "err", err)
			continue
		}
		for _, viewer := range e.Users {
			// most of a big group is offline; their user and chat
			// objects cost queries nobody reads
			if !a.hub.online(viewer) {
				continue
			}
			a.out.add(viewer, func(ctx context.Context) {
				own, err := tg.DecodeUpdate(&bin.Buffer{Buf: raw.Copy()})
				if err != nil {
					slog.Warn("api: channel deliver decode", "err", err)
					return
				}
				u := channels.MarkMentioned(channels.SetOut(own, viewer), viewer, e.Mentions)
				us, err := a.usersFor(ctx, viewer, users)
				if err != nil {
					slog.Warn("api: channel deliver users", "err", err)
					return
				}
				cs, err := a.chatsFor(ctx, viewer, chans)
				if err != nil {
					slog.Warn("api: channel deliver chats", "err", err)
					return
				}
				a.push(ctx, viewer, &tg.Updates{Updates: []tg.UpdateClass{u}, Users: us, Chats: cs, Date: now()}, except)
			})
		}
	}
}
