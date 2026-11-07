package api

import (
	"context"
	"log/slog"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/media"
)

func (a *API) registerPolls() {
	handle(a, tlprofile.SemanticMethodMessagesSendVote, a.sendVote)
	handle(a, tlprofile.SemanticMethodMessagesGetPollResults, a.getPollResults)
	handle(a, tlprofile.SemanticMethodMessagesGetPollVotes, a.getPollVotes)
	handle(a, tlprofile.SemanticMethodMessagesAddPollAnswer, a.addPollAnswer)
	handle(a, tlprofile.SemanticMethodMessagesDeletePollAnswer, a.deletePollAnswer)
	// votes on your own polls aren't counted as unread here
	handle(a, tlprofile.SemanticMethodMessagesGetUnreadPollVotes, func(ctx context.Context, r *tg.MessagesGetUnreadPollVotesRequest) (any, error) {
		if _, err := a.me(ctx); err != nil {
			return nil, err
		}
		return &tg.MessagesMessages{Messages: []tg.MessageClass{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodMessagesReadPollVotes, func(ctx context.Context, r *tg.MessagesReadPollVotesRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		st, err := a.msgs.State(ctx, me.ID)
		if err != nil {
			return nil, err
		}
		return &tg.MessagesAffectedHistory{Pts: st.Pts}, nil
	})
}

var errMsgInvalid = tgerr.New(400, "MESSAGE_ID_INVALID")

// deliverTimeout bounds a detached fan-out.
const deliverTimeout = 30 * time.Second

// pollOf finds the poll a message shows, checking the caller can see it.
func (a *API) pollOf(ctx context.Context, me account.User, p tg.InputPeerClass, id int) (peer, media.Poll, error) {
	to, err := a.peer(ctx, me, p)
	if err != nil {
		return to, media.Poll{}, err
	}
	var ref media.Ref
	if to.channel != 0 {
		if _, _, err := a.chans.Access(ctx, to.channel, me.ID); err != nil {
			return to, media.Poll{}, err
		}
		list, err := a.chans.Messages(ctx, to.channel, []int{id})
		if err != nil {
			return to, media.Poll{}, err
		}
		if len(list) == 1 {
			ref = list[0].Media
		}
	} else {
		list, err := a.msgs.Get(ctx, me.ID, []int{id})
		if err != nil {
			return to, media.Poll{}, err
		}
		if len(list) == 1 && list[0].Peer == to.user {
			ref = list[0].Media
		}
	}
	if ref.Kind != media.KindPoll {
		return to, media.Poll{}, errMsgInvalid
	}
	poll, ok, err := a.media.Poll(ctx, ref.ID)
	if err != nil {
		return to, media.Poll{}, err
	}
	if !ok {
		return to, media.Poll{}, errMsgInvalid
	}
	return to, poll, nil
}

func pollPeer(to peer) tg.PeerClass {
	if to.channel != 0 {
		return &tg.PeerChannel{ChannelID: to.channel}
	}
	return &tg.PeerUser{UserID: clientID(to.user)}
}

// pollUpdate is the poll as viewer sees it now, with the users it names.
func pollUpdate(p media.Poll, t media.Tally, viewer int64, at tg.PeerClass, msg int) (*tg.UpdateMessagePoll, []int64) {
	u := &tg.UpdateMessagePoll{PollID: p.ID, Results: p.Results(t, viewer, true)}
	u.SetPoll(p.PollTL(viewer))
	if at != nil {
		u.SetPeer(at)
		u.SetMsgID(msg)
	}
	return u, p.Voters(t)
}

// pollReply answers the caller with the poll's state as they see it and
// tells everyone else who has the poll in a chat.
func (a *API) pollReply(ctx context.Context, me account.User, to peer, msg int, pollID int64) (*tg.Updates, error) {
	p, ok, err := a.media.Poll(ctx, pollID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errMsgInvalid
	}
	ts, err := a.media.Tallies(ctx, me.ID, []int64{pollID})
	if err != nil {
		return nil, err
	}
	u, voters := pollUpdate(p, ts[pollID], me.ID, pollPeer(to), msg)
	out := &tg.Updates{Updates: []tg.UpdateClass{u}, Date: now()}
	if out.Users, err = a.usersFor(ctx, me.ID, voters); err != nil {
		return nil, err
	}
	var chans []int64
	if to.channel != 0 {
		chans = []int64{to.channel}
	}
	if out.Chats, err = a.chatsFor(ctx, me.ID, chans); err != nil {
		return nil, err
	}
	a.pollChanged(pollID, except(ctx))
	return out, nil
}

// pollChanged pushes a poll's new state to every chat it's in, each
// viewer with their own votes marked. Not journaled, like in Telegram:
// a client that missed it sees the new count when it loads the message.
func (a *API) pollChanged(pollID int64, skip edge.Session) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), deliverTimeout)
		defer cancel()
		p, ok, err := a.media.Poll(ctx, pollID)
		if err != nil || !ok {
			return
		}
		copies, err := a.media.PollCopies(ctx, pollID)
		if err != nil {
			slog.Warn("api: poll copies", "err", err)
			return
		}
		ts, err := a.media.Tallies(ctx, 0, []int64{pollID})
		if err != nil {
			slog.Warn("api: poll tally", "err", err)
			return
		}
		base := ts[pollID]
		type target struct {
			at  tg.PeerClass
			msg int
			ch  int64
		}
		byUser := map[int64][]target{}
		for _, c := range copies {
			if c.Channel == 0 {
				byUser[c.Owner] = append(byUser[c.Owner], target{at: &tg.PeerUser{UserID: clientID(c.Peer)}, msg: c.ID})
				continue
			}
			members, err := a.chans.MemberIDs(ctx, c.Channel)
			if err != nil {
				slog.Warn("api: poll members", "err", err)
				continue
			}
			for _, m := range members {
				byUser[m] = append(byUser[m], target{at: &tg.PeerChannel{ChannelID: c.Channel}, msg: c.ID, ch: c.Channel})
			}
		}
		for viewer, list := range byUser {
			if !a.hub.online(viewer) {
				continue
			}
			a.out.add(viewer, func(ctx context.Context) {
				t, err := a.media.ForViewer(ctx, base, pollID, viewer)
				if err != nil {
					slog.Warn("api: poll viewer", "err", err)
					return
				}
				up := &tg.Updates{Date: now()}
				var users, chans []int64
				for _, x := range list {
					u, voters := pollUpdate(p, t, viewer, x.at, x.msg)
					up.Updates = append(up.Updates, u)
					users = append(users, voters...)
					if x.ch != 0 {
						chans = append(chans, x.ch)
					}
				}
				if up.Users, err = a.usersFor(ctx, viewer, users); err != nil {
					return
				}
				if up.Chats, err = a.chatsFor(ctx, viewer, chans); err != nil {
					return
				}
				a.push(ctx, viewer, up, skip)
			})
		}
	}()
}

