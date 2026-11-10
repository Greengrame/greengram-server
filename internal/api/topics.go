package api

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
)

// Forum topics. Topic changes are service messages in the channel journal;
// pin changes have no pts and only go out live, like Telegram's.
func (a *API) registerTopics() {
	handle(a, tlprofile.SemanticMethodChannelsToggleForum, a.toggleForum)
	handle(a, tlprofile.SemanticMethodChannelsToggleViewForumAsMessages, a.toggleViewForumAsMessages)
	handle(a, tlprofile.SemanticMethodMessagesCreateForumTopic, a.createForumTopic)
	handle(a, tlprofile.SemanticMethodMessagesEditForumTopic, a.editForumTopic)
	handle(a, tlprofile.SemanticMethodMessagesGetForumTopics, a.getForumTopics)
	handle(a, tlprofile.SemanticMethodMessagesGetForumTopicsByID, a.getForumTopicsByID)
	handle(a, tlprofile.SemanticMethodMessagesUpdatePinnedForumTopic, a.updatePinnedForumTopic)
	handle(a, tlprofile.SemanticMethodMessagesReorderPinnedForumTopics, a.reorderPinnedForumTopics)
	handle(a, tlprofile.SemanticMethodMessagesDeleteTopicHistory, a.deleteTopicHistory)
	handle(a, tlprofile.SemanticMethodMessagesReadDiscussion, a.readDiscussion)
}

// forumPeer is the channel a topic call names.
func (a *API) forumPeer(ctx context.Context, me account.User, in tg.InputPeerClass) (int64, error) {
	p, err := a.peer(ctx, me, in)
	if err != nil || p.channel == 0 {
		return 0, errPeerInvalid
	}
	return p.channel, nil
}

func (a *API) toggleForum(ctx context.Context, r *tg.ChannelsToggleForumRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	events, err := a.chans.ToggleForum(ctx, me.ID, id, r.Enabled, r.Tabs)
	if err != nil {
		return nil, err
	}
	return a.opUpdates(ctx, me, id, events)
}

func (a *API) toggleViewForumAsMessages(ctx context.Context, r *tg.ChannelsToggleViewForumAsMessagesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	events, err := a.chans.SetForumAsMessages(ctx, me.ID, id, r.Enabled)
	if err != nil {
		return nil, err
	}
	return a.opUpdates(ctx, me, id, events)
}

func (a *API) createChannelForumTopic(ctx context.Context, r *tg.MessagesCreateForumTopicRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := a.forumPeer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	s, err := a.chans.CreateTopic(ctx, me.ID, id, channels.NewTopic{Title: r.Title, TitleMissing: r.TitleMissing,
		IconColor: r.IconColor, IconEmojiID: r.IconEmojiID, RandomID: r.RandomID})
	if err != nil {
		return nil, err
	}
	up, err := a.opUpdates(ctx, me, id, s.Events)
	if err != nil {
		return nil, err
	}
	// Web A takes the topic id from here
	up.Updates = append([]tg.UpdateClass{&tg.UpdateMessageID{ID: s.Msg.ID, RandomID: r.RandomID}}, up.Updates...)
	return up, nil
}

func (a *API) editChannelForumTopic(ctx context.Context, r *tg.MessagesEditForumTopicRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := a.forumPeer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	var e channels.TopicEdit
	if v, ok := r.GetTitle(); ok {
		e.Title = &v
	}
	if v, ok := r.GetIconEmojiID(); ok {
		e.IconEmojiID = &v
	}
	if v, ok := r.GetClosed(); ok {
		e.Closed = &v
	}
	if v, ok := r.GetHidden(); ok {
		e.Hidden = &v
	}
	events, err := a.chans.EditTopic(ctx, me.ID, id, r.TopicID, e)
	if err != nil {
		return nil, err
	}
	return a.opUpdates(ctx, me, id, events)
}

// forumTopics renders topics with their newest messages and who those name.
func (a *API) forumTopics(ctx context.Context, me account.User, c channels.Channel, list []tg.ForumTopicClass, topics []channels.Topic) (*tg.MessagesForumTopics, error) {
	var tops []int
	for _, t := range topics {
		if t.TopID != 0 {
			tops = append(tops, t.TopID)
		}
	}
	var msgs []channels.Msg
	if len(tops) > 0 {
		var err error
		if msgs, err = a.chans.Messages(ctx, c.ID, tops); err != nil {
			return nil, err
		}
	}
	body, users, chats, err := a.channelBody(ctx, me, c, msgs)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesForumTopics{Count: len(list), Topics: list, Messages: body, Chats: chats, Users: users, Pts: c.Pts}, nil
}

