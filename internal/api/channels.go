package api

import (
	"context"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/stars"
)

func channelErr(msg string) error { return tgerr.New(400, msg) }

func (a *API) registerChannels() {
	handle(a, tlprofile.SemanticMethodChannelsCreateChannel, a.createChannel)
	handle(a, tlprofile.SemanticMethodMessagesCreateChat, a.createChat)
	handle(a, tlprofile.SemanticMethodChannelsInviteToChannel, a.inviteToChannel)
	handle(a, tlprofile.SemanticMethodChannelsJoinChannel, a.joinChannel)
	handle(a, tlprofile.SemanticMethodChannelsLeaveChannel, a.leaveChannel)
	handle(a, tlprofile.SemanticMethodChannelsDeleteChannel, a.deleteChannel)
	handle(a, tlprofile.SemanticMethodChannelsGetChannels, a.getChannels)
	handle(a, tlprofile.SemanticMethodChannelsGetFullChannel, a.getFullChannel)
	handle(a, tlprofile.SemanticMethodChannelsGetParticipants, a.getParticipants)
	handle(a, tlprofile.SemanticMethodChannelsGetParticipant, a.getParticipant)
	handle(a, tlprofile.SemanticMethodChannelsEditAdmin, a.editAdmin)
	handle(a, tlprofile.SemanticMethodChannelsEditBanned, a.editBanned)
	handle(a, tlprofile.SemanticMethodChannelsEditTitle, a.editTitle)
	handle(a, tlprofile.SemanticMethodMessagesEditChatAbout, a.editAbout)
	handle(a, tlprofile.SemanticMethodChannelsCheckUsername, a.checkUsername)
	handle(a, tlprofile.SemanticMethodChannelsUpdateUsername, a.updateUsername)
	handle(a, tlprofile.SemanticMethodMessagesEditChatDefaultBannedRights, a.editDefaultBanned)
	handle(a, tlprofile.SemanticMethodChannelsReadHistory, a.channelReadHistory)
	handle(a, tlprofile.SemanticMethodChannelsDeleteMessages, a.channelDeleteMessages)
	handle(a, tlprofile.SemanticMethodChannelsGetMessages, a.channelGetMessages)
	handle(a, tlprofile.SemanticMethodUpdatesGetChannelDifference, a.getChannelDifference)
	handle(a, tlprofile.SemanticMethodMessagesExportChatInvite, a.exportInvite)
	handle(a, tlprofile.SemanticMethodMessagesGetExportedChatInvites, a.getInvites)
	handle(a, tlprofile.SemanticMethodMessagesEditExportedChatInvite, a.editInvite)
	handle(a, tlprofile.SemanticMethodMessagesDeleteExportedChatInvite, a.deleteInvite)
	handle(a, tlprofile.SemanticMethodMessagesCheckChatInvite, a.checkInvite)
	handle(a, tlprofile.SemanticMethodMessagesImportChatInvite, a.importInvite)
	handle(a, tlprofile.SemanticMethodChannelsSetDiscussionGroup, a.setDiscussion)
	handle(a, tlprofile.SemanticMethodChannelsGetGroupsForDiscussion, a.groupsForDiscussion)
	handle(a, tlprofile.SemanticMethodMessagesGetDiscussionMessage, a.discussionMessage)
	handle(a, tlprofile.SemanticMethodMessagesGetReplies, a.getReplies)
}

// opUpdates is the answer to a channel operation: the caller's share of
// the events plus the chat and users, the rest goes out as pushes.
func (a *API) opUpdates(ctx context.Context, me account.User, channel int64, events []channels.Event) (*tg.Updates, error) {
	out := &tg.Updates{Updates: []tg.UpdateClass{}, Date: now()}
	var users []int64
	for _, e := range events {
		for _, u := range e.Users {
			if u == me.ID {
				out.Updates = append(out.Updates, channels.SetOut(e.Update, me.ID))
				if m := updateMessage(e.Update); m != nil {
					us, _ := mentioned(m)
					users = append(users, us...)
				}
				users = append(users, reactionUsers(e.Update)...)
				break
			}
		}
	}
	var err error
	if out.Users, err = a.usersFor(ctx, me.ID, append(users, me.ID)); err != nil {
		return nil, err
	}
	if out.Chats, err = a.chatsFor(ctx, me.ID, []int64{channel}); err != nil {
		return nil, err
	}
	a.deliverChannel(events, except(ctx))
	return out, nil
}

