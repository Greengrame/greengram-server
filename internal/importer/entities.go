package importer

import (
	"encoding/json"
	"strconv"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

// entity is one item of message_meta.entities: Telegram Web A's
// ApiMessageEntity, which the web server stored as the client sent it.
// Offsets are UTF-16 units on both sides.
type entity struct {
	Type       string `json:"type"`
	Offset     int    `json:"offset"`
	Length     int    `json:"length"`
	URL        string `json:"url"`
	Language   string `json:"language"`
	UserID     idText `json:"userId"`
	DocumentID idText `json:"documentId"`
	Collapsed  bool   `json:"canCollapse"`
	Date       int    `json:"date"`
	Relative   bool   `json:"relative"`
	ShortTime  bool   `json:"shortTime"`
	LongTime   bool   `json:"longTime"`
	ShortDate  bool   `json:"shortDate"`
	LongDate   bool   `json:"longDate"`
	DayOfWeek  bool   `json:"dayOfWeek"`
}

// idText takes an id written either as a number or as a string.
type idText int64

func (v *idText) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		n, _ := strconv.ParseInt(s, 10, 64)
		*v = idText(n)
		return nil
	}
	var n int64
	json.Unmarshal(b, &n)
	*v = idText(n)
	return nil
}

// convertEntities turns the stored JSON into the TL vector the messages
// tables keep. Unknown or broken items are dropped (and counted), the text
// still comes through.
func convertEntities(raw string) (enc []byte, dropped int) {
	if raw == "" || raw == "[]" {
		return nil, 0
	}
	var list []entity
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, 1
	}
	var out []tg.MessageEntityClass
	for _, e := range list {
		if e.Offset < 0 || e.Length < 1 {
			dropped++
			continue
		}
		o, l := e.Offset, e.Length
		var x tg.MessageEntityClass
		switch e.Type {
		case "MessageEntityBold":
			x = &tg.MessageEntityBold{Offset: o, Length: l}
		case "MessageEntityItalic":
			x = &tg.MessageEntityItalic{Offset: o, Length: l}
		case "MessageEntityUnderline":
			x = &tg.MessageEntityUnderline{Offset: o, Length: l}
		case "MessageEntityStrike":
			x = &tg.MessageEntityStrike{Offset: o, Length: l}
		case "MessageEntityCode":
			x = &tg.MessageEntityCode{Offset: o, Length: l}
		case "MessageEntityPre":
			x = &tg.MessageEntityPre{Offset: o, Length: l, Language: e.Language}
		case "MessageEntityTextUrl":
			x = &tg.MessageEntityTextURL{Offset: o, Length: l, URL: e.URL}
		case "MessageEntitySpoiler":
			x = &tg.MessageEntitySpoiler{Offset: o, Length: l}
		case "MessageEntityBlockquote":
			x = &tg.MessageEntityBlockquote{Offset: o, Length: l, Collapsed: e.Collapsed}
		case "MessageEntityUrl":
			x = &tg.MessageEntityURL{Offset: o, Length: l}
		case "MessageEntityMention":
			x = &tg.MessageEntityMention{Offset: o, Length: l}
		case "MessageEntityMentionName":
			x = &tg.MessageEntityMentionName{Offset: o, Length: l, UserID: int64(e.UserID)}
		case "MessageEntityHashtag":
			x = &tg.MessageEntityHashtag{Offset: o, Length: l}
		case "MessageEntityCashtag":
			x = &tg.MessageEntityCashtag{Offset: o, Length: l}
		case "MessageEntityEmail":
			x = &tg.MessageEntityEmail{Offset: o, Length: l}
		case "MessageEntityPhone":
			x = &tg.MessageEntityPhone{Offset: o, Length: l}
		case "MessageEntityBotCommand":
			x = &tg.MessageEntityBotCommand{Offset: o, Length: l}
		case "MessageEntityCustomEmoji":
			// document ids stay the web's (sticker id + 4e9); the media
			// import has to hand out the same ones
			x = &tg.MessageEntityCustomEmoji{Offset: o, Length: l, DocumentID: int64(e.DocumentID)}
		case "MessageEntityFormattedDate":
			x = &tg.MessageEntityFormattedDate{Offset: o, Length: l, Date: e.Date, Relative: e.Relative,
				ShortTime: e.ShortTime, LongTime: e.LongTime, ShortDate: e.ShortDate, LongDate: e.LongDate, DayOfWeek: e.DayOfWeek}
		default:
			dropped++
			continue
		}
		out = append(out, x)
	}
	if len(out) == 0 {
		return nil, dropped
	}
	var b bin.Buffer
	b.PutVectorHeader(len(out))
	for _, x := range out {
		if err := x.Encode(&b); err != nil {
			return nil, len(list)
		}
	}
	return b.Buf, dropped
}