func (a *API) sendVote(ctx context.Context, r *tg.MessagesSendVoteRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, p, err := a.pollOf(ctx, me, r.Peer, r.MsgID)
	if err != nil {
		return nil, err
	}
	if to.channel != 0 {
		// voting is writing: a member who may not post still votes, but
		// someone who only peeks into a public channel doesn't
		if _, m, err := a.chans.Access(ctx, to.channel, me.ID); err != nil || !channels.InChat(m.Status) {
			return nil, tgerr.New(403, "CHANNEL_PRIVATE")
		}
	}
	if err := a.media.Vote(ctx, me.ID, p.ID, r.Options); err != nil {
		return nil, err
	}
	return a.pollReply(ctx, me, to, r.MsgID, p.ID)
}

func (a *API) getPollResults(ctx context.Context, r *tg.MessagesGetPollResultsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, p, err := a.pollOf(ctx, me, r.Peer, r.MsgID)
	if err != nil {
		return nil, err
	}
	ts, err := a.media.Tallies(ctx, me.ID, []int64{p.ID})
	if err != nil {
		return nil, err
	}
	u, voters := pollUpdate(p, ts[p.ID], me.ID, pollPeer(to), r.MsgID)
	out := &tg.Updates{Updates: []tg.UpdateClass{u}, Date: now()}
	if out.Users, err = a.usersFor(ctx, me.ID, voters); err != nil {
		return nil, err
	}
	var chans []int64
	if to.channel != 0 {
		chans = []int64{to.channel}
	}
	out.Chats, err = a.chatsFor(ctx, me.ID, chans)
	return out, err
}