func (a *API) createChannel(ctx context.Context, r *tg.ChannelsCreateChannelRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	c, events, err := a.chans.Create(ctx, me.ID, r.Title, r.About, !r.Broadcast)
	if err != nil {
		return nil, err
	}
	return a.opUpdates(ctx, me, c.ID, events)
}

// createChat makes a supergroup: there are no basic groups here.
func (a *API) createChat(ctx context.Context, r *tg.MessagesCreateChatRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	c, events, err := a.chans.Create(ctx, me.ID, r.Title, "", true)
	if err != nil {
		return nil, err
	}
	var users []int64
	for _, in := range r.Users {
		if id, err := a.inputUser(ctx, me, in); err == nil && id != me.ID {
			users = append(users, id)
		}
	}
	users, refused := a.invitable(ctx, me.ID, users)
	if c.Megagroup {
		var noGroups []int64
		users, noGroups = a.groupBots(ctx, users)
		refused = append(refused, noGroups...)
	}
	more, missing, err := a.chans.Invite(ctx, me.ID, c.ID, users)
	if err != nil {
		return nil, err
	}
	missing = append(missing, refused...)
	up, err := a.opUpdates(ctx, me, c.ID, append(events, more...))
	if err != nil {
		return nil, err
	}
	return &tg.MessagesInvitedUsers{Updates: up, MissingInvitees: missingTL(missing)}, nil
}

// groupBots takes out bots whose owner turned off Allow Groups in BotFather.
func (a *API) groupBots(ctx context.Context, users []int64) (ok, refused []int64) {
	if a.bots == nil {
		return users, nil
	}
	found, err := a.bots.Many(ctx, users)
	if err != nil {
		return users, nil
	}
	for _, u := range users {
		if b, isBot := found[u]; isBot && !b.State.AllowGroups() {
			refused = append(refused, u)
		} else {
			ok = append(ok, u)
		}
	}
	return ok, refused
}

// invitable splits users by their chatInvite privacy towards me; people
// who blocked me refuse whatever their rule says.
func (a *API) invitable(ctx context.Context, me int64, users []int64) (ok, refused []int64) {
	au, err := a.audienceFor(ctx, me, users, "chatInvite")
	if err != nil {
		return nil, users
	}
	for _, u := range users {
		if au.can(u, "chatInvite") {
			ok = append(ok, u)
		} else {
			refused = append(refused, u)
		}
	}
	return ok, refused
}

func missingTL(list []int64) []tg.MissingInvitee {
	out := []tg.MissingInvitee{}
	for _, id := range list {
		out = append(out, tg.MissingInvitee{UserID: clientID(id)})
	}
	return out
}

func (a *API) inviteToChannel(ctx context.Context, r *tg.ChannelsInviteToChannelRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	var users []int64
	for _, in := range r.Users {
		if u, err := a.inputUser(ctx, me, in); err == nil {
			users = append(users, u)
		}
	}
	users, refused := a.invitable(ctx, me.ID, users)
	var noGroups []int64
	if c, _, err := a.chans.Access(ctx, id, me.ID); err == nil && c.Megagroup {
		users, noGroups = a.groupBots(ctx, users)
		refused = append(refused, noGroups...)
	}
	events, missing, err := a.chans.Invite(ctx, me.ID, id, users)
	if err != nil {
		return nil, err
	}
	missing = append(missing, refused...)
	up, err := a.opUpdates(ctx, me, id, events)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesInvitedUsers{Updates: up, MissingInvitees: missingTL(missing)}, nil
}

func (a *API) joinChannel(ctx context.Context, r *tg.ChannelsJoinChannelRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	events, err := a.chans.Join(ctx, me.ID, id, 0)
	if err != nil {
		return nil, err
	}
	up, err := a.opUpdates(ctx, me, id, events)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesChatInviteJoinResultOk{Updates: up}, nil
}

func (a *API) leaveChannel(ctx context.Context, r *tg.ChannelsLeaveChannelRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	events, err := a.chans.Leave(ctx, me.ID, id)
	if err != nil {
		return nil, err
	}
	return a.opUpdates(ctx, me, id, events)
}

