package media

import (
	"crypto/rand"
	"math"
	"math/big"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

// Kinds of media that are only TL, nothing stored besides the message.
const (
	KindGeo     = "geo" // points, venues and live locations: one search filter
	KindContact = "contact"
	KindDice    = "dice"
)

// DiceEmoji are the dice Telegram rolls, with the highest value each can
// land on (appConfig emojies_send_dice).
var DiceEmoji = []struct {
	Emoji string
	Max   int
}{{"🎲", 6}, {"🎯", 6}, {"🏀", 5}, {"⚽", 5}, {"🎳", 6}, {"🎰", 64}}

func roll(max int) int {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 1
	}
	return int(n.Int64()) + 1
}

func dice(emoticon string) (Ref, error) {
	// "⚽️" with the variation selector is the same ball
	e := strings.TrimSuffix(emoticon, "️")
	for _, d := range DiceEmoji {
		if d.Emoji == e {
			return Ref{Kind: KindDice, TL: &tg.MessageMediaDice{Value: roll(d.Max), Emoticon: emoticon}}, nil
		}
	}
	return Ref{}, tgerr.New(400, "EMOTICON_INVALID")
}

var errGeoInvalid = tgerr.New(400, "GEO_POINT_INVALID")

// geoPoint checks a client's point. The access hash is any number: it
// names the map tiles, which come from the client's own map provider.
func geoPoint(in tg.InputGeoPointClass) (*tg.GeoPoint, error) {
	v, ok := in.(*tg.InputGeoPoint)
	if !ok || math.IsNaN(v.Lat) || math.IsNaN(v.Long) || v.Lat < -90 || v.Lat > 90 || v.Long < -180 || v.Long > 180 {
		return nil, errGeoInvalid
	}
	g := &tg.GeoPoint{Lat: v.Lat, Long: v.Long, AccessHash: newID()}
	if r, ok := v.GetAccuracyRadius(); ok && r > 0 {
		g.SetAccuracyRadius(min(r, 1500))
	}
	return g, nil
}

func geo(in *tg.InputMediaGeoPoint) (Ref, error) {
	g, err := geoPoint(in.GeoPoint)
	if err != nil {
		return Ref{}, err
	}
	return Ref{Kind: KindGeo, TL: &tg.MessageMediaGeo{Geo: g}}, nil
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func venue(in *tg.InputMediaVenue) (Ref, error) {
	g, err := geoPoint(in.GeoPoint)
	if err != nil {
		return Ref{}, err
	}
	if strings.TrimSpace(in.Title) == "" {
		return Ref{}, tgerr.New(400, "VENUE_TITLE_INVALID")
	}
	return Ref{Kind: KindGeo, TL: &tg.MessageMediaVenue{Geo: g, Title: clip(in.Title, 128), Address: clip(in.Address, 256),
		Provider: clip(in.Provider, 32), VenueID: clip(in.VenueID, 128), VenueType: clip(in.VenueType, 64)}}, nil
}

// Live locations run 1 minute to a day, or until stopped (0x7fffffff).
const livePeriodForever = math.MaxInt32

func liveGeo(in *tg.InputMediaGeoLive) (Ref, error) {
	if in.Stopped {
		return Ref{}, tgerr.New(400, "MEDIA_INVALID")
	}
	g, err := geoPoint(in.GeoPoint)
	if err != nil {
		return Ref{}, err
	}
	period, _ := in.GetPeriod()
	if period != livePeriodForever && (period < 60 || period > 86400) {
		return Ref{}, tgerr.New(400, "PERIOD_INVALID")
	}
	m := &tg.MessageMediaGeoLive{Geo: g, Period: period}
	if h, ok := in.GetHeading(); ok && h >= 1 && h <= 360 {
		m.SetHeading(h)
	}
	if r, ok := in.GetProximityNotificationRadius(); ok && r > 0 {
		m.SetProximityNotificationRadius(r)
	}
	return Ref{Kind: KindGeo, TL: m}, nil
}

// MoveLive is a live location's edit: a new point, or stopped, which
// ends it at once (the period is cut to the time it ran).
func MoveLive(old *tg.MessageMediaGeoLive, sent, now int, in *tg.InputMediaGeoLive) (Ref, error) {
	out := &tg.MessageMediaGeoLive{Geo: old.Geo, Heading: old.Heading, Period: old.Period, ProximityNotificationRadius: old.ProximityNotificationRadius}
	if sent+old.Period <= now && old.Period != livePeriodForever {
		return Ref{}, tgerr.New(400, "MESSAGE_NOT_MODIFIED")
	}
	if in.Stopped {
		out.Period = max(now-sent, 1)
		out.Heading, out.ProximityNotificationRadius = 0, 0
		return Ref{Kind: KindGeo, TL: out}, nil
	}
	if in.GeoPoint != nil {
		g, err := geoPoint(in.GeoPoint)
		if err != nil {
			return Ref{}, err
		}
		g.AccessHash = 0
		if og, ok := old.Geo.(*tg.GeoPoint); ok {
			g.AccessHash = og.AccessHash
		}
		out.Geo = g
	}
	if h, ok := in.GetHeading(); ok {
		out.Heading = h
	}
	if p, ok := in.GetPeriod(); ok && p > old.Period {
		out.Period = p // extending a running live location
	}
	if r, ok := in.GetProximityNotificationRadius(); ok {
		out.ProximityNotificationRadius = r
	}
	return Ref{Kind: KindGeo, TL: out}, nil
}

// contact is a shared contact card; the caller links it to an account
// with that phone, if any.
func contact(in *tg.InputMediaContact) (Ref, error) {
	if strings.TrimSpace(in.PhoneNumber) == "" && strings.TrimSpace(in.Vcard) == "" {
		return Ref{}, tgerr.New(400, "MEDIA_INVALID")
	}
	if strings.TrimSpace(in.FirstName) == "" {
		return Ref{}, tgerr.New(400, "CONTACT_NAME_EMPTY")
	}
	return Ref{Kind: KindContact, TL: &tg.MessageMediaContact{PhoneNumber: clip(in.PhoneNumber, 32), FirstName: clip(in.FirstName, 64),
		LastName: clip(in.LastName, 64), Vcard: clip(in.Vcard, 4096)}}, nil
}
