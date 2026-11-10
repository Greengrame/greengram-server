package botapi

import (
	"strconv"
	"time"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/messages"
)

type ephemeralParameters struct {
	Receiver int64  `json:"receiver_user_id"`
	Callback string `json:"callback_query_id"`
	Replace  bool   `json:"replace_callback_query_message"`
	Anchor   int
}

func (s *Server) messageChat(c *call) (chat, error) {
	var p ephemeralParameters
	var present, found bool
	var err error
	var query bots.Callback
	var queryID int64
	var to chat
	var membership channels.Membership
	var originals []messages.Msg
	var receiverCopy messages.Msg
	present, err = c.p.json("ephemeral_message_parameters", &p)
	if err != nil {
		return to, err
	}
	if !present && c.p.has("receiver_user_id") {
		return to, bad("use ephemeral_message_parameters for receiver_user_id")
	}
	if !present {
		return s.chat(c.ctx, c.bot, c.p, "chat_id")
	}
	if p.Receiver < 0 {
		return to, bad("invalid receiver_user_id")
	}
	if p.Callback != "" {
		queryID, err = strconv.ParseInt(p.Callback, 10, 64)
		if err != nil {
			return to, bad("invalid callback query")
		}
		query, found, err = s.Bots.Callback(c.ctx, queryID)
		if err != nil {
			return to, err
		}
		if !found || query.Bot != c.bot.UserID || time.Since(query.Created) > 15*time.Minute {
			return to, bad("invalid callback query")
		}
		if p.Receiver != 0 && ids.Server(p.Receiver) != query.User {
			return to, bad("receiver does not match callback query")
		}
		if p.Receiver == 0 {
			to = chat{user: query.User}
			if query.Channel {
				to = chat{channel: query.ChatID}
			}
		} else {
			to, err = s.chat(c.ctx, c.bot, c.p, "chat_id")
			if err != nil {
				return to, err
			}
			if query.Channel != (to.channel != 0) || query.Channel && query.ChatID != to.channel || !query.Channel && query.User != to.user {
				return to, bad("callback query belongs to another chat")
			}
		}
		p.Receiver = query.User
		if p.Replace {
			p.Anchor = query.MsgID
			if !query.Channel && query.InlineID == "" {
				originals, err = s.Msgs.Get(c.ctx, c.bot.UserID, []int{query.MsgID})
				if err != nil || len(originals) != 1 {
					return to, bad("callback message no longer exists")
				}
				receiverCopy, found, err = s.Msgs.CopyIn(c.ctx, query.User, originals[0].Shared)
				if err != nil || !found {
					return to, bad("callback message no longer exists")
				}
				p.Anchor = receiverCopy.ID
			}
		}
	} else {
		if p.Receiver == 0 {
			return to, bad("receiver_user_id or callback_query_id is required")
		}
		if p.Replace {
			return to, bad("replace_callback_query_message requires a callback query")
		}
		p.Receiver = ids.Server(p.Receiver)
		to, err = s.chat(c.ctx, c.bot, c.p, "chat_id")
		if err != nil {
			return to, err
		}
		if to.channel == 0 && to.user != p.Receiver {
			return to, bad("receiver does not belong to this private chat")
		}
		err = s.writable(c.ctx, c.bot, to)
		if err != nil {
			return to, err
		}
		if to.channel != 0 {
			_, membership, err = s.Chans.Access(c.ctx, to.channel, c.bot.UserID)
			if err != nil {
				return to, err
			}
			if !channels.InChat(membership.Status) {
				return to, forbidden("bot is not a member of the chat")
			}
			membership, err = s.Chans.Member(c.ctx, to.channel, p.Receiver)
			if err != nil {
				return to, err
			}
			if !channels.InChat(membership.Status) {
				return to, bad("receiver is not a member of the chat")
			}
		}
	}
	c.ephemeral = &p
	return to, nil
}

func (s *Server) messageWritable(c *call, to chat) error {
	if c.ephemeral != nil {
		return nil
	}
	return s.writable(c.ctx, c.bot, to)
}

