package botapi

import (
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/iamxvbaba/td/tg"
)

// Bot API keyboards, as bots send them.
type apiMarkup struct {
	InlineKeyboard [][]apiInlineButton `json:"inline_keyboard,omitempty"`
	Keyboard       [][]apiButton       `json:"keyboard,omitempty"`
	Resize         bool                `json:"resize_keyboard,omitempty"`
	OneTime        bool                `json:"one_time_keyboard,omitempty"`
	Persistent     bool                `json:"is_persistent,omitempty"`
	Placeholder    string              `json:"input_field_placeholder,omitempty"`
	Selective      bool                `json:"selective,omitempty"`
	Remove         bool                `json:"remove_keyboard,omitempty"`
	ForceReply     bool                `json:"force_reply,omitempty"`
}

type apiWebApp struct {
	URL string `json:"url"`
}

type apiInlineButton struct {
	Text             string     `json:"text"`
	URL              string     `json:"url,omitempty"`
	CallbackData     *string    `json:"callback_data,omitempty"`
	WebApp           *apiWebApp `json:"web_app,omitempty"`
	SwitchInline     *string    `json:"switch_inline_query,omitempty"`
	SwitchInlineChat *string    `json:"switch_inline_query_current_chat,omitempty"`
	CopyText         *struct {
		Text string `json:"text"`
	} `json:"copy_text,omitempty"`
	Pay bool `json:"pay,omitempty"`
}

type apiButton struct {
	Managed         *apiManagedBotButton `json:"request_managed_bot,omitempty"`
	Text            string               `json:"text"`
	RequestContact  bool                 `json:"request_contact,omitempty"`
	RequestLocation bool                 `json:"request_location,omitempty"`
	WebApp          *apiWebApp           `json:"web_app,omitempty"`
}

// UnmarshalJSON also takes the plain-string form Bot API allows for reply
// keyboards: [["yes", "no"]].
func (b *apiButton) UnmarshalJSON(raw []byte) error {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		b.Text = s
		return nil
	}
	type plain apiButton
	return json.Unmarshal(raw, (*plain)(b))
}

func validURL(raw string, schemes ...string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" && u.Scheme != "tg" {
		return false
	}
	for _, s := range schemes {
		if u.Scheme == s {
			return true
		}
	}
	return false
}

