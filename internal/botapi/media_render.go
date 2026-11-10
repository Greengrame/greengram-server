package botapi

import (
	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/media"
)

func (v *view) photoSize(id, hash int64, size media.Size) map[string]any {
	var file = fileID{Kind: "photo", ID: id, Hash: hash, Size: size.Type}
	return map[string]any{"file_id": encodeFileID(v.bot, file), "file_unique_id": fileUniqueID(file), "width": size.W, "height": size.H, "file_size": size.Bytes}
}

func (v *view) document(d *tg.Document) (string, map[string]any) {
	var file = fileID{Kind: "document", ID: d.ID, Hash: d.AccessHash}
	var out = map[string]any{"file_id": encodeFileID(v.bot, file), "file_unique_id": fileUniqueID(file), "file_size": d.Size, "mime_type": d.MimeType}
	var kind = "document"
	var attr tg.DocumentAttributeClass
	var video *tg.DocumentAttributeVideo
	var audio *tg.DocumentAttributeAudio
	var sticker *tg.DocumentAttributeSticker
	var filename *tg.DocumentAttributeFilename
	var dimensions *tg.DocumentAttributeImageSize
	var animated bool
	var exists bool
	for _, attr = range d.Attributes {
		switch attr.(type) {
		case *tg.DocumentAttributeFilename:
			filename = attr.(*tg.DocumentAttributeFilename)
			out["file_name"] = filename.FileName
		case *tg.DocumentAttributeVideo:
			video = attr.(*tg.DocumentAttributeVideo)
			kind = "video"
			out["duration"], out["width"], out["height"] = int(video.Duration), video.W, video.H
			if video.RoundMessage {
				kind = "video_note"
				out["length"] = video.W
			}
		case *tg.DocumentAttributeAudio:
			audio = attr.(*tg.DocumentAttributeAudio)
			kind = "audio"
			out["duration"] = audio.Duration
			if audio.Title != "" {
				out["title"] = audio.Title
			}
			if audio.Performer != "" {
				out["performer"] = audio.Performer
			}
			if audio.Voice {
				kind = "voice"
			}
		case *tg.DocumentAttributeSticker:
			sticker = attr.(*tg.DocumentAttributeSticker)
		case *tg.DocumentAttributeImageSize:
			dimensions = attr.(*tg.DocumentAttributeImageSize)
			out["width"], out["height"] = dimensions.W, dimensions.H
		case *tg.DocumentAttributeAnimated:
			animated = true
		}
	}
	if animated {
		kind = "animation"
	}
	if sticker != nil {
		kind = "sticker"
		out["type"], out["emoji"] = "regular", sticker.Alt
		out["is_animated"], out["is_video"] = d.MimeType == "application/x-tgsticker", d.MimeType == "video/webm"
		if _, exists = out["width"]; !exists {
			out["width"], out["height"] = 512, 512
		}
	}
	return kind, out
}

func (v *view) mediaBody(out *apiMessage, ref media.Ref) {
	var key = media.WidgetKey(ref)
	var message bots.WidgetMessage
	var widget apiWidget
	var photo *tg.MessageMediaPhoto
	var document *tg.MessageMediaDocument
	var contact *tg.MessageMediaContact
	var dice *tg.MessageMediaDice
	var geo *tg.MessageMediaGeo
	var venue *tg.MessageMediaVenue
	var point *tg.GeoPoint
	var p media.Photo
	var size media.Size
	var found bool
	var err error
	var d *tg.Document
	var field string
	var content map[string]any
	var value *tg.Photo
	if key != "" {
		message, err = v.s.Bots.WidgetMessage(v.ctx, key, 0, v.bot.UserID)
		if err == nil {
			widget = widgetJSON(message)
			out.Widget, out.HasProtectedContent = &widget, true
			out.Caption, out.CaptionEntities = out.Text, out.Entities
			out.Text, out.Entities = "", nil
		}
		return
	}
	if ref.Empty() {
		return
	}
	switch ref.TL.(type) {
	case *tg.MessageMediaPhoto, *tg.MessageMediaDocument, *tg.MessageMediaContact, *tg.MessageMediaDice, *tg.MessageMediaGeo, *tg.MessageMediaVenue:
	default:
		return
	}
	out.Caption, out.CaptionEntities = out.Text, out.Entities
	out.Text, out.Entities = "", nil
	switch ref.TL.(type) {
	case *tg.MessageMediaPhoto:
		photo = ref.TL.(*tg.MessageMediaPhoto)
		if v.s.Media == nil {
			return
		}
		if value, found = photo.Photo.(*tg.Photo); found {
			p, found, err = v.s.Media.Photo(v.ctx, value.ID)
			if err == nil && found {
				for _, size = range p.Sizes {
					out.Photo = append(out.Photo, v.photoSize(p.ID, p.AccessHash, size))
				}
			}
		}
		if photo.LivePhoto {
			d, found = photo.Video.(*tg.Document)
			if found {
				_, content = v.document(d)
				content["photo"] = out.Photo
				out.LivePhoto = content
				out.Photo = nil
			}
		}
	case *tg.MessageMediaDocument:
		document = ref.TL.(*tg.MessageMediaDocument)
		d, found = document.Document.(*tg.Document)
		if !found {
			return
		}
		field, content = v.document(d)
		switch field {
		case "video":
			out.Video = content
		case "video_note":
			out.VideoNote = content
		case "audio":
			out.Audio = content
		case "voice":
			out.Voice = content
		case "animation":
			out.Animation, out.Document = content, content
		case "sticker":
			out.Sticker = content
		default:
			out.Document = content
		}
	case *tg.MessageMediaContact:
		contact = ref.TL.(*tg.MessageMediaContact)
		out.Contact = map[string]any{"phone_number": contact.PhoneNumber, "first_name": contact.FirstName, "last_name": contact.LastName, "vcard": contact.Vcard}
	case *tg.MessageMediaDice:
		dice = ref.TL.(*tg.MessageMediaDice)
		out.Dice = map[string]any{"emoji": dice.Emoticon, "value": dice.Value}
	case *tg.MessageMediaGeo:
		geo = ref.TL.(*tg.MessageMediaGeo)
		point, found = geo.Geo.(*tg.GeoPoint)
		if found {
			out.Location = map[string]any{"latitude": point.Lat, "longitude": point.Long}
		}
	case *tg.MessageMediaVenue:
		venue = ref.TL.(*tg.MessageMediaVenue)
		point, found = venue.Geo.(*tg.GeoPoint)
		if found {
			out.Location = map[string]any{"latitude": point.Lat, "longitude": point.Long}
			out.Venue = map[string]any{"location": out.Location, "title": venue.Title, "address": venue.Address}
			if venue.Provider == "foursquare" {
				out.Venue["foursquare_id"], out.Venue["foursquare_type"] = venue.VenueID, venue.VenueType
			}
			if venue.Provider == "gplaces" {
				out.Venue["google_place_id"], out.Venue["google_place_type"] = venue.VenueID, venue.VenueType
			}
		}
	}
}
