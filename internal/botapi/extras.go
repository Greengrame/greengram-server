package botapi

import (
	"encoding/json"
	"math"
	"strconv"

	"github.com/iamxvbaba/td/tg"
)

func (s *Server) sendInput(c *call, input tg.InputMediaClass) (any, error) {
	var to chat
	var err error
	var out Out
	if s.Media == nil {
		return nil, bad("media service is unavailable")
	}
	to, err = s.messageChat(c)
	if err != nil {
		return nil, err
	}
	err = s.messageWritable(c, to)
	if err != nil {
		return nil, err
	}
	out.Markup, _, err = markup(c.p)
	if err != nil {
		return nil, err
	}
	out.ReplyTo, err = replyTo(c.p)
	if err != nil {
		return nil, err
	}
	out.Media, err = s.Media.FromInput(c.ctx, c.bot.UserID, input)
	if err != nil {
		return nil, err
	}
	return s.sendCall(c, to, out)
}

func (s *Server) sendContact(c *call) (any, error) {
	return s.sendInput(c, &tg.InputMediaContact{PhoneNumber: c.p.str("phone_number"), FirstName: c.p.str("first_name"), LastName: c.p.str("last_name"), Vcard: c.p.str("vcard")})
}

func botGeo(p params) (*tg.InputGeoPoint, error) {
	var lat, long float64
	var err error
	lat, err = strconv.ParseFloat(p.str("latitude"), 64)
	if err != nil || math.IsNaN(lat) || math.IsInf(lat, 0) || lat < -90 || lat > 90 {
		return nil, bad("invalid latitude")
	}
	long, err = strconv.ParseFloat(p.str("longitude"), 64)
	if err != nil || math.IsNaN(long) || math.IsInf(long, 0) || long < -180 || long > 180 {
		return nil, bad("invalid longitude")
	}
	return &tg.InputGeoPoint{Lat: lat, Long: long}, nil
}

func (s *Server) sendLocation(c *call) (any, error) {
	var point *tg.InputGeoPoint
	var err error
	if c.p.has("live_period") {
		return nil, bad("live locations are not supported by this method yet")
	}
	point, err = botGeo(c.p)
	if err != nil {
		return nil, err
	}
	return s.sendInput(c, &tg.InputMediaGeoPoint{GeoPoint: point})
}

func (s *Server) sendVenue(c *call) (any, error) {
	var point *tg.InputGeoPoint
	var err error
	var input *tg.InputMediaVenue
	point, err = botGeo(c.p)
	if err != nil {
		return nil, err
	}
	if c.p.str("title") == "" || c.p.str("address") == "" {
		return nil, bad("venue title and address are required")
	}
	input = &tg.InputMediaVenue{GeoPoint: point, Title: c.p.str("title"), Address: c.p.str("address")}
	if c.p.has("foursquare_id") {
		input.Provider, input.VenueID, input.VenueType = "foursquare", c.p.str("foursquare_id"), c.p.str("foursquare_type")
	}
	if c.p.has("google_place_id") {
		input.Provider, input.VenueID, input.VenueType = "gplaces", c.p.str("google_place_id"), c.p.str("google_place_type")
	}
	return s.sendInput(c, input)
}

func (s *Server) sendDice(c *call) (any, error) {
	var emoji = c.p.str("emoji")
	if emoji == "" {
		emoji = "🎲"
	}
	return s.sendInput(c, &tg.InputMediaDice{Emoticon: emoji})
}

func validateMessageParameters(p params) error {
	var input struct {
		HTML     string            `json:"html"`
		Markdown string            `json:"markdown"`
		Blocks   []json.RawMessage `json:"blocks"`
	}
	var reply struct {
		MessageID int               `json:"message_id"`
		RichTexts []json.RawMessage `json:"quote_rich_texts"`
	}
	var present bool
	var formats int
	var err error
	present, err = p.json("rich_message", &input)
	if err != nil {
		return err
	}
	if present {
		if input.HTML != "" {
			formats++
		}
		if input.Markdown != "" {
			formats++
		}
		if len(input.Blocks) != 0 {
			formats++
		}
		if formats != 1 {
			return bad("specify exactly one rich message format")
		}
	}
	_, err = p.json("reply_parameters", &reply)
	if err != nil {
		return err
	}
	if len(reply.RichTexts) != 0 && reply.MessageID <= 0 {
		return bad("a rich quote requires message_id")
	}
	return nil
}
