package botapi

import "github.com/Greengrame/greengram-server/internal/ids"

func (s *Server) managedBotToken(c *call, replace bool) (any, error) {
	var id int64
	var found bool
	var token string
	var err error
	id, err = c.p.int64("user_id")
	if err != nil || id <= 0 {
		return nil, bad("user_id is required")
	}
	id = ids.Server(id)
	found, err = s.Bots.Managed(c.ctx, c.bot.UserID, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, forbidden("bot is not managed by this bot")
	}
	if replace {
		return s.Bots.Revoke(c.ctx, id)
	}
	token, found, err = s.Bots.Token(c.ctx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, bad("managed bot token is unavailable")
	}
	return token, nil
}

func managedTokenMethod(replace bool) method {
	return func(s *Server, c *call) (any, error) { return s.managedBotToken(c, replace) }
}
