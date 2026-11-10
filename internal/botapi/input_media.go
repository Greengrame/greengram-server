package botapi

import (
	"encoding/json"
)

func (s *Server) readInputMedia(c *call) (Out, error) {
	var out Out
	var input map[string]json.RawMessage
	var values = params{}
	var key string
	var value any
	var kind string
	var mediaValue string
	var nested = *c
	var err error
	_, err = c.p.json("media", &input)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(input["type"], &kind)
	if err != nil {
		return out, bad("media type is required")
	}
	if kind != "photo" && kind != "video" && kind != "animation" && kind != "audio" && kind != "document" && kind != "voice" {
		return out, bad("unsupported input media type")
	}
	err = json.Unmarshal(input["media"], &mediaValue)
	if err != nil || mediaValue == "" {
		return out, bad("media is required")
	}
	for key, value = range c.p {
		values[key] = value
	}
	for key = range input {
		value = nil
		err = json.Unmarshal(input[key], &value)
		if err != nil {
			return out, bad("invalid media field")
		}
		values[key] = value
	}
	values[kind] = mediaValue
	nested.p = values
	out.Text, out.Entities, err = botCaption(values)
	if err != nil {
		return out, err
	}
	out.Media, err = s.inputFile(&nested, kind)
	return out, err
}