func (a *API) deleteChannel(ctx context.Context, r *tg.ChannelsDeleteChannelRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	events, err := a.chans.Delete(ctx, me.ID, id)
	if err != nil {
		return nil, err
	}
	return a.opUpdates(ctx, me, id, events)
}

func (a *API) getChannels(ctx context.Context, r *tg.ChannelsGetChannelsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	var list []int64
	for _, in := range r.ID {
		if id, err := inputChannel(in); err == nil {
			list = append(list, id)
		}
	}
	chats, err := a.chatsFor(ctx, me.ID, list)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesChats{Chats: chats}, nil
}

func (a *API) getFullChannel(ctx context.Context, r *tg.ChannelsGetFullChannelRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	c, m, err := a.chans.Access(ctx, id, me.ID)
	if err != nil {
		return nil, err
	}
	_, admins, err := a.chans.Members(ctx, id, "admins", "", 0, 1)
	if err != nil {
		return nil, err
	}
	isAdmin := m.Status == channels.Creator || m.Status == channels.Admin
	full := &tg.ChannelFull{
		ID:                  c.ID,
		About:               c.About,
		ReadInboxMaxID:      m.ReadInboxMax,
		ReadOutboxMaxID:     c.ReadOutboxMax,
		ChatPhoto:           a.fullPhoto(ctx, c.PhotoID),
		NotifySettings:      tg.PeerNotifySettings{},
		BotInfo:             []tg.BotInfo{},
		Pts:                 c.Pts,
		CanViewParticipants: c.Megagroup || isAdmin,
		CanSetUsername:      m.Status == channels.Creator,
		ViewForumAsMessages: m.ForumAsMessages,
	}
	full.SetParticipantsCount(c.Count)
	full.SetAdminsCount(admins)
	full.SetAvailableReactions(&tg.ChatReactionsAll{AllowCustom: true})
	full.PaidReactionsAvailable = true
	if full.NotifySettings, err = a.peerNotifyOf(ctx, me.ID, peer{channel: id}); err != nil {
		return nil, err
	}
	if pin, err := a.chans.TopPinned(ctx, id); err != nil {
		return nil, err
	} else if pin != 0 {
		full.SetPinnedMsgID(pin)
	}
	// who got banned is for admins, as in getParticipants
	if isAdmin {
		_, kicked, err := a.chans.Members(ctx, id, "kicked", "", 0, 1)
		if err != nil {
			return nil, err
		}
		_, banned, err := a.chans.Members(ctx, id, "banned", "", 0, 1)
		if err != nil {
			return nil, err
		}
		full.SetKickedCount(kicked)
		full.SetBannedCount(banned)
	}
	if c.LinkedID != 0 {
		full.SetLinkedChatID(c.LinkedID)
	}
	if isAdmin {
		if inv, err := a.chans.PermanentInvite(ctx, me.ID, id); err == nil {
			full.SetExportedInvite(inv.TL(a.in.BaseURL))
		}
	}
	if a.stars != nil {
		if st, err := a.stars.ChannelBoosts(ctx, me.ID, id); err == nil && len(st.Mine) > 0 {
			full.SetBoostsApplied(len(st.Mine))
		}
	}
	// a broadcast channel can hold collectibles people transfer to it
	if a.stars != nil && !c.Megagroup {
		full.StargiftsAvailable = true
		if n, err := a.stars.GiftCount(ctx, stars.Holder{Channel: id}); err == nil && n > 0 {
			full.SetStargiftsCount(n)
		}
	}
	list := []int64{id}
	if c.LinkedID != 0 {
		list = append(list, c.LinkedID)
	}
	chats, err := a.chatsFor(ctx, me.ID, list)
	if err != nil {
		return nil, err
	}
	users, err := a.usersFor(ctx, me.ID, []int64{me.ID})
	if err != nil {
		return nil, err
	}
	return &tg.MessagesChatFull{FullChat: full, Chats: chats, Users: users}, nil
}

