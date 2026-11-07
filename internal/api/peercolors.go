package api

import (
	_ "embed"
	"encoding/json"

	"github.com/iamxvbaba/td/tg"
)

// Telegram's name and profile color palettes, as help.getPeerColors and
// help.getPeerProfileColors return them (data from gramsrv's appearance
// seed, see third_party/gramsrv/NOTICE). Web A indexes this list by every
// user's color id while rendering a message, so an empty list crashes the
// bubble and new messages don't show until the history is refetched.
//
//go:embed peercolors.json
var peerColorsJSON []byte

type colorOption struct {
	ID              int       `json:"id"`
	Hidden          bool      `json:"hidden"`
	ChannelMinLevel int       `json:"channel_min_level"`
	GroupMinLevel   int       `json:"group_min_level"`
	Colors          *colorSet `json:"colors"`
	DarkColors      *colorSet `json:"dark_colors"`
}

type colorSet struct {
	Colors        []int `json:"colors"`
	PaletteColors []int `json:"palette_colors"`
	BgColors      []int `json:"bg_colors"`
	StoryColors   []int `json:"story_colors"`
}

var peerColors, peerProfileColors = loadPeerColors()

func loadPeerColors() (*tg.HelpPeerColors, *tg.HelpPeerColors) {
	var c struct {
		Peer    []colorOption `json:"peer_colors"`
		Profile []colorOption `json:"peer_profile_colors"`
	}
	if err := json.Unmarshal(peerColorsJSON, &c); err != nil {
		panic(err)
	}
	return &tg.HelpPeerColors{Hash: 1, Colors: colorOptions(c.Peer, false)},
		&tg.HelpPeerColors{Hash: 1, Colors: colorOptions(c.Profile, true)}
}

func colorOptions(in []colorOption, profile bool) []tg.HelpPeerColorOption {
	out := make([]tg.HelpPeerColorOption, 0, len(in))
	for _, c := range in {
		o := tg.HelpPeerColorOption{ColorID: c.ID}
		o.SetHidden(c.Hidden)
		if c.ChannelMinLevel > 0 {
			o.SetChannelMinLevel(c.ChannelMinLevel)
		}
		if c.GroupMinLevel > 0 {
			o.SetGroupMinLevel(c.GroupMinLevel)
		}
		if s := c.Colors.tl(profile); s != nil {
			o.SetColors(s)
		}
		if s := c.DarkColors.tl(profile); s != nil {
			o.SetDarkColors(s)
		}
		out = append(out, o)
	}
	return out
}

func (s *colorSet) tl(profile bool) tg.HelpPeerColorSetClass {
	if s == nil {
		return nil
	}
	if profile {
		return &tg.HelpPeerColorProfileSet{PaletteColors: s.PaletteColors, BgColors: s.BgColors, StoryColors: s.StoryColors}
	}
	return &tg.HelpPeerColorSet{Colors: s.Colors}
}

// peerColorsFor answers a getPeerColors call; a client that already has
// this list sends its hash back.
func peerColorsFor(set *tg.HelpPeerColors, hash int) tg.HelpPeerColorsClass {
	if hash == set.Hash {
		return &tg.HelpPeerColorsNotModified{}
	}
	return set
}
