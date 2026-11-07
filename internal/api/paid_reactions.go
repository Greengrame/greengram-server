package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"github.com/jackc/pgx/v5"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/messages"
	"github.com/Greengrame/greengram-server/internal/stars"
)

func init() {
	frozenOK[tlprofile.SemanticMethodMessagesGetPaidReactionPrivacy] = true
}

// Star reactions. Telegram pays these to the channel; here the Stars go to
// whoever wrote the message, and any chat can have them.
func (a *API) registerPaidReactions() {
	handle(a, tlprofile.SemanticMethodMessagesSendPaidReaction, a.sendPaidReaction)
	handle(a, tlprofile.SemanticMethodMessagesTogglePaidReactionPrivacy, a.togglePaidPrivacy)
	handle(a, tlprofile.SemanticMethodMessagesGetPaidReactionPrivacy, func(ctx context.Context, _ *tg.MessagesGetPaidReactionPrivacyRequest) (any, error) {
		up := emptyUpdates()
		up.Updates = append(up.Updates, &tg.UpdatePaidReactionPrivacy{Private: &tg.PaidReactionPrivacyDefault{}})
		return up, nil
	})
}

func paidAnon(p tg.PaidReactionPrivacyClass) bool {
	_, anon := p.(*tg.PaidReactionPrivacyAnonymous)
	return anon
}

func (a *API) sendPaidReaction(ctx context.Context, r *tg.MessagesSendPaidReactionRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if r.Count < 1 || r.Count > maxPaidReaction {
		return nil, tgerr.New(400, "REACTIONS_COUNT_INVALID")
	}
	anon := paidAnon(r.Private)
	ref := fmt.Sprintf("reaction:%d:%d", me.ID, r.RandomID)
	const title = "Star reaction"
	var author, shared int64
	var record func(pgx.Tx) error
	if to.channel != 0 {
		if author, err = a.chans.PaidTarget(ctx, me.ID, to.channel, r.MsgID); err != nil {
			return nil, err
		}
		record = func(tx pgx.Tx) error { return a.chans.AddPaid(ctx, tx, to.channel, r.MsgID, me.ID, r.Count, anon) }
	} else {
		if err := a.checkBlocked(ctx, me.ID, to.user); err != nil {
			return nil, err
		}
		if shared, author, err = a.msgs.PaidTarget(ctx, me.ID, to.user, r.MsgID); err != nil {
			return nil, err
		}
		record = func(tx pgx.Tx) error { return a.msgs.AddPaid(ctx, tx, shared, me.ID, r.Count, anon) }
	}
	if author == me.ID {
		return nil, messages.ErrReactionInvalid
	}
	err = a.stars.Tip(ctx, me.ID, author, int64(r.Count), ref, title, record)
	if err != nil && !errors.Is(err, stars.ErrDuplicate) {
		return nil, err
	}
	if err == nil {
		a.pushBalance(me.ID)
		a.pushBalance(author)
	}
	return a.pushPaid(ctx, me, to, shared, r.MsgID)
}

// pushPaid sends a message's current reactions round after a star change.
func (a *API) pushPaid(ctx context.Context, me account.User, to peer, shared int64, id int) (any, error) {
	if to.channel != 0 {
		c, ok, err := a.chans.Get(ctx, to.channel)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errPeerInvalid
		}
		sets, err := a.chans.Reactions(ctx, to.channel, []int{id})
		if err != nil {
			return nil, err
		}
		return a.channelReacted(ctx, me, to.channel, id, channels.Reacted{MsgReactions: sets[id], Channel: c})
	}
	sets, err := a.msgs.Reactions(ctx, me.ID, []int{id})
	if err != nil {
		return nil, err
	}
	boxes, err := a.msgs.BoxIDs(ctx, shared)
	if err != nil {
		return nil, err
	}
	return a.privateReacted(ctx, me, to.user, id, messages.Reacted{MsgReactions: sets[id], IDs: boxes})
}

func (a *API) togglePaidPrivacy(ctx context.Context, r *tg.MessagesTogglePaidReactionPrivacyRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	anon := paidAnon(r.Private)
	var shared int64
	if to.channel != 0 {
		if _, err := a.chans.PaidTarget(ctx, me.ID, to.channel, r.MsgID); err != nil {
			return nil, err
		}
		err = a.chans.SetPaidPrivacy(ctx, to.channel, r.MsgID, me.ID, anon)
	} else {
		if shared, _, err = a.msgs.PaidTarget(ctx, me.ID, to.user, r.MsgID); err != nil {
			return nil, err
		}
		err = a.msgs.SetPaidPrivacy(ctx, shared, me.ID, anon)
	}
	if err != nil {
		return nil, err
	}
	if _, err := a.pushPaid(ctx, me, to, shared, r.MsgID); err != nil {
		return nil, err
	}
	return true, nil
}
