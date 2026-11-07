package api

import (
	"context"
	"log/slog"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/messages"
)

func (a *API) registerScheduled() {
	handle(a, tlprofile.SemanticMethodMessagesGetScheduledHistory, a.getScheduledHistory)
	handle(a, tlprofile.SemanticMethodMessagesGetScheduledMessages, a.getScheduledMessages)
	handle(a, tlprofile.SemanticMethodMessagesSendScheduledMessages, a.sendScheduledMessages)
	handle(a, tlprofile.SemanticMethodMessagesDeleteScheduledMessages, a.deleteScheduledMessages)
}

// sendOrSchedule is sendTo for requests with a schedule_date. A date that
// already passed, or "when online", goes out now: there is no online
// wait here.
func (a *API) sendOrSchedule(ctx context.Context, me account.User, to peer, list []outgoing, date, repeat int) (*tg.Updates, error) {
	if date <= now() || date == messages.ScheduleWhenOnline {
		return a.sendTo(ctx, me, to, list)
	}
	return a.schedule(ctx, me, to, list, date, repeat)
}

// Telegram's repeat choices: daily, weekly, two weeks, monthly, 3, 6
// months and yearly; the test server's 1 and 5 minutes help testing.
func repeatValid(p int) bool {
	switch p {
	case 0, 60, 300, 86400, 7 * 86400, 14 * 86400, 30 * 86400, 91 * 86400, 182 * 86400, 365 * 86400:
		return true
	}
	return false
}

// isPost says whether messages to a channel are its posts (a broadcast
// channel) rather than the sender's own.
func (a *API) isPost(ctx context.Context, channel int64) bool {
	c, ok, err := a.chans.Get(ctx, channel)
	return err == nil && ok && !c.Megagroup
}

func (a *API) schedule(ctx context.Context, me account.User, to peer, list []outgoing, date, repeat int) (*tg.Updates, error) {
	if !repeatValid(repeat) {
		return nil, tgerr.New(400, "SCHEDULE_REPEAT_PERIOD_INVALID")
	}
	withMedia := false
	for _, o := range list {
		withMedia = withMedia || !o.media.Empty()
	}
	if err := a.mayWrite(ctx, me, to, withMedia); err != nil {
		return nil, err
	}
	post := to.channel != 0 && a.isPost(ctx, to.channel)
	out := &tg.Updates{Updates: []tg.UpdateClass{}, Date: now()}
	var echo []tg.UpdateClass
	for _, o := range list {
		sc, err := a.msgs.Schedule(ctx, messages.Scheduled{Owner: me.ID, Peer: to.user, Channel: to.channel, Date: date, Repeat: repeat,
			Text: o.text, Entities: o.entities, ReplyTo: o.replyTo, Media: o.media, Grouped: o.grouped, RandomID: o.randomID, Fwd: o.fwd})
		if err != nil {
			return nil, err
		}
		out.Updates = append(out.Updates, &tg.UpdateMessageID{ID: sc.ID, RandomID: o.randomID},
			&tg.UpdateNewScheduledMessage{Message: sc.TL(post)})
		echo = append(echo, &tg.UpdateNewScheduledMessage{Message: sc.TL(post)})
	}
	if err := a.scheduledBody(ctx, me, to, out); err != nil {
		return nil, err
	}
	a.echoChats(ctx, me, echo, out.Users, out.Chats)
	return out, nil
}

// scheduledBody fills the users and chats an answer about scheduled
// messages needs, and the live state of their polls.
func (a *API) scheduledBody(ctx context.Context, me account.User, to peer, up *tg.Updates) error {
	var msgs []tg.MessageClass
	for _, u := range up.Updates {
		if v, ok := u.(*tg.UpdateNewScheduledMessage); ok {
			msgs = append(msgs, v.Message)
		}
	}
	users, chans := mentioned(msgs...)
	users = append(users, me.ID)
	users = append(users, a.livePolls(ctx, me.ID, msgs)...)
	if to.channel != 0 {
		chans = append(chans, to.channel)
	}
	var err error
	if up.Users, err = a.usersFor(ctx, me.ID, users); err != nil {
		return err
	}
	up.Chats, err = a.chatsFor(ctx, me.ID, chans)
	return err
}

