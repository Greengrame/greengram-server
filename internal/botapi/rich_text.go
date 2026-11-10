package botapi

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/iamxvbaba/td/tg"
)

var richTextConstructors = map[string]tg.RichTextClass{
	"bold": &tg.TextBold{}, "italic": &tg.TextItalic{}, "underline": &tg.TextUnderline{},
	"strikethrough": &tg.TextStrike{}, "spoiler": &tg.TextSpoiler{}, "subscript": &tg.TextSubscript{},
	"superscript": &tg.TextSuperscript{}, "marked": &tg.TextMarked{}, "code": &tg.TextFixed{},
	"url": &tg.TextURL{}, "email_address": &tg.TextEmail{}, "phone_number": &tg.TextPhone{},
	"bank_card_number": &tg.TextBankCard{}, "ton_address": &tg.TextTonAddress{},
	"mention": &tg.TextMention{}, "hashtag": &tg.TextHashtag{}, "cashtag": &tg.TextCashtag{},
	"bot_command": &tg.TextBotCommand{}, "anchor": &tg.TextAnchor{}, "anchor_link": &tg.TextURL{},
	"custom_emoji": &tg.TextCustomEmoji{}, "mathematical_expression": &tg.TextMath{}, "text_mention": &tg.TextMentionName{},
}

type richTextInput struct {
	Type        string          `json:"type"`
	Text        json.RawMessage `json:"text"`
	URL         string          `json:"url"`
	Address     string          `json:"address"`
	Email       string          `json:"email_address"`
	Phone       string          `json:"phone_number"`
	Emoji       string          `json:"custom_emoji_id"`
	Alternative string          `json:"alternative_text"`
	Expression  string          `json:"expression"`
	Name        string          `json:"name"`
	Anchor      string          `json:"anchor_name"`
	User        struct {
		ID int64 `json:"id"`
	} `json:"user"`
}

func parseRichText(raw json.RawMessage, depth int) (tg.RichTextClass, error) {
	var plain string
	var inputs []json.RawMessage
	var input richTextInput
	var prototype tg.RichTextClass
	var result tg.RichTextClass
	var child tg.RichTextClass
	var children []tg.RichTextClass
	var item json.RawMessage
	var value reflect.Value
	var field reflect.Value
	var err error
	var id int64
	if depth > 32 || len(raw) > 512<<10 {
		return nil, bad("rich text is too complex")
	}
	if len(raw) == 0 || string(raw) == "null" {
		return &tg.TextEmpty{}, nil
	}
	if raw[0] == '"' {
		err = json.Unmarshal(raw, &plain)
		if err != nil || !utf8.ValidString(plain) || strings.ContainsRune(plain, 0) {
			return nil, bad("invalid rich text")
		}
		return &tg.TextPlain{Text: plain}, nil
	}
	if raw[0] == '[' {
		err = json.Unmarshal(raw, &inputs)
		if err != nil || len(inputs) > 500 {
			return nil, bad("invalid rich text array")
		}
		children = []tg.RichTextClass{}
		for _, item = range inputs {
			child, err = parseRichText(item, depth+1)
			if err != nil {
				return nil, err
			}
			children = append(children, child)
		}
		return &tg.TextConcat{Texts: children}, nil
	}
	err = json.Unmarshal(raw, &input)
	if err != nil {
		return nil, bad("invalid rich text object")
	}
	prototype = richTextConstructors[input.Type]
	if prototype == nil {
		return nil, bad("unsupported rich text type: " + input.Type)
	}
	result = reflect.New(reflect.TypeOf(prototype).Elem()).Interface().(tg.RichTextClass)
	value = reflect.ValueOf(result).Elem()
	field = value.FieldByName("Text")
	if field.IsValid() {
		child, err = parseRichText(input.Text, depth+1)
		if err != nil {
			return nil, err
		}
		field.Set(reflect.ValueOf(child))
	}
	switch input.Type {
	case "url":
		if !validURL(input.URL, "https", "http", "tg", "ton", "mailto", "tel") {
			return nil, bad("invalid rich text URL")
		}
		result.(*tg.TextURL).URL = input.URL
	case "email_address":
		result.(*tg.TextEmail).Email = input.Email
	case "phone_number":
		result.(*tg.TextPhone).Phone = input.Phone
	case "ton_address":
		plain = richTextPlain(child)
		if input.Address != "" && input.Address != plain {
			result = &tg.TextURL{Text: result, URL: "ton://transfer/" + input.Address}
		}
	case "anchor":
		result.(*tg.TextAnchor).Name = input.Name
	case "anchor_link":
		result.(*tg.TextURL).URL = "#" + input.Anchor
	case "custom_emoji":
		id, err = strconv.ParseInt(input.Emoji, 10, 64)
		if err != nil || id <= 0 {
			return nil, bad("invalid custom emoji")
		}
		result.(*tg.TextCustomEmoji).DocumentID = id
		result.(*tg.TextCustomEmoji).Alt = input.Alternative
	case "mathematical_expression":
		result.(*tg.TextMath).Source = input.Expression
	case "text_mention":
		if input.User.ID <= 0 {
			return nil, bad("invalid rich text mention")
		}
		result.(*tg.TextMentionName).UserID = input.User.ID
	}
	return result, nil
}

