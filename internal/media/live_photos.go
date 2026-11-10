package media

import (
	"context"
	"math"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

func (s *Service) LivePhoto(ctx context.Context, photo Ref, input tg.InputDocumentClass) (Ref, error) {
	var document Document
	var picture *tg.MessageMediaPhoto
	var result tg.MessageMediaPhoto
	var attribute tg.DocumentAttributeClass
	var video *tg.DocumentAttributeVideo
	var found bool
	var err error
	picture, found = photo.TL.(*tg.MessageMediaPhoto)
	if !found {
		return Ref{}, errMediaInvalid
	}
	document, err = s.InputDocument(ctx, input)
	if err != nil {
		return Ref{}, err
	}
	if document.Size > 10<<20 || !strings.HasPrefix(document.Mime, "video/") {
		return Ref{}, tgerr.New(400, "VIDEO_CONTENT_TYPE_INVALID")
	}
	for _, attribute = range document.Attrs {
		video, found = attribute.(*tg.DocumentAttributeVideo)
		if found && (video.Duration < 0 || video.Duration > 10 || math.IsNaN(video.Duration) || math.IsInf(video.Duration, 0)) {
			return Ref{}, tgerr.New(400, "VIDEO_DURATION_INVALID")
		}
	}
	result = tg.MessageMediaPhoto{Spoiler: picture.Spoiler, Photo: picture.Photo, LivePhoto: true, Video: s.DocumentTL(document)}
	return Ref{Kind: photo.Kind, ID: photo.ID, TL: &result}, nil
}
