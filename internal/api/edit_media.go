package api

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/messages"
)

// editMedia is editMessage with media: stopping a poll, moving or
// stopping a live location, or swapping a photo or file (with the
// caption, when the request has one).
func (a *API) editMedia(ctx context.Context, me account.User, to peer, r *tg.MessagesEditMessageRequest, in tg.InputMediaClass) (any, error) {
	if p, ok := in.(*tg.InputMediaPoll); ok {
		return a.closePoll(ctx, me, to, r.ID, p)
	}
	var text *string
	if t, ok := r.GetMessage(); ok {
		text = &t
	}
	var old media.Ref
	var sent int
	if to.channel != 0 {
		m, err := a.chans.CanEdit(ctx, me.ID, to.channel, r.ID)
		if err != nil {
			return nil, err
		}
		old, sent = m.Media, m.Date
	} else {
		list, err := a.msgs.Get(ctx, me.ID, []int{r.ID})
		if err != nil {
			return nil, err
		}
		if len(list) == 0 || list[0].Peer != to.user {
			return nil, errMsgInvalid
		}
		old, sent = list[0].Media, list[0].Date
	}
	var ref media.Ref
	var err error
	switch v := in.(type) {
	case *tg.InputMediaGeoLive:
		live, ok := old.TL.(*tg.MessageMediaGeoLive)
		if !ok {
			return nil, tgerr.New(400, "MEDIA_PREV_INVALID")
		}
		if ref, err = media.MoveLive(live, sent, now(), v); err != nil {
			return nil, err
		}
	case *tg.InputMediaTodo:
		prev, ok := old.TL.(*tg.MessageMediaToDo)
		if !ok {
			return nil, tgerr.New(400, "MEDIA_PREV_INVALID")
		}
		if ref, err = media.NewTodo(v.Todo, prev); err != nil {
			return nil, err
		}
	case *tg.InputMediaDice, *tg.InputMediaContact, *tg.InputMediaGeoPoint, *tg.InputMediaVenue:
		// Telegram doesn't let these change once sent
		return nil, tgerr.New(400, "MEDIA_INVALID")
	default:
		// only something with media can get other media
		switch old.Kind {
		case media.KindPoll, media.KindGeo, media.KindContact, media.KindDice, media.KindTodo, "":
			return nil, tgerr.New(400, "MEDIA_PREV_INVALID")
		}
		if err := a.mayWrite(ctx, me, to, true); err != nil {
			return nil, err
		}
		if ref, err = a.mediaFromInput(ctx, me, in); err != nil {
			return nil, err
		}
	}
	if to.channel != 0 {
		_, u, events, err := a.chans.EditMedia(ctx, me.ID, to.channel, r.ID, ref, text, r.Entities)
		if err != nil {
			return nil, err
		}
		up, err := a.opUpdates(ctx, me, to.channel, events)
		if err != nil {
			return nil, err
		}
		if len(up.Updates) == 0 {
			up.Updates = []tg.UpdateClass{channels.SetOut(u, me.ID)}
		}
		return up, nil
	}
	m, u, events, err := a.msgs.EditMedia(ctx, me.ID, to.user, r.ID, ref, text, r.Entities)
	if err != nil {
		return nil, err
	}
	users, err := a.usersFor(ctx, me.ID, messages.Users([]messages.Msg{m}))
	if err != nil {
		return nil, err
	}
	a.echo(ctx, me, []tg.UpdateClass{u}, users)
	a.deliver(events, except(ctx))
	return &tg.Updates{Updates: []tg.UpdateClass{u}, Users: users, Chats: []tg.ChatClass{}, Date: now()}, nil
}
