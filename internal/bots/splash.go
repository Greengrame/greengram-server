package bots

import (
	"encoding/xml"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/iamxvbaba/td/tg"
)

// Splash is the launch screen: colors while a mini app loads and the icon on them.
type Splash struct {
	Background     string `json:"bg_color,omitempty"`
	BackgroundDark string `json:"bg_dark_color,omitempty"`
	Header         string `json:"header_color,omitempty"`
	HeaderDark     string `json:"header_dark_color,omitempty"`
	Path           string `json:"placeholder_path,omitempty"`
}

// pathChars is the alphabet of Telegram's packed vector path: a byte >= 192 is
// the character at that index. The client puts M in front and z behind.
const pathChars = "AACAAAAHAAALMAAAQASTAVAAAZaacaaaahaaalmaaaqastava.az0123456789-,"

var pathRe = regexp.MustCompile(`^[ACHLMQSTVZachlmqstvz0-9.,-]*$`)

// packPath turns d, which starts at the implied M, into the bytes on the wire.
func packPath(d string) []byte {
	out := make([]byte, 0, len(d))
	for _, c := range []byte(d) {
		if i := strings.IndexByte(pathChars, c); i >= 0 {
			out = append(out, byte(192+i))
		}
	}
	return out
}

var (
	pathGaps  = regexp.MustCompile(`[\s,]+`)
	pathLeads = regexp.MustCompile(`([A-Za-z]),`)
	pathTails = regexp.MustCompile(`,([A-Za-z])`)
)

// tidyPath writes d with single commas between numbers only: a comma beside a
// command letter ends the path in a browser, and bytes are short here.
func tidyPath(d string) string {
	d = strings.Trim(pathGaps.ReplaceAllString(d, ","), ",")
	d = pathLeads.ReplaceAllString(d, "$1")
	d = pathTails.ReplaceAllString(d, "$1")
	return strings.ReplaceAll(d, ",-", "-")
}

// IconPath reads a launch screen icon: one or more plain paths on a 512x512
// canvas, joined into the single path Telegram's format has. A transform or a
// shape other than a path can't be packed, so it's refused rather than drawn wrong.
func IconPath(svg io.Reader) (string, error) {
	bad := refuse("Use a 512x512 SVG made of plain paths, with no transforms.")
	dec := xml.NewDecoder(svg)
	var parts []string
	sized := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", bad
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		attr := func(name string) string {
			for _, a := range el.Attr {
				if a.Name.Local == name {
					return a.Value
				}
			}
			return ""
		}
		if attr("transform") != "" {
			return "", bad
		}
		switch el.Name.Local {
		case "svg":
			box := strings.Join(strings.Fields(attr("viewBox")), " ")
			sized = box == "0 0 512 512" || box == "" && strings.TrimSuffix(attr("width"), "px") == "512" && strings.TrimSuffix(attr("height"), "px") == "512"
		case "path":
			parts = append(parts, tidyPath(attr("d")))
		case "defs", "g", "title", "desc", "metadata":
		default:
			return "", bad
		}
	}
	if !sized || len(parts) == 0 {
		return "", bad
	}
	joined := strings.Join(parts, "")
	if len(joined) < 2 || joined[0] != 'M' && joined[0] != 'm' {
		return "", bad
	}
	joined = strings.TrimRight(joined[1:], "zZ")
	if !ValidPath(joined) {
		return "", bad
	}
	return joined, nil
}

// ValidPath is for what the page posts back: only what IconPath can make.
func ValidPath(d string) bool {
	return len(d) <= 64<<10 && pathRe.MatchString(d)
}

func hexColor(c string) (int, bool) {
	if len(c) != 7 || c[0] != '#' {
		return 0, false
	}
	n, err := strconv.ParseInt(c[1:], 16, 32)
	return int(n), err == nil
}

// ValidColor is "" for the theme's own or #rrggbb.
func ValidColor(c string) bool {
	if c == "" {
		return true
	}
	_, ok := hexColor(c)
	return ok
}

// AppSettings is the launch screen as bot_info carries it; false when unset.
func (s Splash) AppSettings() (tg.BotAppSettings, bool) {
	var out tg.BotAppSettings
	if s == (Splash{}) {
		return out, false
	}
	if n, ok := hexColor(s.Background); ok {
		out.SetBackgroundColor(n)
	}
	if n, ok := hexColor(s.BackgroundDark); ok {
		out.SetBackgroundDarkColor(n)
	}
	if n, ok := hexColor(s.Header); ok {
		out.SetHeaderColor(n)
	}
	if n, ok := hexColor(s.HeaderDark); ok {
		out.SetHeaderDarkColor(n)
	}
	if s.Path != "" {
		out.SetPlaceholderPath(packPath(s.Path))
	}
	return out, true
}
