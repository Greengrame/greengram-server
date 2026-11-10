package botapi

import (
	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/ids"
)

func (s *Server) verifyPeer(c *call, user, remove bool) (any, error) {
	var to chat
	var id int64
	var err error
	var parsed []fmtEntity
	var description string
	var entities []tg.MessageEntityClass
	var entity tg.MessageEntityClass
	var ok bool
	if user {
		id, err = c.p.int64("user_id")
		if err != nil || id <= 0 {
			return nil, bad("invalid user_id")
		}
		to, err = s.chat(c.ctx, c.bot, params{"chat_id": ids.Client(ids.Server(id))}, "chat_id")
	} else {
		to, err = s.chat(c.ctx, c.bot, c.p, "chat_id")
	}
	if err != nil {
		return nil, err
	}
	description, parsed, err = botAPIFormattedTextRaw(c.p.str("custom_description"), c.p.str("custom_description_parse_mode"), c.p.str("custom_description_entities"), 70, false)
	if err != nil {
		return nil, bad(err.Error())
	}
	entities = tgEntities(parsed)
	for _, entity = range entities {
		if _, ok = entity.(*tg.MessageEntityTextURL); !ok {
			return nil, bad("only text_link entities are allowed in verification descriptions")
		}
	}
	err = s.Bots.VerifyPeer(c.ctx, c.bot.UserID, max(to.user, to.channel), to.channel != 0, tg.TextWithEntities{Text: description, Entities: entities}, remove)
	if err != nil {
		return nil, err
	}
	return true, nil
}

func verificationMethod(user, remove bool) method {
	return func(s *Server, c *call) (any, error) { return s.verifyPeer(c, user, remove) }
}