func participantTL(m channels.Membership, viewer int64) tg.ChannelParticipantClass {
	uid := clientID(m.UserID)
	switch m.Status {
	case channels.Creator:
		p := &tg.ChannelParticipantCreator{UserID: uid, AdminRights: m.AdminRights}
		if m.Rank != "" {
			p.SetRank(m.Rank)
		}
		return p
	case channels.Admin:
		p := &tg.ChannelParticipantAdmin{UserID: uid, PromotedBy: clientID(m.PromotedBy), Date: m.Date, AdminRights: m.AdminRights, Self: m.UserID == viewer}
		if m.Inviter != 0 {
			p.SetInviterID(clientID(m.Inviter))
		}
		if m.Rank != "" {
			p.SetRank(m.Rank)
		}
		return p
	case channels.Restricted, channels.Kicked:
		return &tg.ChannelParticipantBanned{Peer: &tg.PeerUser{UserID: uid}, KickedBy: 0, Date: m.Date, BannedRights: m.BannedRights, Left: m.Status == channels.Kicked}
	case channels.Left:
		return &tg.ChannelParticipantLeft{Peer: &tg.PeerUser{UserID: uid}}
	}
	if m.UserID == viewer {
		return &tg.ChannelParticipantSelf{UserID: uid, InviterID: clientID(m.Inviter), Date: m.Date}
	}
	return &tg.ChannelParticipant{UserID: uid, Date: m.Date}
}

