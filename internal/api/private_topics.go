package api

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/messages"
)

func isPrivateTopicPeer(input tg.InputPeerClass) bool {
	var _, ok = input.(*tg.InputPeerUser)
	return ok
}

func (a *API) createPrivateTopic(ctx context.Context, r *tg.MessagesCreateForumTopicRequest) (any, error) {
	var me account.User
	var target peer
	var sent messages.Sent
	var action = tg.MessageActionTopicCreate{Title: r.Title, IconColor: r.IconColor, IconEmojiID: r.IconEmojiID}
	var err error
	me, err = a.me(ctx)
	if err != nil {
		return nil, err
	}
	target, err = a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	err = a.mayWrite(ctx, me, target, false)
	if err != nil {
		return nil, err
	}
	sent, err = a.msgs.Send(ctx, me.ID, target.user, messages.Draft{Text: r.Title, Action: &action, RandomID: r.RandomID})
	if err != nil {
		return nil, err
	}
	return a.privateTopicUpdates(ctx, me, sent, r.RandomID)
}

func (a *API) privateTopicUpdates(ctx context.Context, me account.User, sent messages.Sent, random int64) (any, error) {
	var users []tg.UserClass
	var chats []tg.ChatClass
	var updates = []tg.UpdateClass{sent.Update}
	var err error
	_, users, chats, err = a.messagesBody(ctx, me, []messages.Msg{sent.Msg})
	if err != nil {
		return nil, err
	}
	a.Deliver(sent.Events)
	if random != 0 {
		updates = append([]tg.UpdateClass{&tg.UpdateMessageID{ID: sent.Msg.ID, RandomID: random}}, updates...)
	}
	return &tg.Updates{Updates: updates, Users: users, Chats: chats, Date: now()}, nil
}

func (a *API) editPrivateTopic(ctx context.Context, r *tg.MessagesEditForumTopicRequest) (any, error) {
	var me account.User
	var target peer
	var action = tg.MessageActionTopicEdit{}
	var sent messages.Sent
	var err error
	me, err = a.me(ctx)
	if err != nil {
		return nil, err
	}
	target, err = a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if r.Flags.Has(0) {
		action.SetTitle(r.Title)
	}
	if r.Flags.Has(1) {
		action.SetIconEmojiID(r.IconEmojiID)
	}
	if r.Flags.Has(2) {
		action.SetClosed(r.Closed)
	}
	sent, err = a.msgs.Send(ctx, me.ID, target.user, messages.Draft{Text: "Topic updated", Action: &action, Topic: r.TopicID})
	if err != nil {
		return nil, err
	}
	return a.privateTopicUpdates(ctx, me, sent, 0)
}

func (a *API) getPrivateTopics(ctx context.Context, input tg.InputPeerClass, ids []int, query string, offset, limit int) (any, error) {
	var me account.User
	var target peer
	var topics []tg.ForumTopic
	var index int
	var list = []tg.ForumTopicClass{}
	var topIDs []int
	var tops []messages.Msg
	var body []tg.MessageClass
	var users []tg.UserClass
	var chats []tg.ChatClass
	var err error
	me, err = a.me(ctx)
	if err != nil {
		return nil, err
	}
	target, err = a.peer(ctx, me, input)
	if err != nil {
		return nil, err
	}
	topics, err = a.msgs.PrivateTopics(ctx, me.ID, target.user, ids, query, offset, limit)
	if err != nil {
		return nil, err
	}
	for index = range topics {
		list = append(list, &topics[index])
		topIDs = append(topIDs, topics[index].TopMessage)
	}
	tops, err = a.msgs.Get(ctx, me.ID, topIDs)
	if err != nil {
		return nil, err
	}
	body, users, chats, err = a.messagesBody(ctx, me, tops)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesForumTopics{Count: len(topics), Topics: list, Messages: body, Users: users, Chats: chats}, nil
}