func (s *Server) sendCall(c *call, to chat, out Out) (any, error) {
	var record bots.Ephemeral
	var err error
	var blocked bool
	var rich tg.RichMessage
	var ok bool
	out.Topic, err = c.p.int("message_thread_id")
	if err != nil || out.Topic < 0 {
		return nil, bad("invalid message_thread_id")
	}
	err = s.readReplyQuote(c, to, &out)
	if err != nil {
		return nil, err
	}
	if c.ephemeral == nil {
		return s.send(c.ctx, c.bot, to, out)
	}
	if !c.bot.Usable(c.ephemeral.Receiver) {
		return nil, forbidden("bot is restricted to selected users")
	}
	_, blocked, err = s.Msgs.Blocks(c.ctx, c.bot.UserID, c.ephemeral.Receiver)
	if err != nil {
		return nil, err
	}
	if blocked {
		return nil, forbidden("bot was blocked by the user")
	}
	record = bots.Ephemeral{Bot: c.bot.UserID, Receiver: c.ephemeral.Receiver, Chat: to.user, Channel: to.channel != 0}
	record.Message = tg.EphemeralMessage{FromID: &tg.PeerUser{UserID: ids.Client(c.bot.UserID)}, ReceiverID: ids.Client(record.Receiver), Date: s.now(), Message: out.Text, Noforwards: true}
	if rich, ok = media.DecodeRich(out.Rich); ok {
		record.Message.SetRichMessage(rich)
	}
	record.Message.SetPeerID(&tg.PeerUser{UserID: ids.Client(c.bot.UserID)})
	if to.channel != 0 {
		record.Chat = to.channel
		record.Message.SetPeerID(&tg.PeerChannel{ChannelID: to.channel})
	}
	if len(out.Entities) != 0 {
		record.Message.SetEntities(out.Entities)
	}
	if out.Markup != nil {
		record.Message.SetReplyMarkup(out.Markup)
	}
	if !out.Media.Empty() {
		record.Message.SetMedia(out.Media.TL)
	}
	if c.ephemeral.Anchor != 0 {
		record.Message.SetAnchorMsgID(c.ephemeral.Anchor)
	}
	record, err = s.Bots.CreateEphemeral(c.ctx, record)
	if err != nil {
		return nil, err
	}
	s.Push.Deliver([]messages.Event{{UserID: record.Receiver, Update: &tg.UpdateNewEphemeralMessage{Message: record.Message}}})
	return s.ephemeralJSON(c, to, record), nil
}

func (s *Server) ephemeralJSON(c *call, to chat, record bots.Ephemeral) *apiMessage {
	var v = s.view(c.ctx, c.bot)
	var rich tg.RichMessage
	var ok bool
	var out = &apiMessage{From: v.user(record.Bot), Chat: v.chat(to), Date: record.Message.Date, EphemeralMessageID: record.Message.ID, ReceiverUser: v.user(record.Receiver)}
	v.body(out, record.Message.Message, record.Message.Entities, record.Message.ReplyMarkup, false)
	v.mediaBody(out, media.Ref{TL: record.Message.Media})
	if rich, ok = record.Message.GetRichMessage(); ok {
		out.RichMessage = map[string]any{"blocks": v.richBlocksJSON(rich.Blocks), "is_rtl": rich.Rtl}
		out.Text = ""
		out.Entities = nil
	}
	return out
}

func (s *Server) editEphemeral(c *call, action string) (any, error) {
	var to chat
	var receiver int64
	var id int
	var record bots.Ephemeral
	var err error
	var textValue string
	var entities []tg.MessageEntityClass
	var markupValue tg.ReplyMarkupClass
	var update tg.UpdateClass
	var replacement Out
	var rawRich []byte
	var rich tg.RichMessage
	var hasRich bool
	to, err = s.chat(c.ctx, c.bot, c.p, "chat_id")
	if err != nil {
		return nil, err
	}
	receiver, err = c.p.int64("receiver_user_id")
	if err != nil || receiver <= 0 {
		return nil, bad("receiver_user_id is required")
	}
	id, err = c.p.int("ephemeral_message_id")
	if err != nil || id <= 0 {
		return nil, bad("ephemeral_message_id is required")
	}
	record, err = s.Bots.Ephemeral(c.ctx, c.bot.UserID, ids.Server(receiver), max(to.user, to.channel), to.channel != 0, id)
	if err != nil {
		return nil, err
	}
	switch action {
	case "text", "caption":
		if action == "text" {
			if c.p.has("rich_message") {
				rawRich, textValue, err = s.readRich(c)
				rich, hasRich = media.DecodeRich(rawRich)
				if hasRich {
					record.Message.SetRichMessage(rich)
				}
			} else {
				textValue, entities, err = text(c.p, "text", "entities")
				record.Message.RichMessage = tg.RichMessage{}
				record.Message.Flags.Unset(8)
			}
		} else {
			textValue, entities, err = botCaption(c.p)
		}
		if err != nil {
			return nil, err
		}
		record.Message.Message = textValue
		record.Message.SetEntities(entities)
	case "media":
		replacement, err = s.readInputMedia(c)
		if err != nil {
			return nil, err
		}
		record.Message.SetMedia(replacement.Media.TL)
		record.Message.Message = replacement.Text
		record.Message.SetEntities(replacement.Entities)
	case "markup":
		markupValue, _, err = markup(c.p)
		if err != nil {
			return nil, err
		}
		record.Message.SetReplyMarkup(markupValue)
	}
	err = s.Bots.UpdateEphemeral(c.ctx, record, action == "delete")
	if err != nil {
		return nil, err
	}
	update = &tg.UpdateEditEphemeralMessage{Message: record.Message}
	if action == "delete" {
		update = &tg.UpdateDeleteEphemeralMessages{Peer: record.Message.PeerID, IDs: []int{id}}
	}
	s.Push.Deliver([]messages.Event{{UserID: record.Receiver, Update: update}})
	return true, nil
}

func ephemeralMethod(action string) method {
	return func(s *Server, c *call) (any, error) { return s.editEphemeral(c, action) }
}