func (a *API) forum(ctx context.Context, me account.User, in tg.InputPeerClass) (channels.Channel, error) {
	id, err := a.forumPeer(ctx, me, in)
	if err != nil {
		return channels.Channel{}, err
	}
	c, _, err := a.chans.Access(ctx, id, me.ID)
	if err != nil {
		return c, err
	}
	if !c.Forum {
		return c, tgerr.New(400, "CHANNEL_FORUM_MISSING")
	}
	return c, nil
}

func (a *API) getChannelForumTopics(ctx context.Context, r *tg.MessagesGetForumTopicsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	c, err := a.forum(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	q, _ := r.GetQ()
	topics, total, err := a.chans.Topics(ctx, me.ID, c.ID, channels.TopicQuery{Q: q, OffsetTopic: r.OffsetTopic, Limit: r.Limit})
	if err != nil {
		return nil, err
	}
	list := make([]tg.ForumTopicClass, 0, len(topics))
	for _, t := range topics {
		list = append(list, t.TL(c.ID, me.ID))
	}
	out, err := a.forumTopics(ctx, me, c, list, topics)
	if err != nil {
		return nil, err
	}
	out.Count = total
	return out, nil
}

func (a *API) getChannelForumTopicsByID(ctx context.Context, r *tg.MessagesGetForumTopicsByIDRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	c, err := a.forum(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if len(r.Topics) == 0 {
		return nil, tgerr.New(400, "TOPICS_EMPTY")
	}
	if len(r.Topics) > 100 {
		return nil, tgerr.New(400, "LIMIT_INVALID")
	}
	topics, err := a.chans.TopicsByID(ctx, me.ID, c.ID, r.Topics)
	if err != nil {
		return nil, err
	}
	byID := map[int]channels.Topic{}
	for _, t := range topics {
		byID[t.ID] = t
	}
	// one answer per id asked, in that order
	list := make([]tg.ForumTopicClass, 0, len(r.Topics))
	for _, id := range r.Topics {
		if t, ok := byID[id]; ok {
			list = append(list, t.TL(c.ID, me.ID))
		} else {
			list = append(list, &tg.ForumTopicDeleted{ID: id})
		}
	}
	return a.forumTopics(ctx, me, c, list, topics)
}

func (a *API) updatePinnedForumTopic(ctx context.Context, r *tg.MessagesUpdatePinnedForumTopicRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := a.forumPeer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	events, err := a.chans.PinTopic(ctx, me.ID, id, r.TopicID, r.Pinned)
	if err != nil {
		return nil, err
	}
	return a.opUpdates(ctx, me, id, events)
}

func (a *API) reorderPinnedForumTopics(ctx context.Context, r *tg.MessagesReorderPinnedForumTopicsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := a.forumPeer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	events, err := a.chans.ReorderPinnedTopics(ctx, me.ID, id, r.Order, r.Force)
	if err != nil {
		return nil, err
	}
	return a.opUpdates(ctx, me, id, events)
}

func (a *API) deleteChannelTopicHistory(ctx context.Context, r *tg.MessagesDeleteTopicHistoryRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := a.forumPeer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	res, events, err := a.chans.DeleteTopic(ctx, me.ID, id, r.TopMsgID)
	if err != nil {
		return nil, err
	}
	a.deliverChannel(events, except(ctx))
	return &res, nil
}

// readDiscussion reads a forum topic. Comment threads have no read marks of
// their own here, so on any other chat it's a no-op.
func (a *API) readDiscussion(ctx context.Context, r *tg.MessagesReadDiscussionRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := a.forumPeer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	c, _, err := a.chans.Access(ctx, id, me.ID)
	if err != nil {
		return nil, err
	}
	if !c.Forum {
		return true, nil
	}
	res, err := a.chans.ReadTopic(ctx, me.ID, id, r.MsgID, r.ReadMaxID)
	if tgerr.Is(err, "TOPIC_ID_INVALID") {
		return true, nil // a reply thread inside a forum
	}
	if err != nil {
		return nil, err
	}
	if res.Inbox != nil {
		a.echo(ctx, me, []tg.UpdateClass{res.Inbox}, []tg.UserClass{})
	}
	a.deliverChannel(res.Events, except(ctx))
	return true, nil
}