func richTextPlain(text tg.RichTextClass) string {
	var value reflect.Value
	var field reflect.Value
	var child tg.RichTextClass
	var output strings.Builder
	var item tg.RichTextClass
	switch text.(type) {
	case *tg.TextPlain:
		return text.(*tg.TextPlain).Text
	case *tg.TextCustomEmoji:
		return text.(*tg.TextCustomEmoji).Alt
	case *tg.TextMath:
		return text.(*tg.TextMath).Source
	case *tg.TextConcat:
		for _, item = range text.(*tg.TextConcat).Texts {
			output.WriteString(richTextPlain(item))
		}
		return output.String()
	}
	if text == nil {
		return ""
	}
	value = reflect.ValueOf(text).Elem()
	field = value.FieldByName("Text")
	if field.IsValid() && field.CanInterface() {
		child, _ = field.Interface().(tg.RichTextClass)
		return richTextPlain(child)
	}
	return ""
}

func (v *view) richTextJSON(text tg.RichTextClass) any {
	var output map[string]any
	var items = []any{}
	var child tg.RichTextClass
	var prototype tg.RichTextClass
	var kind string
	var found bool
	var value reflect.Value
	var field reflect.Value
	if text == nil {
		return ""
	}
	switch text.(type) {
	case *tg.TextEmpty:
		return ""
	case *tg.TextPlain:
		return text.(*tg.TextPlain).Text
	case *tg.TextConcat:
		for _, child = range text.(*tg.TextConcat).Texts {
			items = append(items, v.richTextJSON(child))
		}
		return items
	}
	for kind, prototype = range richTextConstructors {
		if reflect.TypeOf(prototype) == reflect.TypeOf(text) && kind != "anchor_link" {
			found = true
			break
		}
	}
	if !found {
		return richTextPlain(text)
	}
	output = map[string]any{"type": kind}
	value = reflect.ValueOf(text).Elem()
	field = value.FieldByName("Text")
	if field.IsValid() {
		child, _ = field.Interface().(tg.RichTextClass)
		output["text"] = v.richTextJSON(child)
	}
	switch text.(type) {
	case *tg.TextURL:
		if strings.HasPrefix(text.(*tg.TextURL).URL, "ton://transfer/") {
			if _, found = child.(*tg.TextTonAddress); found {
				return map[string]any{"type": "ton_address", "text": v.richTextJSON(child.(*tg.TextTonAddress).Text), "address": strings.TrimPrefix(text.(*tg.TextURL).URL, "ton://transfer/")}
			}
		}
		output["url"] = text.(*tg.TextURL).URL
	case *tg.TextTonAddress:
		output["address"] = richTextPlain(child)
	case *tg.TextBankCard:
		output["bank_card_number"] = richTextPlain(child)
	case *tg.TextMention:
		output["username"] = strings.TrimPrefix(richTextPlain(child), "@")
	case *tg.TextHashtag:
		output["hashtag"] = richTextPlain(child)
	case *tg.TextCashtag:
		output["cashtag"] = richTextPlain(child)
	case *tg.TextBotCommand:
		output["bot_command"] = richTextPlain(child)
	case *tg.TextEmail:
		output["email_address"] = text.(*tg.TextEmail).Email
	case *tg.TextPhone:
		output["phone_number"] = text.(*tg.TextPhone).Phone
	case *tg.TextAnchor:
		output["name"] = text.(*tg.TextAnchor).Name
	case *tg.TextCustomEmoji:
		output["custom_emoji_id"] = strconv.FormatInt(text.(*tg.TextCustomEmoji).DocumentID, 10)
		output["alternative_text"] = text.(*tg.TextCustomEmoji).Alt
	case *tg.TextMath:
		output["expression"] = text.(*tg.TextMath).Source
	case *tg.TextMentionName:
		output["user"] = v.user(ids.Server(text.(*tg.TextMentionName).UserID))
	}
	return output
}
