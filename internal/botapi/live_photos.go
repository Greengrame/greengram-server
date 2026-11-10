package botapi

import (
	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/media"
)

func (s *Server) inputLivePhoto(c *call) (media.Ref, error) {
	var values = params{}
	var nested = *c
	var key string
	var value any
	var photo, video media.Ref
	var document *tg.MessageMediaDocument
	var file *tg.Document
	var found bool
	var err error
	for key, value = range c.p {
		values[key] = value
	}
	values["video"] = c.p["live_photo"]
	nested.p = values
	photo, err = s.inputFile(c, "photo")
	if err != nil {
		return media.Ref{}, err
	}
	video, err = s.inputFile(&nested, "video")
	if err != nil {
		return media.Ref{}, err
	}
	document, found = video.TL.(*tg.MessageMediaDocument)
	if !found {
		return media.Ref{}, bad("invalid live photo video")
	}
	file, found = document.Document.(*tg.Document)
	if !found {
		return media.Ref{}, bad("invalid live photo video")
	}
	return s.Media.LivePhoto(c.ctx, photo, &tg.InputDocument{ID: file.ID, AccessHash: file.AccessHash})
}

func (s *Server) sendLivePhoto(c *call) (any, error) {
	var to chat
	var out Out
	var err error
	to, err = s.messageChat(c)
	if err != nil {
		return nil, err
	}
	err = s.messageWritable(c, to)
	if err != nil {
		return nil, err
	}
	out.Text, out.Entities, err = botCaption(c.p)
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
	out.Media, err = s.inputLivePhoto(c)
	if err != nil {
		return nil, err
	}
	return s.sendCall(c, to, out)
}
