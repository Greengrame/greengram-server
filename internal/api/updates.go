package api

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/messages"
)

// The journal (internal/messages) is the source; these two calls are how a
// client that was away catches up exactly.
func (a *API) registerUpdates() {
	handle(a, tlprofile.SemanticMethodUpdatesGetState, func(ctx context.Context, _ *tg.UpdatesGetStateRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		st, err := a.msgs.State(ctx, me.ID)
		return &st, err
	})
	handle(a, tlprofile.SemanticMethodUpdatesGetDifference, func(ctx context.Context, r *tg.UpdatesGetDifferenceRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		d, err := a.msgs.Difference(ctx, me.ID, r.Pts)
		if err != nil {
			return nil, err
		}
		if d.TooLong {
			return &tg.UpdatesDifferenceTooLong{Pts: d.State.Pts}, nil
		}
		if len(d.Updates) == 0 {
			return &tg.UpdatesDifferenceEmpty{Date: d.State.Date, Seq: d.State.Seq}, nil
		}
		newMsgs := []tg.MessageClass{}
		other := []tg.UpdateClass{}
		var who []int64
		for _, u := range d.Updates {
			who = append(who, updateUsers(u)...)
			if nm, ok := u.(*tg.UpdateNewMessage); ok {
				newMsgs = append(newMsgs, nm.Message)
				continue
			}
			other = append(other, u)
		}
		users, err := a.usersFor(ctx, me.ID, append(who, me.ID))
		if err != nil {
			return nil, err
		}
		if d.More {
			return &tg.UpdatesDifferenceSlice{NewMessages: newMsgs, NewEncryptedMessages: []tg.EncryptedMessageClass{},
				OtherUpdates: other, Chats: []tg.ChatClass{}, Users: users, IntermediateState: d.State}, nil
		}
		return &tg.UpdatesDifference{NewMessages: newMsgs, NewEncryptedMessages: []tg.EncryptedMessageClass{},
			OtherUpdates: other, Chats: []tg.ChatClass{}, Users: users, State: d.State}, nil
	})
}

var _ = messages.MaxLength
