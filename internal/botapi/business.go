package botapi

import (
	"context"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/ids"
)

func (s *Server) businessJSON(c *call, connection bots.BusinessConnection) map[string]any {
	var v = s.view(c.ctx, c.bot)
	return map[string]any{
		"id": connection.ID, "user": v.user(connection.User), "user_chat_id": ids.Client(connection.User), "date": connection.Date, "is_enabled": connection.Enabled,
		"rights": map[string]any{"can_read_messages": connection.Rights.ReadMessages, "can_convert_gifts_to_stars": connection.Rights.SellGifts, "can_transfer_and_upgrade_gifts": connection.Rights.TransferAndUpgradeGifts, "can_transfer_stars": connection.Rights.TransferStars, "can_view_gifts_and_stars": connection.Rights.ViewGifts},
	}
}

func (s *Server) getBusinessConnection(c *call) (any, error) {
	var connection bots.BusinessConnection
	var err error
	connection, err = s.Bots.BusinessConnection(c.ctx, c.bot.UserID, c.p.str("business_connection_id"))
	if err != nil {
		return nil, err
	}
	return s.businessJSON(c, connection), nil
}

func (s *Server) deleteBusinessAccountGift(c *call) (any, error) {
	var id int64
	var err error
	id, err = c.p.int64("owned_gift_id")
	if err != nil || id <= 0 {
		return nil, bad("invalid owned_gift_id")
	}
	err = s.Bots.DeleteBusinessGift(c.ctx, c.bot.UserID, c.p.str("business_connection_id"), id)
	if err != nil {
		return nil, err
	}
	return true, nil
}

func (s *Server) QueueBusinessConnection(ctx context.Context, bot bots.Bot, connection bots.BusinessConnection) error {
	return s.Bots.QueueBusinessConnection(ctx, connection, s.businessJSON(&call{ctx: ctx, bot: bot}, connection))
}
