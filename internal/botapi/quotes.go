package botapi

import (
	"encoding/json"
	"strings"
	"unicode/utf16"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/messages"
)

func (s *Server) readReplyQuote(c *call, to chat, out *Out) error {
	var input struct {
		Quote     string            `json:"quote"`
		Position  int               `json:"quote_position"`
		Entities  json.RawMessage   `json:"quote_entities"`
		ParseMode string            `json:"quote_parse_mode"`
		RichTexts []json.RawMessage `json:"quote_rich_texts"`
		ChatID    json.RawMessage   `json:"chat_id"`
	}
	var header = tg.MessageReplyHeader{Quote: true}
	var rich []tg.RichTextClass
	var item json.RawMessage
	var text tg.RichTextClass
	var plain strings.Builder
	var source string
	var sourceRich []byte
	var private []messages.Msg
	var group []channels.Msg
	var entities []fmtEntity
	var err error
	_, err = c.p.json("reply_parameters", &input)
	if err != nil {
		return err
	}
	if input.Quote == "" && len(input.RichTexts) == 0 {
		return nil
	}
	if out.ReplyTo <= 0 {
		return bad("a quote requires message_id")
	}
	if len(input.ChatID) != 0 {
		return bad("cross-chat replies are not supported")
	}
	if input.Position < 0 {
		return bad("invalid quote position")
	}
	if to.channel != 0 {
		group, err = s.Chans.Messages(c.ctx, to.channel, []int{out.ReplyTo})
		if err != nil {
			return err
		}
		if len(group) != 1 {
			return bad("reply message not found")
		}
		source, sourceRich = group[0].Text, group[0].Rich
	} else {
		private, err = s.Msgs.Get(c.ctx, c.bot.UserID, []int{out.ReplyTo})
		if err != nil {
			return err
		}
		if len(private) != 1 || private[0].Peer != to.user {
			return bad("reply message not found")
		}
		source, sourceRich = private[0].Text, private[0].Rich
	}
	if len(input.RichTexts) != 0 {
		if len(sourceRich) == 0 || len(input.RichTexts) > 500 {
			return bad("quote_rich_texts requires a rich message")
		}
		rich = []tg.RichTextClass{}
		for _, item = range input.RichTexts {
			text, err = parseRichText(item, 0)
			if err != nil {
				return err
			}
			if !strings.Contains(source, richTextPlain(text)) {
				return bad("quoted rich text is not in the original message")
			}
			if plain.Len() != 0 {
				plain.WriteByte('\n')
			}
			plain.WriteString(richTextPlain(text))
			rich = append(rich, text)
		}
		header.SetQuoteRich(rich)
		input.Quote = plain.String()
	} else {
		input.Quote, entities, err = botAPIFormattedTextRaw(input.Quote, input.ParseMode, string(input.Entities), 1024, true)
		if err != nil {
			return bad(err.Error())
		}
		if !strings.Contains(source, input.Quote) {
			return bad("quoted text is not in the original message")
		}
		header.SetQuoteEntities(tgEntities(entities))
	}
	if len(utf16.Encode([]rune(input.Quote))) > 1024 {
		return bad("quote is too long")
	}
	header.SetQuoteText(input.Quote)
	header.SetQuoteOffset(input.Position)
	out.Quote, err = messages.EncodeReplyQuote(&header)
	return err
}

func (v *view) quoteBody(out *apiMessage, raw []byte) {
	var header *tg.MessageReplyHeader
	var texts = []any{}
	var text tg.RichTextClass
	if len(raw) == 0 {
		return
	}
	header = messages.ReplyHeader(0, raw)
	out.Quote = map[string]any{"text": header.QuoteText, "position": header.QuoteOffset, "is_manual": header.Quote}
	if len(header.QuoteEntities) != 0 {
		out.Quote["entities"] = apiEntities(header.QuoteEntities)
	}
	if len(header.QuoteRich) != 0 {
		for _, text = range header.QuoteRich {
			texts = append(texts, v.richTextJSON(text))
		}
		out.Quote["rich_texts"] = texts
	}
}