func (a *API) privateTopicReplies(ctx context.Context, r *tg.MessagesGetRepliesRequest) (any, error) {
	var me account.User
	var target peer
	var topics []tg.ForumTopic
	var list []messages.Msg
	var total int
	var body []tg.MessageClass
	var users []tg.UserClass
	var chats []tg.ChatClass
	var err error
	me, err = a.me(ctx)
	if err != nil {
		return nil, err
	}
	target, err = a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	topics, err = a.msgs.PrivateTopics(ctx, me.ID, target.user, []int{r.MsgID}, "", 0, 1)
	if err != nil {
		return nil, err
	}
	if len(topics) != 1 {
		return nil, tgerr.New(400, "TOPIC_ID_INVALID")
	}
	list, total, err = a.msgs.History(ctx, me.ID, target.user, messages.HistoryQuery{Topic: r.MsgID, OffsetID: r.OffsetID, OffsetDate: r.OffsetDate, AddOffset: r.AddOffset, Limit: r.Limit, MaxID: r.MaxID, MinID: r.MinID})
	if err != nil {
		return nil, err
	}
	body, users, chats, err = a.messagesBody(ctx, me, list)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesMessagesSlice{Count: total, Messages: body, Users: users, Chats: chats, Topics: []tg.ForumTopicClass{&topics[0]}}, nil
}

func (a *API) deletePrivateTopic(ctx context.Context, r *tg.MessagesDeleteTopicHistoryRequest) (any, error) {
	var me account.User
	var target peer
	var events []messages.Event
	var state tg.UpdatesState
	var err error
	me, err = a.me(ctx)
	if err != nil {
		return nil, err
	}
	target, err = a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	events, err = a.msgs.DeletePrivateTopic(ctx, me.ID, target.user, r.TopMsgID)
	if err != nil {
		return nil, err
	}
	a.Deliver(events)
	state, err = a.msgs.State(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesAffectedHistory{Pts: state.Pts}, nil
}

func (a *API) createForumTopic(ctx context.Context, r *tg.MessagesCreateForumTopicRequest) (any, error) {
	if isPrivateTopicPeer(r.Peer) {
		return a.createPrivateTopic(ctx, r)
	}
	return a.createChannelForumTopic(ctx, r)
}

func (a *API) editForumTopic(ctx context.Context, r *tg.MessagesEditForumTopicRequest) (any, error) {
	if isPrivateTopicPeer(r.Peer) {
		return a.editPrivateTopic(ctx, r)
	}
	return a.editChannelForumTopic(ctx, r)
}

func (a *API) getForumTopics(ctx context.Context, r *tg.MessagesGetForumTopicsRequest) (any, error) {
	if isPrivateTopicPeer(r.Peer) {
		return a.getPrivateTopics(ctx, r.Peer, nil, r.Q, r.OffsetTopic, r.Limit)
	}
	return a.getChannelForumTopics(ctx, r)
}

func (a *API) getForumTopicsByID(ctx context.Context, r *tg.MessagesGetForumTopicsByIDRequest) (any, error) {
	if isPrivateTopicPeer(r.Peer) {
		return a.getPrivateTopics(ctx, r.Peer, r.Topics, "", 0, 100)
	}
	return a.getChannelForumTopicsByID(ctx, r)
}

func (a *API) deleteTopicHistory(ctx context.Context, r *tg.MessagesDeleteTopicHistoryRequest) (any, error) {
	if isPrivateTopicPeer(r.Peer) {
		return a.deletePrivateTopic(ctx, r)
	}
	return a.deleteChannelTopicHistory(ctx, r)
}

func (a *API) getReplies(ctx context.Context, r *tg.MessagesGetRepliesRequest) (any, error) {
	if isPrivateTopicPeer(r.Peer) {
		return a.privateTopicReplies(ctx, r)
	}
	return a.getChannelReplies(ctx, r)
}
