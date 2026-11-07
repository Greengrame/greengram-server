package api

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/messages"
)

func (a *API) registerTodo() {
	handle(a, tlprofile.SemanticMethodMessagesToggleTodoCompleted, a.toggleTodo)
	handle(a, tlprofile.SemanticMethodMessagesAppendTodoList, a.appendTodo)
}

func (a *API) toggleTodo(ctx context.Context, r *tg.MessagesToggleTodoCompletedRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	by := &tg.PeerUser{UserID: clientID(me.ID)}
	act := &tg.MessageActionTodoCompletions{Completed: r.Completed, Incompleted: r.Incompleted}
	return a.changeTodo(ctx, me, to, r.MsgID, act, func(m *tg.MessageMediaToDo, own bool) (bool, error) {
		if err := messages.TodoRules(m, own, false); err != nil {
			return false, err
		}
		return media.ToggleTodo(m, by, r.Completed, r.Incompleted, now())
	})
}

func (a *API) appendTodo(ctx context.Context, r *tg.MessagesAppendTodoListRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	act := &tg.MessageActionTodoAppendTasks{List: r.List}
	return a.changeTodo(ctx, me, to, r.MsgID, act, func(m *tg.MessageMediaToDo, own bool) (bool, error) {
		if err := messages.TodoRules(m, own, true); err != nil {
			return false, err
		}
		return true, media.AppendTodo(m, r.List)
	})
}

func (a *API) changeTodo(ctx context.Context, me account.User, to peer, id int, act tg.MessageActionClass, change func(*tg.MessageMediaToDo, bool) (bool, error)) (any, error) {
	if to.channel != 0 {
		events, err := a.chans.ChangeTodo(ctx, me.ID, to.channel, id, act, change)
		if err != nil {
			return nil, err
		}
		return a.channelOp(ctx, me, to.channel, events)
	}
	u, events, err := a.msgs.ChangeTodo(ctx, me.ID, to.user, id, change)
	if err != nil {
		return nil, err
	}
	if u == nil { // nothing changed
		return emptyUpdates(), nil
	}
	list := []tg.UpdateClass{u}
	// the service message is sent by whoever ticked, in their own dialog
	s, err := a.msgs.Send(ctx, me.ID, to.user, messages.Draft{Action: act, ReplyTo: id})
	if err != nil {
		return nil, err
	}
	list = append(list, s.Update)
	events = append(events, messages.Event{UserID: me.ID, Update: s.Update})
	events = append(events, s.Events...)
	return a.privateOp(ctx, me, to.user, list, events)
}
