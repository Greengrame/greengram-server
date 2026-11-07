package botapi

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"unicode/utf16"

	"github.com/iamxvbaba/td/tg"
)

// fmtEntity is what formatted_text.go (from gramsrv) parses into; offsets
// are UTF-16, like TL and the Bot API.
type fmtEntity struct {
	Type       fmtEntityType
	Offset     int
	Length     int
	URL        string
	UserID     int64 // client id
	Language   string
	DocumentID int64
	Collapsed  bool
	Date       int
	Relative   bool
	ShortTime  bool
	LongTime   bool
	ShortDate  bool
	LongDate   bool
	DayOfWeek  bool
}

type fmtEntityType string

const (
	fmtEntityBold          fmtEntityType = "bold"
	fmtEntityItalic        fmtEntityType = "italic"
	fmtEntityUnderline     fmtEntityType = "underline"
	fmtEntityStrike        fmtEntityType = "strikethrough"
	fmtEntityCode          fmtEntityType = "code"
	fmtEntityPre           fmtEntityType = "pre"
	fmtEntityTextURL       fmtEntityType = "text_link"
	fmtEntityMentionName   fmtEntityType = "text_mention"
	fmtEntitySpoiler       fmtEntityType = "spoiler"
	fmtEntityBlockquote    fmtEntityType = "blockquote"
	fmtEntityCustomEmoji   fmtEntityType = "custom_emoji"
	fmtEntityFormattedDate fmtEntityType = "date_time"
)

const maxEntityCount = 256

// apiMessageEntity is MessageEntity as bots send and get it.
type apiMessageEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	URL    string `json:"url,omitempty"`
	User   *struct {
		ID int64 `json:"id"`
	} `json:"user,omitempty"`
	Language       string `json:"language,omitempty"`
	CustomEmojiID  string `json:"custom_emoji_id,omitempty"`
	UnixTime       int    `json:"unix_time,omitempty"`
	DateTimeFormat string `json:"date_time_format,omitempty"`
}

func botAPIMessageEntities(raw string) ([]fmtEntity, error) {
	if raw == "" {
		return nil, nil
	}
	var list []apiMessageEntity
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, errors.New("can't parse entities JSON object")
	}
	return messageEntitiesFromAPI(list)
}

// plain entity kinds the Bot API accepts on input with nothing extra
var plainKinds = map[string]bool{
	"mention": true, "hashtag": true, "cashtag": true, "bot_command": true, "url": true, "email": true, "phone_number": true,
	"bold": true, "italic": true, "underline": true, "strikethrough": true, "spoiler": true, "code": true, "pre": true,
	"blockquote": true, "expandable_blockquote": true, "text_link": true, "text_mention": true, "custom_emoji": true,
}

func messageEntitiesFromAPI(in []apiMessageEntity) ([]fmtEntity, error) {
	if len(in) > maxEntityCount {
		return nil, errors.New("too many entities")
	}
	out := make([]fmtEntity, 0, len(in))
	for _, e := range in {
		if !plainKinds[e.Type] {
			return nil, errors.New("unsupported entity type " + strconv.Quote(e.Type))
		}
		f := fmtEntity{Type: fmtEntityType(e.Type), Offset: e.Offset, Length: e.Length, Language: e.Language}
		switch e.Type {
		case "expandable_blockquote":
			f.Type, f.Collapsed = fmtEntityBlockquote, true
		case "text_link":
			if !validBotAPITextURL(e.URL) {
				return nil, errors.New("wrong text_link URL")
			}
			f.URL = e.URL
		case "text_mention":
			if e.User == nil || e.User.ID <= 0 {
				return nil, errors.New("text_mention needs a user")
			}
			f.UserID = e.User.ID
		case "custom_emoji":
			id, err := strconv.ParseInt(e.CustomEmojiID, 10, 64)
			if err != nil || id <= 0 {
				return nil, errors.New("wrong custom_emoji_id")
			}
			f.DocumentID = id
		}
		out = append(out, f)
	}
	return out, nil
}