// scheduledPeer resolves the chat of a scheduled-messages call; a channel
// is only for those who may post there.
func (a *API) scheduledPeer(ctx context.Context, me account.User, p tg.InputPeerClass) (peer, error) {
	to, err := a.peer(ctx, me, p)
	if err != nil {
		return to, err
	}
	if to.channel != 0 {
		if _, _, err := a.chans.Access(ctx, to.channel, me.ID); err != nil {
			return to, err
		}
	}
	return to, nil
}

func (a *API) scheduledList(ctx context.Context, me account.User, to peer, list []messages.Scheduled) (*tg.MessagesMessages, error) {
	post := to.channel != 0 && a.isPost(ctx, to.channel)
	up := &tg.Updates{}
	out := &tg.MessagesMessages{Messages: []tg.MessageClass{}}
	// newest first, like history
	for i := len(list) - 1; i >= 0; i-- {
		m := list[i].TL(post)
		out.Messages = append(out.Messages, m)
		up.Updates = append(up.Updates, &tg.UpdateNewScheduledMessage{Message: m})
	}
	if err := a.scheduledBody(ctx, me, to, up); err != nil {
		return nil, err
	}
	out.Users, out.Chats = up.Users, up.Chats
	return out, nil
}

func scheduledHash(list []messages.Scheduled) int64 {
	vals := make([]int64, 0, 2*len(list))
	for _, m := range list {
		vals = append(vals, int64(m.ID), int64(max(m.EditDate, m.Date)))
	}
	return media.ListHash(vals)
}

func (a *API) getScheduledHistory(ctx context.Context, r *tg.MessagesGetScheduledHistoryRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.scheduledPeer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	list, err := a.msgs.ScheduledIn(ctx, me.ID, to.user, to.channel, nil)
	if err != nil {
		return nil, err
	}
	if r.Hash != 0 && r.Hash == scheduledHash(list) {
		return &tg.MessagesMessagesNotModified{Count: len(list)}, nil
	}
	return a.scheduledList(ctx, me, to, list)
}

func (a *API) getScheduledMessages(ctx context.Context, r *tg.MessagesGetScheduledMessagesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.scheduledPeer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	list, err := a.msgs.ScheduledIn(ctx, me.ID, to.user, to.channel, append([]int{}, r.ID...))
	if err != nil {
		return nil, err
	}
	return a.scheduledList(ctx, me, to, list)
}

func scheduledGone(to peer, list, sent []int) *tg.UpdateDeleteScheduledMessages {
	u := &tg.UpdateDeleteScheduledMessages{Peer: pollPeer(to), Messages: list}
	if len(sent) > 0 {
		u.SetSentMessages(sent)
	}
	return u
}

func (a *API) deleteScheduledMessages(ctx context.Context, r *tg.MessagesDeleteScheduledMessagesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	gone, err := a.msgs.DeleteScheduled(ctx, me.ID, to.user, to.channel, r.ID)
	if err != nil {
		return nil, err
	}
	out := emptyUpdates()
	if len(gone) == 0 {
		return out, nil
	}
	out.Updates = []tg.UpdateClass{scheduledGone(to, gone, nil)}
	a.echoChats(ctx, me, []tg.UpdateClass{scheduledGone(to, gone, nil)}, []tg.UserClass{}, []tg.ChatClass{})
	return out, nil
}

func (a *API) sendScheduledMessages(ctx context.Context, r *tg.MessagesSendScheduledMessagesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	list, err := a.msgs.ClaimNow(ctx, me.ID, to.user, to.channel, r.ID)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, errMsgInvalid
	}
	out := emptyUpdates()
	for _, sc := range list {
		up, err := a.postScheduled(ctx, me, sc)
		if err != nil {
			return nil, err
		}
		out.Updates = append(out.Updates, up.Updates...)
		out.Users = append(out.Users, up.Users...)
		out.Chats = append(out.Chats, up.Chats...)
	}
	return out, nil
}

