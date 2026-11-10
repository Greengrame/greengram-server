package botapi

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/media"
)

func TestFileIdentifiers(t *testing.T) {
	var bot = bots.Bot{UserID: 10, WebAppKey: []byte("first-secret")}
	var other = bots.Bot{UserID: 11, WebAppKey: []byte("second-secret")}
	var file = fileID{Kind: "photo", ID: 123, Hash: 456, Size: "x"}
	var value = encodeFileID(bot, file)
	var decoded fileID
	var err error
	decoded, err = decodeFileID(bot, value)
	if err != nil || decoded != file {
		t.Fatalf("roundtrip: %+v %v", decoded, err)
	}
	_, err = decodeFileID(other, value)
	if err == nil {
		t.Fatal("another bot accepted a file ID")
	}
	bot.WebAppKey = []byte("rotated-secret")
	_, err = decodeFileID(bot, value)
	if err == nil {
		t.Fatal("a rotated token accepted an old file ID")
	}
	if fileUniqueID(file) != fileUniqueID(decoded) {
		t.Fatal("unique ID changed")
	}
}

func TestMediaResponses(t *testing.T) {
	var v = (&Server{}).view(nil, bots.Bot{UserID: 10, WebAppKey: []byte("secret")})
	var message = &apiMessage{Text: "caption", Entities: []apiMessageEntity{{Type: "bold", Length: 7}}}
	var raw []byte
	var err error
	var body map[string]any
	v.mediaBody(message, media.Ref{Kind: media.KindFile, TL: &tg.MessageMediaDocument{Document: &tg.Document{ID: 1, AccessHash: 2, Size: 3, MimeType: "text/plain", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "sample.txt"}}}}})
	raw, err = json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	err = json.Unmarshal(raw, &body)
	if err != nil {
		t.Fatal(err)
	}
	if body["text"] != nil || body["caption"] != "caption" || body["document"] == nil || len(message.CaptionEntities) != 1 {
		t.Fatalf("document response: %s", raw)
	}
	if message.Document["file_name"] != "sample.txt" {
		t.Fatalf("document: %v", message.Document)
	}
	message = &apiMessage{}
	v.mediaBody(message, media.Ref{Kind: media.KindDice, TL: &tg.MessageMediaDice{Emoticon: "🎲", Value: 4}})
	if message.Dice["value"] != 4 {
		t.Fatalf("dice: %v", message.Dice)
	}
	message = &apiMessage{Text: "https://example.com"}
	v.mediaBody(message, media.Ref{Kind: media.KindWebPage, TL: &tg.MessageMediaWebPage{}})
	if message.Text != "https://example.com" || message.Caption != "" {
		t.Fatalf("link preview changed text: %+v", message)
	}
}

func TestNovemberEntities(t *testing.T) {
	var input = []apiMessageEntity{{Type: "ton_address", Offset: 0, Length: 48}, {Type: "bank_card_number", Offset: 49, Length: 16}}
	var parsed []fmtEntity
	var err error
	var output []apiMessageEntity
	parsed, err = messageEntitiesFromAPI(input)
	if err != nil {
		t.Fatal(err)
	}
	output = apiEntities(tgEntities(parsed))
	if !reflect.DeepEqual(input, output) {
		t.Fatalf("entities: %+v", output)
	}
}

func TestUnsupportedPrivateMessageParameters(t *testing.T) {
	var cases = []params{
		{"rich_message": map[string]any{"text": "private"}},
		{"reply_parameters": map[string]any{"quote_rich_texts": []any{map[string]any{"type": "ton_address", "text": "test"}}}},
	}
	var input params
	for _, input = range cases {
		if validateMessageParameters(input) == nil {
			t.Fatalf("accepted unsupported parameters: %v", input)
		}
	}
	if validateMessageParameters(params{"text": "hello"}) != nil {
		t.Fatal("ordinary message rejected")
	}
}

func TestGeoValidation(t *testing.T) {
	var cases = []params{{"latitude": "NaN", "longitude": "1"}, {"latitude": "1", "longitude": "+Inf"}, {"latitude": "91", "longitude": "1"}, {"longitude": "1"}}
	var input params
	var point *tg.InputGeoPoint
	var err error
	for _, input = range cases {
		_, err = botGeo(input)
		if err == nil {
			t.Fatalf("accepted coordinates: %v", input)
		}
	}
	point, err = botGeo(params{"latitude": "59.33", "longitude": "18.06"})
	if err != nil || point.Lat != 59.33 || point.Long != 18.06 {
		t.Fatalf("coordinates: %v %v", point, err)
	}
}
