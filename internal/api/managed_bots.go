package api

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/messages"
)

func (a *API) createManagedBot(ctx context.Context, r *tg.BotsCreateBotRequest) (any, error) {
	var me account.User
	var manager int64
	var bot bots.Bot
	var managerBot bots.Bot
	var found bool
	var result *tg.User
	var user account.User
	var err error
	me, err = a.me(ctx)
	if err != nil {
		return nil, err
	}
	if me.IsBot {
		return nil, tgerr.New(403, "USER_BOT_INVALID")
	}
	manager, err = a.inputUser(ctx, me, r.ManagerID)
	if err != nil {
		return nil, err
	}
	managerBot, found, err = a.bots.Get(ctx, manager)
	if err != nil {
		return nil, err
	}
	if !found || managerBot.Disabled || managerBot.Frozen || managerBot.BuiltIn() || !managerBot.Usable(me.ID) {
		return nil, errBotInvalid
	}
	bot, _, err = a.bots.CreateManaged(ctx, me.ID, manager, r.Name, r.Username)
	if err != nil {
		return nil, err
	}
	user, found, err = a.accounts.User(ctx, bot.UserID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errBotInvalid
	}
	result = tgUser(user, false)
	botFlags(me.ID, result, bot)
	return result, nil
}

func (a *API) shareManagedBot(ctx context.Context, r *tg.MessagesSendBotRequestedPeerRequest) (any, error) {
	var me account.User
	var target, created peer
	var originals []messages.Msg
	var row tg.KeyboardButtonRow
	var button tg.KeyboardButton
	var request *tg.ButtonTypeRequestPeer
	var ok, found, managed bool
	var result messages.Sent
	var bot bots.Bot
	var markup *tg.ReplyKeyboardMarkup
	var valid bool
	var err error
	me, err = a.me(ctx)
	if err != nil {
		return nil, err
	}
	target, err = a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if target.channel != 0 || len(r.RequestedPeers) != 1 {
		return nil, tgerr.New(400, "PEER_ID_INVALID")
	}
	originals, err = a.msgs.Get(ctx, me.ID, []int{r.MsgID})
	if err != nil {
		return nil, err
	}
	if len(originals) != 1 || originals[0].Peer != target.user || originals[0].From != target.user {
		return nil, tgerr.New(400, "MESSAGE_ID_INVALID")
	}
	if markup, valid = originals[0].ReplyMarkup.(*tg.ReplyKeyboardMarkup); valid {
		for _, row = range markup.Rows {
			for _, button = range row.Buttons {
				request, ok = button.Type.(*tg.ButtonTypeRequestPeer)
				if ok && request.ButtonID == r.ButtonID {
					if _, ok = request.PeerType.(*tg.RequestPeerTypeCreateBot); ok {
						found = true
					}
				}
			}
		}
	}
	if !found {
		return nil, tgerr.New(400, "BUTTON_ID_INVALID")
	}
	created, err = a.peer(ctx, me, r.RequestedPeers[0])
	if err != nil {
		return nil, err
	}
	if created.channel != 0 {
		return nil, errBotInvalid
	}
	bot, found, err = a.bots.Get(ctx, created.user)
	if err != nil {
		return nil, err
	}
	if !found || bot.OwnerID != me.ID {
		return nil, errBotInvalid
	}
	managed, err = a.bots.Managed(ctx, target.user, created.user)
	if err != nil {
		return nil, err
	}
	if !managed {
		return nil, errBotInvalid
	}
	result, err = a.msgs.Send(ctx, me.ID, target.user, messages.Draft{Action: &tg.MessageActionManagedBotCreated{BotID: ids.Client(created.user)}, Text: "Managed bot created"})
	if err != nil {
		return nil, err
	}
	a.Deliver(result.Events)
	return &tg.Updates{Updates: []tg.UpdateClass{result.Update}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: now()}, nil
}
