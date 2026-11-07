package botapi

import (
	"encoding/json"
	"strconv"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/messages"
)

// Results stay as text: the Bot API here takes no files yet, so results that
// need a photo, GIF or document are refused out loud instead of arriving empty.
type apiInlineResult struct {
	Type         string          `json:"type"`
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	URL          string          `json:"url"`
	HideURL      bool            `json:"hide_url"`
	ReplyMarkup  json.RawMessage `json:"reply_markup"`
	InputContent *struct {
		MessageText string          `json:"message_text"`
		ParseMode   string          `json:"parse_mode"`
		Entities    json.RawMessage `json:"entities"`
		NoPreview   bool            `json:"disable_web_page_preview"`
		Preview     *struct {
			Disabled bool `json:"is_disabled"`
		} `json:"link_preview_options"`
	} `json:"input_message_content"`
}

func parseResult(r apiInlineResult) (bots.InlineResult, error) {
	if r.Type != "article" {
		return bots.InlineResult{}, bad("only article results are supported on this server, not " + r.Type)
	}
	if n := len(r.ID); n < 1 || n > 64 {
		return bots.InlineResult{}, bad("RESULT_ID_INVALID")
	}
	if r.Title == "" {
		return bots.InlineResult{}, bad("RESULT_TITLE_EMPTY")
	}
	c := r.InputContent
	if c == nil || c.MessageText == "" {
		return bots.InlineResult{}, bad("only input_message_content with message_text is supported on this server")
	}
	out := bots.InlineResult{ID: r.ID, Type: r.Type, Title: r.Title, Description: r.Description, Text: c.MessageText, ParseMode: c.ParseMode,
		Entities: c.Entities, NoWebpage: c.NoPreview || c.Preview != nil && c.Preview.Disabled, Markup: r.ReplyMarkup}
	if r.URL != "" && !r.HideURL {
		out.URL = r.URL
	}
	// a result that can't be sent should fail here, where the bot hears about it
	if _, _, _, err := InlineMessage(out); err != nil {
		return out, err
	}
	return out, nil
}

// InlineMessage turns a stored result into what gets sent.
func InlineMessage(r bots.InlineResult) (string, []tg.MessageEntityClass, tg.ReplyMarkupClass, error) {
	t, ents, err := botAPIFormattedTextRaw(r.Text, r.ParseMode, string(r.Entities), messages.MaxLength, true)
	if err != nil {
		if e := asAPIError(err); e != nil {
			return "", nil, nil, err
		}
		return "", nil, nil, bad(err.Error())
	}
	var mk tg.ReplyMarkupClass
	if len(r.Markup) > 0 && string(r.Markup) != "null" {
		var m apiMarkup
		if json.Unmarshal(r.Markup, &m) != nil {
			return "", nil, nil, bad("can't parse reply_markup JSON object")
		}
		if m.InlineKeyboard == nil {
			return "", nil, nil, bad("only inline keyboards can be attached to inline results")
		}
		if mk, err = tgMarkup(m); err != nil {
			return "", nil, nil, err
		}
	}
	return t, tgEntities(ents), mk, nil
}

// InlineResultTL is a result as the person's app lists it.
func InlineResultTL(r bots.InlineResult) (tg.BotInlineResultClass, error) {
	t, ents, mk, err := InlineMessage(r)
	if err != nil {
		return nil, err
	}
	msg := &tg.BotInlineMessageText{NoWebpage: r.NoWebpage, Message: t}
	if len(ents) > 0 {
		msg.SetEntities(ents)
	}
	if mk != nil {
		msg.SetReplyMarkup(mk)
	}
	out := &tg.BotInlineResult{ID: r.ID, Type: r.Type, SendMessage: msg}
	out.SetTitle(r.Title)
	if r.Description != "" {
		out.SetDescription(r.Description)
	}
	if r.URL != "" {
		out.SetURL(r.URL)
	}
	return out, nil
}

func validStartParam(s string) bool {
	if n := len(s); n < 1 || n > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func (s *Server) answerInlineQuery(c *call) (any, error) {
	id, err := strconv.ParseInt(c.p.str("inline_query_id"), 10, 64)
	if err != nil {
		return nil, bad(bots.ErrQueryInvalid.Error())
	}
	var list []apiInlineResult
	if ok, err := c.p.json("results", &list); err != nil {
		return nil, err
	} else if !ok {
		return nil, bad("results is empty")
	}
	if len(list) > 50 {
		return nil, bad("RESULTS_TOO_MUCH")
	}
	a := bots.InlineAnswer{Results: []bots.InlineResult{}, NextOffset: c.p.str("next_offset")}
	if len(a.NextOffset) > 64 {
		return nil, bad("NEXT_OFFSET_INVALID")
	}
	seen := map[string]bool{}
	for _, r := range list {
		res, err := parseResult(r)
		if err != nil {
			return nil, err
		}
		if seen[res.ID] {
			return nil, bad("RESULT_ID_DUPLICATE")
		}
		seen[res.ID] = true
		a.Results = append(a.Results, res)
	}
	var btn struct {
		Text   string     `json:"text"`
		WebApp *apiWebApp `json:"web_app"`
		Start  string     `json:"start_parameter"`
	}
	if _, err := c.p.json("button", &btn); err != nil {
		return nil, err
	}
	if btn.Text == "" && c.p.has("switch_pm_text") {
		btn.Text, btn.Start = c.p.str("switch_pm_text"), c.p.str("switch_pm_parameter")
	}
	if btn.Text != "" {
		if utf8.RuneCountInString(btn.Text) > 64 {
			return nil, bad("BUTTON_TEXT_INVALID")
		}
		switch {
		case btn.WebApp != nil:
			if !validURL(btn.WebApp.URL, "https", "http") {
				return nil, bad("BUTTON_URL_INVALID")
			}
			a.ButtonText, a.ButtonApp = btn.Text, btn.WebApp.URL
		case validStartParam(btn.Start):
			a.ButtonText, a.ButtonStart = btn.Text, btn.Start
		default:
			return nil, bad("START_PARAM_INVALID")
		}
	}
	// Telegram's default; a bot that wants no cache says cache_time=0
	cache := 300
	if c.p.has("cache_time") {
		if cache, err = c.p.int("cache_time"); err != nil {
			return nil, err
		}
	}
	cache = min(max(cache, 0), 86400)
	if err := s.Bots.AnswerInline(c.ctx, c.bot.UserID, id, a, c.p.bool("is_personal"), cache); err != nil {
		return nil, err
	}
	return true, nil
}

// editInline edits a message sent via the bot; Telegram answers true, not the
// message, since a bot may not be able to see it.
func (s *Server) editInline(c *call, ch messages.Change) (any, error) {
	shared, channel, msg, ok := bots.ParseInlineID(c.p.str("inline_message_id"))
	if !ok {
		return nil, bad("inline message not found")
	}
	if channel != 0 {
		list, err := s.Chans.Messages(c.ctx, channel, []int{msg})
		if err != nil {
			return nil, err
		}
		if len(list) == 0 || list[0].ViaBot != c.bot.UserID {
			return nil, bad("inline message not found")
		}
		_, events, err := s.Chans.EditBot(c.ctx, c.bot.UserID, channel, msg, ch)
		if err != nil {
			return nil, err
		}
		s.Push.DeliverChannel(events)
		return true, nil
	}
	events, err := s.Msgs.EditInline(c.ctx, c.bot.UserID, shared, ch)
	if err != nil {
		return nil, err
	}
	s.Push.Deliver(events)
	return true, nil
}
