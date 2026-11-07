package api

import (
	"context"
	"strings"
	"unicode/utf16"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/messages"
)

func (a *API) registerMentions() {
	handle(a, tlprofile.SemanticMethodMessagesGetUnreadMentions, a.unreadMentions)
	handle(a, tlprofile.SemanticMethodMessagesReadMentions, a.readMentions)
	handle(a, tlprofile.SemanticMethodChannelsReadMessageContents, a.channelReadContents)
	handle(a, tlprofile.SemanticMethodMessagesReadMessageContents, a.readContents)
}

// mentionsIn turns the input-only mention entity into the stored one and
// lists who the text names, by id or by @username.
func (a *API) mentionsIn(ctx context.Context, me account.User, text string, list []tg.MessageEntityClass) ([]tg.MessageEntityClass, []int64) {
	var units []uint16
	var users []int64
	out := make([]tg.MessageEntityClass, 0, len(list))
	for _, e := range list {
		switch v := e.(type) {
		case *tg.InputMessageEntityMentionName:
			id, err := a.inputUser(ctx, me, v.UserID)
			if err != nil {
				// a name nobody can resolve stays plain text
				continue
			}
			users = append(users, id)
			out = append(out, &tg.MessageEntityMentionName{Offset: v.Offset, Length: v.Length, UserID: clientID(id)})
			continue
		case *tg.MessageEntityMentionName:
			users = append(users, serverID(v.UserID))
		case *tg.MessageEntityMention:
			if units == nil {
				units = utf16.Encode([]rune(text))
			}
			if v.Offset >= 0 && v.Length > 1 && v.Offset+v.Length <= len(units) {
				name := strings.TrimPrefix(string(utf16.Decode(units[v.Offset:v.Offset+v.Length])), "@")
				if u, ok, err := a.accounts.UserByUsername(ctx, name); err == nil && ok {
					users = append(users, u.ID)
				}
			}
		}
		out = append(out, e)
	}
	return out, users
}

// markMentions sets mentioned/media_unread on the channel messages that
// mention the viewer.
func (a *API) markMentions(ctx context.Context, viewer, channel int64, list []tg.MessageClass) error {
	ids := make([]int, 0, len(list))
	for _, m := range list {
		if msg, ok := m.(*tg.Message); ok {
			ids = append(ids, msg.ID)
		}
	}
	got, err := a.chans.Mentions(ctx, channel, viewer, ids)
	if err != nil || len(got) == 0 {
		return err
	}
	for _, m := range list {
		if msg, ok := m.(*tg.Message); ok {
			if unread, ok := got[msg.ID]; ok {
				msg.Mentioned, msg.MediaUnread = true, unread
			}
		}
	}
	return nil
}

func (a *API) unreadMentions(ctx context.Context, r *tg.MessagesGetUnreadMentionsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if to.channel == 0 {
		return &tg.MessagesMessages{Messages: []tg.MessageClass{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}, nil
	}
	return a.filtered(ctx, me, to, messages.UnreadMentions, r.OffsetID, r.AddOffset, r.Limit, r.MaxID, r.MinID)
}

// readMentions moves no pts, as Telegram; the app clears its counter.
func (a *API) readMentions(ctx context.Context, r *tg.MessagesReadMentionsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if to.channel == 0 {
		st, err := a.msgs.State(ctx, me.ID)
		if err != nil {
			return nil, err
		}
		return &tg.MessagesAffectedHistory{Pts: st.Pts}, nil
	}
	c, _, err := a.chans.Access(ctx, to.channel, me.ID)
	if err != nil {
		return nil, err
	}
	read, err := a.chans.ReadMentions(ctx, me.ID, c.ID, nil)
	if err != nil {
		return nil, err
	}
	a.contentsRead(me.ID, c.ID, read, except(ctx))
	return &tg.MessagesAffectedHistory{Pts: c.Pts}, nil
}

// channelReadContents is the app scrolling past mentions (and voice
// notes, which have nothing to mark here yet).
func (a *API) channelReadContents(ctx context.Context, r *tg.ChannelsReadMessageContentsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	if _, _, err := a.chans.Access(ctx, id, me.ID); err != nil {
		return nil, err
	}
	read, err := a.chans.ReadMentions(ctx, me.ID, id, r.ID)
	if err != nil {
		return nil, err
	}
	a.contentsRead(me.ID, id, read, except(ctx))
	return true, nil
}

// contentsRead tells the user's other sessions.
func (a *API) contentsRead(user, channel int64, read []int, skip edge.Session) {
	if len(read) == 0 {
		return
	}
	a.deliverChannel([]channels.Event{{Users: []int64{user}, Update: &tg.UpdateChannelReadMessagesContents{ChannelID: channel, Messages: read}}}, skip)
}

// readContents in a private chat: voice notes and mentions aren't
// tracked there, so nothing changes.
func (a *API) readContents(ctx context.Context, _ *tg.MessagesReadMessageContentsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	st, err := a.msgs.State(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesAffectedMessages{Pts: st.Pts}, nil
}