// tgEntities turns parsed entities into TL; kinds clients detect on their
// own (mentions, links, ...) pass through as such.
func tgEntities(list []fmtEntity) []tg.MessageEntityClass {
	var out []tg.MessageEntityClass
	for _, e := range list {
		o, l := e.Offset, e.Length
		var t tg.MessageEntityClass
		switch e.Type {
		case fmtEntityBold:
			t = &tg.MessageEntityBold{Offset: o, Length: l}
		case fmtEntityItalic:
			t = &tg.MessageEntityItalic{Offset: o, Length: l}
		case fmtEntityUnderline:
			t = &tg.MessageEntityUnderline{Offset: o, Length: l}
		case fmtEntityStrike, "strike":
			t = &tg.MessageEntityStrike{Offset: o, Length: l}
		case fmtEntityCode:
			t = &tg.MessageEntityCode{Offset: o, Length: l}
		case fmtEntityPre:
			t = &tg.MessageEntityPre{Offset: o, Length: l, Language: e.Language}
		case fmtEntityTextURL, "text_url":
			t = &tg.MessageEntityTextURL{Offset: o, Length: l, URL: e.URL}
		case fmtEntityMentionName, "mention_name":
			t = &tg.MessageEntityMentionName{Offset: o, Length: l, UserID: e.UserID}
		case fmtEntitySpoiler:
			t = &tg.MessageEntitySpoiler{Offset: o, Length: l}
		case fmtEntityBlockquote:
			t = &tg.MessageEntityBlockquote{Offset: o, Length: l, Collapsed: e.Collapsed}
		case fmtEntityCustomEmoji:
			t = &tg.MessageEntityCustomEmoji{Offset: o, Length: l, DocumentID: e.DocumentID}
		case fmtEntityFormattedDate, "formatted_date":
			t = &tg.MessageEntityFormattedDate{Offset: o, Length: l, Date: e.Date, Relative: e.Relative, ShortTime: e.ShortTime,
				LongTime: e.LongTime, ShortDate: e.ShortDate, LongDate: e.LongDate, DayOfWeek: e.DayOfWeek}
		case "mention":
			t = &tg.MessageEntityMention{Offset: o, Length: l}
		case "hashtag":
			t = &tg.MessageEntityHashtag{Offset: o, Length: l}
		case "cashtag":
			t = &tg.MessageEntityCashtag{Offset: o, Length: l}
		case "bot_command":
			t = &tg.MessageEntityBotCommand{Offset: o, Length: l}
		case "url":
			t = &tg.MessageEntityURL{Offset: o, Length: l}
		case "email":
			t = &tg.MessageEntityEmail{Offset: o, Length: l}
		case "phone_number":
			t = &tg.MessageEntityPhone{Offset: o, Length: l}
		default:
			continue
		}
		out = append(out, t)
	}
	return out
}

// apiEntities is the Bot API view of TL entities.
func apiEntities(list []tg.MessageEntityClass) []apiMessageEntity {
	var out []apiMessageEntity
	for _, t := range list {
		e := apiMessageEntity{Offset: t.GetOffset(), Length: t.GetLength()}
		switch v := t.(type) {
		case *tg.MessageEntityBold:
			e.Type = "bold"
		case *tg.MessageEntityItalic:
			e.Type = "italic"
		case *tg.MessageEntityUnderline:
			e.Type = "underline"
		case *tg.MessageEntityStrike:
			e.Type = "strikethrough"
		case *tg.MessageEntitySpoiler:
			e.Type = "spoiler"
		case *tg.MessageEntityCode:
			e.Type = "code"
		case *tg.MessageEntityPre:
			e.Type, e.Language = "pre", v.Language
		case *tg.MessageEntityTextURL:
			e.Type, e.URL = "text_link", v.URL
		case *tg.MessageEntityMentionName:
			e.Type = "text_mention"
			e.User = &struct {
				ID int64 `json:"id"`
			}{v.UserID}
		case *tg.MessageEntityBlockquote:
			e.Type = "blockquote"
			if v.Collapsed {
				e.Type = "expandable_blockquote"
			}
		case *tg.MessageEntityCustomEmoji:
			e.Type, e.CustomEmojiID = "custom_emoji", strconv.FormatInt(v.DocumentID, 10)
		case *tg.MessageEntityMention:
			e.Type = "mention"
		case *tg.MessageEntityHashtag:
			e.Type = "hashtag"
		case *tg.MessageEntityCashtag:
			e.Type = "cashtag"
		case *tg.MessageEntityBotCommand:
			e.Type = "bot_command"
		case *tg.MessageEntityURL:
			e.Type = "url"
		case *tg.MessageEntityEmail:
			e.Type = "email"
		case *tg.MessageEntityPhone:
			e.Type = "phone_number"
		default:
			continue
		}
		out = append(out, e)
	}
	return out
}

var commandRe = regexp.MustCompile(`^/[A-Za-z0-9_]{1,64}(@[A-Za-z0-9_]{3,32})?`)

// withCommand adds the bot_command entity Telegram's server detects in
// "/start" and friends; bot libraries route commands by it.
func withCommand(text string, list []tg.MessageEntityClass) []tg.MessageEntityClass {
	loc := commandRe.FindStringIndex(text)
	if loc == nil {
		return list
	}
	for _, e := range list {
		if _, ok := e.(*tg.MessageEntityBotCommand); ok && e.GetOffset() == 0 {
			return list
		}
	}
	n := len(utf16.Encode([]rune(text[:loc[1]])))
	return append([]tg.MessageEntityClass{&tg.MessageEntityBotCommand{Offset: 0, Length: n}}, list...)
}