func (a *API) getParticipants(ctx context.Context, r *tg.ChannelsGetParticipantsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	c, m, err := a.chans.Access(ctx, id, me.ID)
	if err != nil {
		return nil, err
	}
	filter, q := "recent", ""
	switch f := r.Filter.(type) {
	case *tg.ChannelParticipantsAdmins:
		filter = "admins"
	case *tg.ChannelParticipantsKicked:
		filter, q = "kicked", f.Q
	case *tg.ChannelParticipantsBanned:
		filter, q = "banned", f.Q
	case *tg.ChannelParticipantsSearch:
		q = f.Q
	case *tg.ChannelParticipantsBots:
		return &tg.ChannelsChannelParticipants{Participants: []tg.ChannelParticipantClass{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}, nil
	}
	// a broadcast channel's members are only for its admins
	isAdmin := m.Status == channels.Creator || m.Status == channels.Admin
	if !c.Megagroup && !isAdmin && filter != "admins" {
		return nil, tgerr.New(400, "CHAT_ADMIN_REQUIRED")
	}
	// and so is who got banned, in groups too
	if (filter == "kicked" || filter == "banned") && !isAdmin {
		return nil, tgerr.New(400, "CHAT_ADMIN_REQUIRED")
	}
	list, total, err := a.chans.Members(ctx, id, filter, q, r.Offset, r.Limit)
	if err != nil {
		return nil, err
	}
	out := &tg.ChannelsChannelParticipants{Count: total, Participants: []tg.ChannelParticipantClass{}, Chats: []tg.ChatClass{}}
	var who []int64
	for _, p := range list {
		out.Participants = append(out.Participants, participantTL(p, me.ID))
		who = append(who, p.UserID)
	}
	out.Users, err = a.usersFor(ctx, me.ID, who)
	return out, err
}

func (a *API) getParticipant(ctx context.Context, r *tg.ChannelsGetParticipantRequest) (any, error) {
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
	p, err := a.peer(ctx, me, r.Participant)
	if err != nil || p.user == 0 {
		return nil, tgerr.New(400, "PARTICIPANT_ID_INVALID")
	}
	m, err := a.chans.Member(ctx, id, p.user)
	if err != nil {
		return nil, err
	}
	if !channels.InChat(m.Status) && m.Status != channels.Kicked {
		return nil, tgerr.New(400, "USER_NOT_PARTICIPANT")
	}
	users, err := a.usersFor(ctx, me.ID, []int64{p.user})
	if err != nil {
		return nil, err
	}
	return &tg.ChannelsChannelParticipant{Participant: participantTL(m, me.ID), Users: users, Chats: []tg.ChatClass{}}, nil
}

func (a *API) editAdmin(ctx context.Context, r *tg.ChannelsEditAdminRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	target, err := a.inputUser(ctx, me, r.UserID)
	if err != nil {
		return nil, err
	}
	events, err := a.chans.EditAdmin(ctx, me.ID, id, target, r.AdminRights, r.Rank)
	if err != nil {
		return nil, err
	}
	return a.opUpdates(ctx, me, id, events)
}

func (a *API) editBanned(ctx context.Context, r *tg.ChannelsEditBannedRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	p, err := a.peer(ctx, me, r.Participant)
	if err != nil || p.user == 0 {
		return nil, tgerr.New(400, "PARTICIPANT_ID_INVALID")
	}
	events, err := a.chans.EditBanned(ctx, me.ID, id, p.user, r.BannedRights)
	if err != nil {
		return nil, err
	}
	return a.opUpdates(ctx, me, id, events)
}

func (a *API) editTitle(ctx context.Context, r *tg.ChannelsEditTitleRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	events, err := a.chans.EditInfo(ctx, me.ID, id, &r.Title, nil)
	if err != nil {
		return nil, err
	}
	return a.opUpdates(ctx, me, id, events)
}

func (a *API) editAbout(ctx context.Context, r *tg.MessagesEditChatAboutRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	p, err := a.peer(ctx, me, r.Peer)
	if err != nil || p.channel == 0 {
		return nil, errPeerInvalid
	}
	events, err := a.chans.EditInfo(ctx, me.ID, p.channel, nil, &r.About)
	if err != nil {
		return nil, err
	}
	a.deliverChannel(events, except(ctx))
	return true, nil
}

func (a *API) checkUsername(ctx context.Context, r *tg.ChannelsCheckUsernameRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, _ := inputChannel(r.Channel)
	return a.chans.UsernameFreeFor(ctx, me.ID, r.Username, id)
}

func (a *API) updateUsername(ctx context.Context, r *tg.ChannelsUpdateUsernameRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	events, err := a.chans.SetUsername(ctx, me.ID, id, r.Username)
	if err != nil {
		return nil, err
	}
	a.deliverChannel(events, except(ctx))
	return true, nil
}

func (a *API) editDefaultBanned(ctx context.Context, r *tg.MessagesEditChatDefaultBannedRightsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	p, err := a.peer(ctx, me, r.Peer)
	if err != nil || p.channel == 0 {
		return nil, errPeerInvalid
	}
	events, err := a.chans.SetDefaultBanned(ctx, me.ID, p.channel, r.BannedRights)
	if err != nil {
		return nil, err
	}
	return a.opUpdates(ctx, me, p.channel, events)
}

func (a *API) channelReadHistory(ctx context.Context, r *tg.ChannelsReadHistoryRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	inbox, events, err := a.chans.ReadHistory(ctx, me.ID, id, r.MaxID)
	if err != nil {
		return nil, err
	}
	if inbox != nil {
		a.echo(ctx, me, []tg.UpdateClass{inbox}, []tg.UserClass{})
	}
	a.deliverChannel(events, except(ctx))
	return true, nil
}

func (a *API) channelDeleteMessages(ctx context.Context, r *tg.ChannelsDeleteMessagesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	res, events, err := a.chans.DeleteMessages(ctx, me.ID, id, r.ID)
	if err != nil {
		return nil, err
	}
	a.deliverChannel(events, except(ctx))
	return &res, nil
}

// channelBody renders channel messages for viewer, with comment counters
// on posts of a channel that has a discussion group.
func (a *API) channelBody(ctx context.Context, me account.User, c channels.Channel, list []channels.Msg) ([]tg.MessageClass, []tg.UserClass, []tg.ChatClass, error) {
	out := make([]tg.MessageClass, 0, len(list))
	var all []tg.MessageClass
	for _, m := range list {
		tl := channels.MarkOut(m.TL(), me.ID)
		if msg, ok := tl.(*tg.Message); ok && m.Post && c.LinkedID != 0 {
			if cp, group, err := a.chans.DiscussionCopy(ctx, c.ID, m.ID); err == nil {
				n, maxID, _ := a.chans.Replies(ctx, group, cp.ID)
				rep := tg.MessageReplies{Comments: true, Replies: n}
				rep.SetChannelID(group)
				if maxID != 0 {
					rep.SetMaxID(maxID)
				}
				cpy := *msg
				cpy.SetReplies(rep)
				tl = &cpy
			}
		}
		out = append(out, tl)
		all = append(all, tl)
	}
	ids := make([]int, 0, len(list))
	for _, m := range list {
		ids = append(ids, m.ID)
	}
	sets, err := a.chans.Reactions(ctx, c.ID, ids)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := a.markMentions(ctx, me.ID, c.ID, out); err != nil {
		return nil, nil, nil, err
	}
	if !c.Megagroup {
		views, err := a.chans.Views(ctx, c.ID, ids)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, tl := range out {
			if msg, ok := tl.(*tg.Message); ok {
				msg.SetViews(views[msg.ID])
			}
		}
	}
	users, chans := mentioned(all...)
	users = append(users, withReactions(out, sets, me.ID, c.Megagroup)...)
	users = append(users, a.livePolls(ctx, me.ID, out)...)
	chans = append(chans, c.ID)
	if c.LinkedID != 0 {
		chans = append(chans, c.LinkedID)
	}
	us, err := a.usersFor(ctx, me.ID, users)
	if err != nil {
		return nil, nil, nil, err
	}
	cs, err := a.chatsFor(ctx, me.ID, chans)
	return out, us, cs, err
}

func (a *API) channelGetMessages(ctx context.Context, r *tg.ChannelsGetMessagesRequest) (any, error) {
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
	var want, replyOf []int
	for _, in := range r.ID {
		switch v := in.(type) {
		case *tg.InputMessageID:
			want = append(want, v.ID)
		case *tg.InputMessageReplyTo:
			replyOf = append(replyOf, v.ID)
		}
	}
	if len(replyOf) > 0 {
		quoted, err := a.chans.Messages(ctx, id, replyOf)
		if err != nil {
			return nil, err
		}
		for _, m := range quoted {
			if m.ReplyTo != 0 {
				want = append(want, m.ReplyTo)
			}
		}
	}
	list, err := a.chans.Messages(ctx, id, want)
	if err != nil {
		return nil, err
	}
	msgs, users, chats, err := a.channelBody(ctx, me, c, list)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesChannelMessages{Pts: c.Pts, Count: len(msgs), Messages: msgs, Topics: []tg.ForumTopicClass{}, Chats: chats, Users: users}, nil
}

// channelPollTimeout is how long clients wait before polling a channel they
// don't get pushes for; without it Web A polls every second.
const channelPollTimeout = 30

func (a *API) getChannelDifference(ctx context.Context, r *tg.UpdatesGetChannelDifferenceRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	c, m, err := a.chans.Access(ctx, id, me.ID)
	if err != nil {
		return nil, err
	}
	d, err := a.chans.Difference(ctx, id, r.Pts, r.Limit)
	if err != nil {
		return nil, err
	}
	if d.TooLong {
		// the client drops its copy and reloads from this dialog
		list, _, err := a.chans.History(ctx, id, channels.HistoryQuery{Limit: 20})
		if err != nil {
			return nil, err
		}
		msgs, users, chats, err := a.channelBody(ctx, me, c, list)
		if err != nil {
			return nil, err
		}
		dlg := &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: id}, TopMessage: c.TopID, ReadInboxMaxID: m.ReadInboxMax,
			ReadOutboxMaxID: c.ReadOutboxMax, NotifySettings: tg.PeerNotifySettings{}}
		dlg.SetPts(c.Pts)
		return &tg.UpdatesChannelDifferenceTooLong{Final: true, Timeout: channelPollTimeout, Dialog: dlg, Messages: msgs, Chats: chats, Users: users}, nil
	}
	if len(d.Updates) == 0 {
		return &tg.UpdatesChannelDifferenceEmpty{Final: true, Pts: d.Pts, Timeout: channelPollTimeout}, nil
	}
	newMsgs := []tg.MessageClass{}
	other := []tg.UpdateClass{}
	var all []tg.MessageClass
	for _, u := range d.Updates {
		u = channels.SetOut(u, me.ID)
		if nm, ok := u.(*tg.UpdateNewChannelMessage); ok {
			newMsgs = append(newMsgs, nm.Message)
			all = append(all, nm.Message)
			continue
		}
		if em := updateMessage(u); em != nil {
			all = append(all, em)
		}
		other = append(other, u)
	}
	if err := a.markMentions(ctx, me.ID, id, newMsgs); err != nil {
		return nil, err
	}
	users, chans := mentioned(all...)
	us, err := a.usersFor(ctx, me.ID, users)
	if err != nil {
		return nil, err
	}
	cs, err := a.chatsFor(ctx, me.ID, append(chans, id))
	if err != nil {
		return nil, err
	}
	return &tg.UpdatesChannelDifference{Final: d.Final, Pts: d.Pts, Timeout: channelPollTimeout, NewMessages: newMsgs, OtherUpdates: other, Chats: cs, Users: us}, nil
}