func (a *API) getPollVotes(ctx context.Context, r *tg.MessagesGetPollVotesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, p, err := a.pollOf(ctx, me, r.Peer, r.ID)
	if err != nil {
		return nil, err
	}
	if to.channel != 0 {
		c, _, err := a.chans.Access(ctx, to.channel, me.ID)
		if err != nil {
			return nil, err
		}
		// a broadcast channel's voters stay anonymous to its readers
		if !c.Megagroup {
			return nil, tgerr.New(403, "BROADCAST_FORBIDDEN")
		}
	}
	option, filtered := r.GetOption()
	offset, _ := r.GetOffset()
	votes, count, next, err := a.media.PollVotes(ctx, p.ID, option, offset, r.Limit)
	if err != nil {
		return nil, err
	}
	out := &tg.MessagesVotesList{Count: count, Votes: []tg.MessagePeerVoteClass{}}
	var users []int64
	for _, v := range votes {
		at := &tg.PeerUser{UserID: clientID(v.User)}
		users = append(users, v.User)
		switch {
		case filtered:
			out.Votes = append(out.Votes, &tg.MessagePeerVoteInputOption{Peer: at, Date: v.Date})
		case len(v.Options) == 1:
			out.Votes = append(out.Votes, &tg.MessagePeerVote{Peer: at, Option: v.Options[0], Date: v.Date})
		default:
			out.Votes = append(out.Votes, &tg.MessagePeerVoteMultiple{Peer: at, Options: v.Options, Date: v.Date})
		}
	}
	if next != "" {
		out.SetNextOffset(next)
	}
	if out.Users, err = a.usersFor(ctx, me.ID, users); err != nil {
		return nil, err
	}
	out.Chats = []tg.ChatClass{}
	return out, nil
}

func (a *API) addPollAnswer(ctx context.Context, r *tg.MessagesAddPollAnswerRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, p, err := a.pollOf(ctx, me, r.Peer, r.MsgID)
	if err != nil {
		return nil, err
	}
	var text tg.TextWithEntities
	switch v := r.Answer.(type) {
	case *tg.InputPollAnswer:
		text = v.Text
	case *tg.PollAnswer:
		text = v.Text
	default:
		return nil, tgerr.New(400, "POLL_ANSWER_INVALID")
	}
	if err := a.media.AddPollAnswer(ctx, me.ID, p.ID, text); err != nil {
		return nil, err
	}
	return a.pollReply(ctx, me, to, r.MsgID, p.ID)
}

func (a *API) deletePollAnswer(ctx context.Context, r *tg.MessagesDeletePollAnswerRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, p, err := a.pollOf(ctx, me, r.Peer, r.MsgID)
	if err != nil {
		return nil, err
	}
	if err := a.media.DeletePollAnswer(ctx, me.ID, p.ID, r.Option); err != nil {
		return nil, err
	}
	return a.pollReply(ctx, me, to, r.MsgID, p.ID)
}

