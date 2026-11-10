package api

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/ids"
)

func (a *API) updateConnectedBot(ctx context.Context, r *tg.AccountUpdateConnectedBotRequest) (any, error) {
	var me account.User
	var bot bots.Bot
	var botID int64
	var connection bots.BusinessConnection
	var previous bots.BusinessConnection
	var found bool
	var err error
	var input tg.InputUserClass
	var id int64
	me, err = a.me(ctx)
	if err != nil {
		return nil, err
	}
	botID, err = a.inputUser(ctx, me, r.Bot)
	if err != nil {
		return nil, err
	}
	bot, found, err = a.bots.Get(ctx, botID)
	if err != nil {
		return nil, err
	}
	if !found || bot.Disabled || bot.Frozen || bot.BuiltIn() || !bot.Usable(me.ID) {
		return nil, tgerr.New(400, "BOT_INVALID")
	}
	previous, found, err = a.bots.ConnectedBusiness(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	if r.Deleted {
		if !found || previous.Bot != botID {
			return nil, tgerr.New(400, "BUSINESS_CONNECTION_INVALID")
		}
		previous.Enabled = false
		err = a.bots.DisableBusiness(ctx, me.ID)
		if err != nil {
			return nil, err
		}
		err = a.botAPI.QueueBusinessConnection(ctx, bot, previous)
		return businessUpdates(err)
	}
	connection = bots.BusinessConnection{Bot: botID, User: me.ID, Rights: r.Rights, Enabled: true}
	connection.Recipients = tg.BusinessBotRecipients{ExistingChats: r.Recipients.ExistingChats, NewChats: r.Recipients.NewChats, Contacts: r.Recipients.Contacts, NonContacts: r.Recipients.NonContacts, ExcludeSelected: r.Recipients.ExcludeSelected}
	for _, input = range r.Recipients.Users {
		id, err = a.inputUser(ctx, me, input)
		if err != nil {
			return nil, err
		}
		connection.Recipients.Users = append(connection.Recipients.Users, ids.Client(id))
	}
	for _, input = range r.Recipients.ExcludeUsers {
		id, err = a.inputUser(ctx, me, input)
		if err != nil {
			return nil, err
		}
		connection.Recipients.ExcludeUsers = append(connection.Recipients.ExcludeUsers, ids.Client(id))
	}
	connection, err = a.bots.ConnectBusiness(ctx, connection)
	if err != nil {
		return nil, err
	}
	if found {
		previous.Enabled = false
		err = a.botAPI.QueueBusinessConnection(ctx, bots.Bot{UserID: previous.Bot}, previous)
		if err != nil {
			return nil, err
		}
	}
	err = a.botAPI.QueueBusinessConnection(ctx, bot, connection)
	return businessUpdates(err)
}

func (a *API) getConnectedBots(ctx context.Context, r *tg.AccountGetConnectedBotsRequest) (any, error) {
	var me account.User
	var connection bots.BusinessConnection
	var found bool
	var err error
	var users []tg.UserClass
	var out = &tg.AccountConnectedBots{ConnectedBots: []tg.ConnectedBot{}, Users: []tg.UserClass{}}
	me, err = a.me(ctx)
	if err != nil {
		return nil, err
	}
	if a.bots == nil {
		return out, nil
	}
	connection, found, err = a.bots.ConnectedBusiness(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	if !found {
		return out, nil
	}
	users, err = a.usersFor(ctx, me.ID, []int64{connection.Bot})
	if err != nil {
		return nil, err
	}
	out.Users = users
	out.ConnectedBots = append(out.ConnectedBots, tg.ConnectedBot{BotID: ids.Client(connection.Bot), Recipients: connection.Recipients, Rights: connection.Rights})
	return out, nil
}

func businessUpdates(err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return &tg.Updates{Updates: []tg.UpdateClass{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: now()}, nil
}