func (a *API) exportInvite(ctx context.Context, r *tg.MessagesExportChatInviteRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	p, err := a.peer(ctx, me, r.Peer)
	if err != nil || p.channel == 0 {
		return nil, errPeerInvalid
	}
	inv, err := a.chans.ExportInvite(ctx, me.ID, p.channel, r.Title, r.ExpireDate, r.UsageLimit, r.LegacyRevokePermanent)
	if err != nil {
		return nil, err
	}
	return inv.TL(a.in.BaseURL), nil
}

func (a *API) getInvites(ctx context.Context, r *tg.MessagesGetExportedChatInvitesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	p, err := a.peer(ctx, me, r.Peer)
	if err != nil || p.channel == 0 {
		return nil, errPeerInvalid
	}
	admin, err := a.inputUser(ctx, me, r.AdminID)
	if err != nil {
		return nil, err
	}
	list, total, err := a.chans.Invites(ctx, me.ID, p.channel, admin, r.Revoked, r.Limit)
	if err != nil {
		return nil, err
	}
	out := &tg.MessagesExportedChatInvites{Count: total, Invites: []tg.ExportedChatInviteClass{}}
	for _, inv := range list {
		out.Invites = append(out.Invites, inv.TL(a.in.BaseURL))
	}
	out.Users, err = a.usersFor(ctx, me.ID, []int64{admin})
	return out, err
}