// closePoll is editMessage with the poll marked closed: the sender, or a
// channel admin who may edit the post, stops it.
func (a *API) closePoll(ctx context.Context, me account.User, to peer, id int, in *tg.InputMediaPoll) (*tg.Updates, error) {
	var ref media.Ref
	if to.channel != 0 {
		m, err := a.chans.CanEdit(ctx, me.ID, to.channel, id)
		if err != nil {
			return nil, err
		}
		ref = m.Media
	} else {
		list, err := a.msgs.Get(ctx, me.ID, []int{id})
		if err != nil {
			return nil, err
		}
		if len(list) == 0 || list[0].Peer != to.user {
			return nil, errMsgInvalid
		}
		if !list[0].Out || !list[0].Fwd.Empty() {
			return nil, tgerr.New(403, "MESSAGE_AUTHOR_REQUIRED")
		}
		ref = list[0].Media
	}
	if ref.Kind != media.KindPoll {
		return nil, tgerr.New(400, "MEDIA_INVALID")
	}
	if !in.Poll.Closed {
		return nil, tgerr.New(400, "MESSAGE_NOT_MODIFIED")
	}
	if _, err := a.media.ClosePoll(ctx, ref.ID); err != nil {
		return nil, err
	}
	return a.pollReply(ctx, me, to, id, ref.ID)
}

// livePolls puts the current votes, as viewer sees them, into the polls
// of messages rendered from the stored copy, and returns the voters they
// name. The media is replaced, not changed: other renders may hold it.
func (a *API) livePolls(ctx context.Context, viewer int64, list []tg.MessageClass) []int64 {
	if a.media == nil {
		return nil
	}
	var want []int64
	for _, m := range list {
		if msg, ok := m.(*tg.Message); ok {
			if mp, ok := msg.Media.(*tg.MessageMediaPoll); ok {
				want = append(want, mp.Poll.ID)
			}
		}
	}
	if len(want) == 0 {
		return nil
	}
	polls, err := a.media.Polls(ctx, want)
	if err != nil {
		slog.Warn("api: polls", "err", err)
		return nil
	}
	ts, err := a.media.Tallies(ctx, viewer, want)
	if err != nil {
		slog.Warn("api: poll tallies", "err", err)
		return nil
	}
	var voters []int64
	for _, m := range list {
		msg, ok := m.(*tg.Message)
		if !ok {
			continue
		}
		mp, ok := msg.Media.(*tg.MessageMediaPoll)
		if !ok {
			continue
		}
		p, ok := polls[mp.Poll.ID]
		if !ok {
			continue
		}
		t := ts[p.ID]
		msg.Media = p.Media(t, viewer)
		voters = append(voters, p.Voters(t)...)
	}
	return voters
}

// ownPolls gives the sender of new messages their own view of the polls
// in them (stored copies are nobody's, so a recipient can't stop it).
// The updates are copied first: channel members get the same objects.
func (a *API) ownPolls(ctx context.Context, viewer int64, list []tg.UpdateClass) []int64 {
	var msgs []tg.MessageClass
	for i, u := range list {
		m, ok := updateMessage(u).(*tg.Message)
		if !ok {
			continue
		}
		if _, ok := m.Media.(*tg.MessageMediaPoll); !ok {
			continue
		}
		c := *m
		switch v := u.(type) {
		case *tg.UpdateNewMessage:
			cu := *v
			cu.Message = &c
			list[i] = &cu
		case *tg.UpdateNewChannelMessage:
			cu := *v
			cu.Message = &c
			list[i] = &cu
		default:
			continue
		}
		msgs = append(msgs, &c)
	}
	return a.livePolls(ctx, viewer, msgs)
}

// closeDuePolls closes the polls whose time ran out and tells their chats.
func (a *API) closeDuePolls(ctx context.Context) {
	if a.media == nil {
		return
	}
	list, err := a.media.CloseDuePolls(ctx)
	if err != nil {
		slog.Warn("api: close polls", "err", err)
		return
	}
	for _, id := range list {
		a.pollChanged(id, edge.Session{})
	}
}
