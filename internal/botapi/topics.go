package botapi

import (
	"strconv"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/messages"
)

func (s *Server) privateForumTopic(c *call, action string) (any, error) {
	var to chat
	var id, color int
	var emoji int64
	var draft messages.Draft
	var sent messages.Sent
	var edit = tg.MessageActionTopicEdit{}
	var create tg.MessageActionTopicCreate
	var events []messages.Event
	var present bool
	var err error
	to, err = s.chat(c.ctx, c.bot, c.p, "chat_id")
	if err != nil {
		return nil, err
	}
	if to.channel != 0 {
		return nil, bad("this method currently supports private bot topics")
	}
	err = s.writable(c.ctx, c.bot, to)
	if err != nil {
		return nil, err
	}
	if !bool(c.bot.State.Settings.Topics) {
		return nil, bad("enable topic mode in BotFather first")
	}
	if action == "create" {
		color, err = c.p.int("icon_color")
		if err != nil {
			return nil, err
		}
		if c.p.has("icon_custom_emoji_id") {
			emoji, err = strconv.ParseInt(c.p.str("icon_custom_emoji_id"), 10, 64)
			if err != nil || emoji <= 0 {
				return nil, bad("invalid custom emoji")
			}
		}
		create = tg.MessageActionTopicCreate{Title: c.p.str("name"), IconColor: color, IconEmojiID: emoji}
		draft = messages.Draft{Text: create.Title, Action: &create}
	} else {
		id, err = c.p.int("message_thread_id")
		if err != nil || id <= 0 {
			return nil, bad("message_thread_id is required")
		}
		if action == "delete" {
			events, err = s.Msgs.DeletePrivateTopic(c.ctx, c.bot.UserID, to.user, id)
			if err != nil {
				return nil, err
			}
			s.Push.Deliver(events)
			return true, nil
		}
		if action == "close" {
			edit.SetClosed(true)
		}
		if action == "reopen" {
			edit.SetClosed(false)
		}
		if action == "edit" {
			if c.p.has("name") {
				edit.SetTitle(c.p.str("name"))
			}
			if _, present = c.p["icon_custom_emoji_id"]; present {
				if c.p.str("icon_custom_emoji_id") != "" {
					emoji, err = strconv.ParseInt(c.p.str("icon_custom_emoji_id"), 10, 64)
					if err != nil {
						return nil, bad("invalid custom emoji")
					}
				}
				edit.SetIconEmojiID(emoji)
			}
		}
		draft = messages.Draft{Text: "Topic updated", Action: &edit, Topic: id}
	}
	sent, err = s.Msgs.Send(c.ctx, c.bot.UserID, to.user, draft)
	if err != nil {
		return nil, err
	}
	s.Push.Deliver(sent.Events)
	if action == "create" {
		return map[string]any{"message_thread_id": sent.Msg.ID, "name": create.Title, "icon_color": create.IconColor, "icon_custom_emoji_id": c.p.str("icon_custom_emoji_id")}, nil
	}
	return true, nil
}

func topicMethod(action string) method {
	return func(s *Server, c *call) (any, error) { return s.privateForumTopic(c, action) }
}