// inviteHash takes a full link or the bare hash.
func inviteHash(link string) string {
	link = strings.TrimSpace(link)
	if i := strings.LastIndex(link, "/+"); i >= 0 {
		return link[i+2:]
	}
	if i := strings.LastIndex(link, "joinchat/"); i >= 0 {
		return link[i+9:]
	}
	return strings.TrimPrefix(link, "+")
}

func (a *API) editInvite(ctx context.Context, r *tg.MessagesEditExportedChatInviteRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	var title *string
	if v, ok := r.GetTitle(); ok {
		title = &v
	}
	var expire, limit *int
	if v, ok := r.GetExpireDate(); ok {
		expire = &v
	}
	if v, ok := r.GetUsageLimit(); ok {
		limit = &v
	}
	inv, err := a.chans.EditInvite(ctx, me.ID, inviteHash(r.Link), r.Revoked, title, expire, limit)
	if err != nil {
		return nil, err
	}
	users, err := a.usersFor(ctx, me.ID, []int64{inv.Creator})
	if err != nil {
		return nil, err
	}
	return &tg.MessagesExportedChatInvite{Invite: inv.TL(a.in.BaseURL), Users: users}, nil
}

func (a *API) deleteInvite(ctx context.Context, r *tg.MessagesDeleteExportedChatInviteRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	return true, a.chans.DeleteInvite(ctx, me.ID, inviteHash(r.Link))
}

func (a *API) checkInvite(ctx context.Context, r *tg.MessagesCheckChatInviteRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	_, c, err := a.chans.CheckInvite(ctx, inviteHash(r.Hash))
	if err != nil {
		return nil, err
	}
	m, err := a.chans.Member(ctx, c.ID, me.ID)
	if err != nil {
		return nil, err
	}
	if channels.InChat(m.Status) {
		return &tg.ChatInviteAlready{Chat: c.TL(m)}, nil
	}
	out := &tg.ChatInvite{Title: c.Title, Photo: &tg.PhotoEmpty{}, ParticipantsCount: c.Count, Channel: true,
		Megagroup: c.Megagroup, Broadcast: !c.Megagroup, Public: c.Username != "", Color: 0}
	if c.About != "" {
		out.SetAbout(c.About)
	}
	return out, nil
}

