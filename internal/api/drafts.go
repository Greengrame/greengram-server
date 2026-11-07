package api

import (
	"context"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/messages"
)

// Drafts follow the user from app to app: every save goes to their other
// sessions as updateDraftMessage (no pts, like Telegram).
func (a *API) registerDrafts() {
	handle(a, tlprofile.SemanticMethodMessagesSaveDraft, a.saveDraft)
	handle(a, tlprofile.SemanticMethodMessagesGetAllDrafts, a.allDrafts)
	handle(a, tlprofile.SemanticMethodMessagesClearAllDrafts, a.clearDrafts)
}

func draftKey(to peer, reply tg.InputReplyToClass) messages.DraftKey {
	k := messages.DraftKey{User: to.user, Channel: to.channel}
	if r, ok := reply.(*tg.InputReplyToMessage); ok {
		k.Thread = r.TopMsgID
	}
	if k.Channel != 0 {
		k.User = 0
	}
	return k
}

func (k peer) tl() tg.PeerClass {
	if k.channel != 0 {
		return &tg.PeerChannel{ChannelID: k.channel}
	}
	return &tg.PeerUser{UserID: clientID(k.user)}
}

func draftUpdate(k messages.DraftKey, d tg.DraftMessageClass) *tg.UpdateDraftMessage {
	u := &tg.UpdateDraftMessage{Peer: peer{user: k.User, channel: k.Channel}.tl(), Draft: d}
	if k.Thread != 0 {
		u.SetTopMsgID(k.Thread)
	}
	return u
}

// tellDraft sends a draft change to the user's other sessions; each push
// gets its own object.
func (a *API) tellDraft(ctx context.Context, me account.User, k messages.DraftKey, d tg.DraftMessageClass) {
	a.deliver([]messages.Event{{UserID: me.ID, Update: cloneUpdate(draftUpdate(k, d))}}, except(ctx))
}

func (a *API) saveDraft(ctx context.Context, r *tg.MessagesSaveDraftRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if to.channel != 0 {
		if _, _, err := a.chans.Access(ctx, to.channel, me.ID); err != nil {
			return nil, err
		}
	}
	if utf8.RuneCountInString(r.Message) > messages.MaxLength {
		return nil, tgerr.New(400, "MESSAGE_TOO_LONG")
	}
	k := draftKey(to, r.ReplyTo)
	if r.Message == "" && replyID(r.ReplyTo) == 0 && r.Media == nil {
		dropped, err := a.msgs.DropDraft(ctx, me.ID, k)
		if err != nil {
			return nil, err
		}
		if dropped {
			a.tellDraft(ctx, me, k, &tg.DraftMessageEmpty{Date: now()})
		}
		return true, nil
	}
	d := tg.DraftMessage{Message: r.Message, Date: now(), NoWebpage: r.NoWebpage, InvertMedia: r.InvertMedia}
	if ents, _ := a.mentionsIn(ctx, me, r.Message, r.Entities); len(ents) > 0 {
		d.SetEntities(ents)
	}
	if r.ReplyTo != nil {
		d.SetReplyTo(r.ReplyTo)
	}
	if r.Media != nil {
		d.SetMedia(r.Media)
	}
	if r.Effect != 0 {
		d.SetEffect(r.Effect)
	}
	if err := a.msgs.SaveDraft(ctx, me.ID, k, d); err != nil {
		return nil, err
	}
	a.tellDraft(ctx, me, k, &d)
	return true, nil
}

// dropSentDraft is clear_draft on a send: the draft goes, and the other
// apps hear it went.
func (a *API) dropSentDraft(ctx context.Context, me account.User, to peer, reply tg.InputReplyToClass) {
	k := draftKey(to, reply)
	if dropped, err := a.msgs.DropDraft(ctx, me.ID, k); err == nil && dropped {
		a.tellDraft(ctx, me, k, &tg.DraftMessageEmpty{Date: now()})
	}
}

func (a *API) draftsUpdates(ctx context.Context, me account.User, list []messages.SavedDraft, empty bool) (*tg.Updates, error) {
	out := &tg.Updates{Updates: []tg.UpdateClass{}, Date: now()}
	var users, chans []int64
	for i := range list {
		d := list[i]
		var body tg.DraftMessageClass = &d.Draft
		if empty {
			body = &tg.DraftMessageEmpty{Date: now()}
		}
		out.Updates = append(out.Updates, draftUpdate(d.DraftKey, body))
		if d.Channel != 0 {
			chans = append(chans, d.Channel)
		} else {
			users = append(users, d.User)
		}
	}
	var err error
	if out.Users, err = a.usersFor(ctx, me.ID, users); err != nil {
		return nil, err
	}
	out.Chats, err = a.chatsFor(ctx, me.ID, chans)
	return out, err
}

func (a *API) allDrafts(ctx context.Context, _ *tg.MessagesGetAllDraftsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	list, err := a.msgs.Drafts(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	return a.draftsUpdates(ctx, me, list, false)
}

func (a *API) clearDrafts(ctx context.Context, _ *tg.MessagesClearAllDraftsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	list, err := a.msgs.ClearDrafts(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	for _, d := range list {
		a.tellDraft(ctx, me, d.DraftKey, &tg.DraftMessageEmpty{Date: now()})
	}
	return true, nil
}