// postScheduled sends one claimed message as its owner and takes it off
// the queue (or moves a repeating one to its next date). The answer is
// what the sending call would have got, without the UpdateMessageID; the
// owner's other sessions hear the same from the pushes.
func (a *API) postScheduled(ctx context.Context, me account.User, sc messages.Scheduled) (*tg.Updates, error) {
	to := peer{user: sc.Peer, channel: sc.Channel}
	o := outgoing{text: sc.Text, entities: sc.Entities, replyTo: sc.ReplyTo, fwd: sc.Fwd, media: sc.Media, grouped: sc.Grouped}
	sent, err := a.sendTo(ctx, me, to, []outgoing{o})
	if err != nil {
		// can't go any more (left the group, blocked): drop it like a
		// deleted one, or it would fail every second
		if _, derr := a.msgs.SentScheduled(ctx, messages.Scheduled{ID: sc.ID}, 0); derr != nil {
			return nil, derr
		}
		a.echoChats(ctx, me, []tg.UpdateClass{scheduledGone(to, []int{sc.ID}, nil)}, []tg.UserClass{}, []tg.ChatClass{})
		return nil, err
	}
	var ids []int
	out := &tg.Updates{Users: sent.Users, Chats: sent.Chats, Date: sent.Date}
	for _, u := range sent.Updates {
		if v, ok := u.(*tg.UpdateMessageID); ok {
			ids = append(ids, v.ID)
			continue
		}
		out.Updates = append(out.Updates, u)
	}
	next, err := a.msgs.SentScheduled(ctx, sc, now())
	if err != nil {
		return nil, err
	}
	var after tg.UpdateClass
	if next != 0 {
		sc.Date, sc.EditDate = next, 0
		post := to.channel != 0 && a.isPost(ctx, to.channel)
		after = &tg.UpdateNewScheduledMessage{Message: sc.TL(post)}
		out.Updates = append(out.Updates, &tg.UpdateNewScheduledMessage{Message: sc.TL(post)})
	} else {
		after = scheduledGone(to, []int{sc.ID}, ids)
		out.Updates = append(out.Updates, scheduledGone(to, []int{sc.ID}, ids))
	}
	// queued behind the new message's own push to the owner
	a.echoChats(ctx, me, []tg.UpdateClass{after}, []tg.UserClass{}, []tg.ChatClass{})
	return out, nil
}

// sendDue posts what's due. Called every second by Tick.
func (a *API) sendDue(ctx context.Context) {
	for {
		list, err := a.msgs.ClaimDue(ctx, 50)
		if err != nil {
			slog.Warn("api: scheduled claim", "err", err)
			return
		}
		if len(list) == 0 {
			return
		}
		owners := make([]int64, 0, len(list))
		for _, sc := range list {
			owners = append(owners, sc.Owner)
		}
		users, err := a.accounts.Users(ctx, owners)
		if err != nil {
			slog.Warn("api: scheduled owners", "err", err)
			return
		}
		for _, sc := range list {
			u, ok := users[sc.Owner]
			// a frozen or closed account's queue waits; they may come back
			if !ok || u.Disabled || u.State != "" && u.State != "active" {
				continue
			}
			if _, err := a.postScheduled(ctx, u, sc); err != nil {
				slog.Info("api: scheduled message dropped", "owner", sc.Owner, "err", err)
			}
		}
		if len(list) < 50 {
			return
		}
	}
}

// Tick runs the clock-driven jobs: scheduled messages going out, polls
// closing on time and giveaways drawn. It returns when ctx ends.
func (a *API) Tick(ctx context.Context) {
	go a.drawLoop(ctx)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.sendDue(ctx)
		a.closeDuePolls(ctx)
	}
}

// editScheduled is editMessage on a queued message: new text, media or date.
func (a *API) editScheduled(ctx context.Context, me account.User, to peer, r *tg.MessagesEditMessageRequest, date int) (*tg.Updates, error) {
	var text *string
	if t, ok := r.GetMessage(); ok {
		text = &t
	}
	var ref *media.Ref
	if in, ok := r.GetMedia(); ok {
		if err := a.mayWrite(ctx, me, to, true); err != nil {
			return nil, err
		}
		m, err := a.media.FromInput(ctx, me.ID, in)
		if err != nil {
			return nil, err
		}
		ref = &m
	}
	if date <= now() {
		date = 0
	}
	sc, err := a.msgs.EditScheduled(ctx, me.ID, to.user, to.channel, r.ID, text, r.Entities, ref, date)
	if err != nil {
		return nil, err
	}
	post := to.channel != 0 && a.isPost(ctx, to.channel)
	out := &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewScheduledMessage{Message: sc.TL(post)}}, Date: now()}
	if err := a.scheduledBody(ctx, me, to, out); err != nil {
		return nil, err
	}
	a.echoChats(ctx, me, []tg.UpdateClass{&tg.UpdateNewScheduledMessage{Message: sc.TL(post)}}, out.Users, out.Chats)
	return out, nil
}