func (a *API) importInvite(ctx context.Context, r *tg.MessagesImportChatInviteRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, events, err := a.chans.ImportInvite(ctx, me.ID, inviteHash(r.Hash))
	if err != nil {
		return nil, err
	}
	up, err := a.opUpdates(ctx, me, id, events)
	if err != nil {
		return nil, err
	}
	// layer 232 wraps it; tlprofile unwraps for older clients
	return &tg.MessagesChatInviteJoinResultOk{Updates: up}, nil
}

func (a *API) setDiscussion(ctx context.Context, r *tg.ChannelsSetDiscussionGroupRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	b, err := inputChannel(r.Broadcast)
	if err != nil {
		return nil, err
	}
	g, _ := inputChannel(r.Group) // inputChannelEmpty unlinks
	events, err := a.chans.SetDiscussion(ctx, me.ID, b, g)
	if err != nil {
		return nil, err
	}
	a.deliverChannel(events, edge.Session{})
	return true, nil
}

func (a *API) groupsForDiscussion(ctx context.Context, _ *tg.ChannelsGetGroupsForDiscussionRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	list, err := a.chans.DiscussionCandidates(ctx, me.ID)
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

// thread finds where comments on (peer, msg) live: a channel post's copy in
// the discussion group, or a group message itself.
func (a *API) thread(ctx context.Context, me account.User, in tg.InputPeerClass, msg int) (channels.Channel, int, error) {
	p, err := a.peer(ctx, me, in)
	if err != nil || p.channel == 0 {
		return channels.Channel{}, 0, errPeerInvalid
	}
	c, _, err := a.chans.Access(ctx, p.channel, me.ID)
	if err != nil {
		return c, 0, err
	}
	if c.Megagroup {
		return c, msg, nil
	}
	cp, group, err := a.chans.DiscussionCopy(ctx, c.ID, msg)
	if err != nil {
		return c, 0, err
	}
	g, ok, err := a.chans.Get(ctx, group)
	if err != nil || !ok {
		return c, 0, channelErr("MSG_ID_INVALID")
	}
	return g, cp.ID, nil
}

func (a *API) discussionMessage(ctx context.Context, r *tg.MessagesGetDiscussionMessageRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	g, top, err := a.thread(ctx, me, r.Peer, r.MsgID)
	if err != nil {
		return nil, err
	}
	list, err := a.chans.Messages(ctx, g.ID, []int{top})
	if err != nil || len(list) == 0 {
		return nil, channelErr("MSG_ID_INVALID")
	}
	msgs, users, chats, err := a.channelBody(ctx, me, g, list)
	if err != nil {
		return nil, err
	}
	n, maxID, err := a.chans.Replies(ctx, g.ID, top)
	if err != nil {
		return nil, err
	}
	gm, _ := a.chans.Member(ctx, g.ID, me.ID)
	out := &tg.MessagesDiscussionMessage{Messages: msgs, UnreadCount: 0, Chats: chats, Users: users}
	if maxID != 0 {
		out.SetMaxID(maxID)
	}
	if gm.ReadInboxMax != 0 {
		out.SetReadInboxMaxID(gm.ReadInboxMax)
	}
	_ = n
	return out, nil
}

func (a *API) getReplies(ctx context.Context, r *tg.MessagesGetRepliesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	g, top, err := a.thread(ctx, me, r.Peer, r.MsgID)
	if err != nil {
		return nil, err
	}
	q := channels.HistoryQuery{OffsetID: r.OffsetID, OffsetDate: r.OffsetDate,
		AddOffset: r.AddOffset, Limit: r.Limit, MaxID: r.MaxID, MinID: r.MinID, Thread: top}
	// in a forum a topic id (1 for General) asks for the topic, opening
	// message included; anything else is a reply thread as before
	topics := []tg.ForumTopicClass{}
	if g.Forum {
		ts, err := a.chans.TopicsByID(ctx, me.ID, g.ID, []int{top})
		if err != nil {
			return nil, err
		}
		if len(ts) == 1 {
			q.Thread, q.Topic = 0, top
			topics = append(topics, ts[0].TL(g.ID, me.ID))
		}
	}
	list, total, err := a.chans.History(ctx, g.ID, q)
	if err != nil {
		return nil, err
	}
	msgs, users, chats, err := a.channelBody(ctx, me, g, list)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesChannelMessages{Pts: g.Pts, Count: total, Messages: msgs, Topics: topics, Chats: chats, Users: users}, nil
}