// tgMarkup checks a bot's reply_markup and turns it into TL. Limits are
// Telegram's: 64-byte callback data, 100 buttons.
func tgMarkup(m apiMarkup) (tg.ReplyMarkupClass, error) {
	switch {
	case m.InlineKeyboard != nil:
		out := &tg.ReplyInlineMarkup{}
		n := 0
		for _, row := range m.InlineKeyboard {
			var r tg.KeyboardInlineButtonRow
			for _, b := range row {
				if n++; n > 100 {
					return nil, bad("too many inline keyboard buttons")
				}
				if b.Text == "" {
					return nil, bad("text buttons are unallowed in the inline keyboard")
				}
				btn := tg.KeyboardInlineButton{Text: b.Text}
				switch {
				case b.CallbackData != nil:
					if len(*b.CallbackData) > 64 || *b.CallbackData == "" {
						return nil, bad("BUTTON_DATA_INVALID")
					}
					btn.Type = &tg.InlineButtonTypeCallback{Data: []byte(*b.CallbackData)}
				case b.URL != "":
					if !validURL(b.URL, "http", "https", "tg") {
						return nil, bad("inline keyboard button URL '" + b.URL + "' is invalid: Wrong HTTP URL")
					}
					btn.Type = &tg.InlineButtonTypeURL{URL: b.URL}
				case b.WebApp != nil:
					if !validURL(b.WebApp.URL, "https", "http") {
						return nil, bad("inline keyboard button Web App URL '" + b.WebApp.URL + "' is invalid: Only HTTPS links are allowed")
					}
					btn.Type = &tg.InlineButtonTypeWebView{URL: b.WebApp.URL}
				case b.SwitchInline != nil:
					btn.Type = &tg.InlineButtonTypeSwitchInline{Query: *b.SwitchInline}
				case b.SwitchInlineChat != nil:
					btn.Type = &tg.InlineButtonTypeSwitchInline{SamePeer: true, Query: *b.SwitchInlineChat}
				case b.CopyText != nil:
					btn.Type = &tg.InlineButtonTypeCopy{CopyText: b.CopyText.Text}
				case b.Pay:
					btn.Type = &tg.InlineButtonTypeBuy{}
				default:
					return nil, bad("text buttons are unallowed in the inline keyboard")
				}
				r.Buttons = append(r.Buttons, btn)
			}
			if len(r.Buttons) > 0 {
				out.Rows = append(out.Rows, r)
			}
		}
		return out, nil
	case m.Keyboard != nil:
		out := &tg.ReplyKeyboardMarkup{Resize: m.Resize, SingleUse: m.OneTime, Selective: m.Selective, Persistent: m.Persistent}
		if m.Placeholder != "" {
			out.SetPlaceholder(m.Placeholder)
		}
		for _, row := range m.Keyboard {
			var r tg.KeyboardButtonRow
			for _, b := range row {
				if b.Text == "" {
					return nil, bad("keyboard button text is empty")
				}
				btn := tg.KeyboardButton{Text: b.Text, Type: &tg.ButtonTypeDefault{}}
				switch {
				case b.Managed != nil:
					btn.Type = managedButtonType(b.Managed)
				case b.RequestContact:
					btn.Type = &tg.ButtonTypeRequestPhone{}
				case b.RequestLocation:
					btn.Type = &tg.ButtonTypeRequestGeoLocation{}
				case b.WebApp != nil:
					btn.Type = &tg.ButtonTypeSimpleWebView{URL: b.WebApp.URL}
				}
				r.Buttons = append(r.Buttons, btn)
			}
			if len(r.Buttons) > 0 {
				out.Rows = append(out.Rows, r)
			}
		}
		return out, nil
	case m.Remove:
		return &tg.ReplyKeyboardHide{Selective: m.Selective}, nil
	case m.ForceReply:
		out := &tg.ReplyKeyboardForceReply{Selective: m.Selective}
		if m.Placeholder != "" {
			out.SetPlaceholder(m.Placeholder)
		}
		return out, nil
	}
	return nil, bad("can't parse reply keyboard markup JSON object")
}

// apiInlineMarkup is the inline keyboard as bots read it back; other
// keyboards aren't shown to bots, as on Telegram.
func apiInlineMarkup(m tg.ReplyMarkupClass) *apiMarkup {
	in, ok := m.(*tg.ReplyInlineMarkup)
	if !ok {
		return nil
	}
	out := &apiMarkup{InlineKeyboard: [][]apiInlineButton{}}
	for _, row := range in.Rows {
		r := []apiInlineButton{}
		for _, b := range row.Buttons {
			x := apiInlineButton{Text: b.Text}
			switch t := b.Type.(type) {
			case *tg.InlineButtonTypeCallback:
				s := string(t.Data)
				x.CallbackData = &s
			case *tg.InlineButtonTypeURL:
				x.URL = t.URL
			case *tg.InlineButtonTypeWebView:
				x.WebApp = &apiWebApp{URL: t.URL}
			case *tg.InlineButtonTypeSwitchInline:
				q := t.Query
				if t.SamePeer {
					x.SwitchInlineChat = &q
				} else {
					x.SwitchInline = &q
				}
			case *tg.InlineButtonTypeCopy:
				x.CopyText = &struct {
					Text string `json:"text"`
				}{t.CopyText}
			case *tg.InlineButtonTypeBuy:
				x.Pay = true
			case *tg.InlineButtonTypeUserProfile:
				x.URL = "tg://user?id=" + strconv.FormatInt(t.UserID, 10)
			}
			r = append(r, x)
		}
		out.InlineKeyboard = append(out.InlineKeyboard, r)
	}
	return out
}

// HasCallback is whether markup has a callback button carrying data.
func HasCallback(m tg.ReplyMarkupClass, data []byte) bool {
	in, ok := m.(*tg.ReplyInlineMarkup)
	if !ok {
		return false
	}
	for _, row := range in.Rows {
		for _, b := range row.Buttons {
			if c, ok := b.Type.(*tg.InlineButtonTypeCallback); ok && string(c.Data) == string(data) {
				return true
			}
		}
	}
	return false
}
