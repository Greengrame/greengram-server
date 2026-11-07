package reports

import (
	"strings"

	"github.com/iamxvbaba/td/tg"
)

// Reason is one entry of the menu messages.report and stories.report walk
// through, Telegram's tree as of 2024: a top level, sub-options under most,
// then a comment.
type Reason struct {
	Key   string
	Text  string
	Title string // the question over Children
	// Other's comment can't be skipped: it is the report
	CommentRequired bool
	Children        []Reason
}

// Root is the first menu.
var Root = Reason{Title: "Why are you reporting this?", Children: []Reason{
	{Key: "spam", Text: "Spam or scam", Title: "What kind of spam or scam?", Children: []Reason{
		{Key: "spam", Text: "Spam"},
		{Key: "phishing", Text: "Phishing"},
		{Key: "impersonation", Text: "Impersonation"},
		{Key: "fraud", Text: "Fraudulent sales"},
	}},
	{Key: "violence", Text: "Violence", Title: "What kind of violence?", Children: []Reason{
		{Key: "insults", Text: "Insults or false information"},
		{Key: "graphic", Text: "Graphic or disturbing content"},
		{Key: "extreme", Text: "Extreme violence, dismemberment"},
		{Key: "hate", Text: "Hate speech or symbols"},
		{Key: "calls", Text: "Calling for violence"},
		{Key: "crime", Text: "Organized crime"},
		{Key: "terrorism", Text: "Terrorism"},
		{Key: "animals", Text: "Animal abuse"},
	}},
	{Key: "child_abuse", Text: "Child abuse", Title: "What kind of child abuse?", Children: []Reason{
		{Key: "sexual", Text: "Child sexual abuse"},
		{Key: "physical", Text: "Child physical abuse"},
	}},
	{Key: "illegal_goods", Text: "Illegal goods and services", Title: "What is being offered?", Children: []Reason{
		{Key: "weapons", Text: "Weapons"},
		{Key: "drugs", Text: "Drugs"},
		{Key: "documents", Text: "Fake documents"},
		{Key: "money", Text: "Counterfeit money"},
		{Key: "hacking", Text: "Hacking tools and malware"},
		{Key: "merchandise", Text: "Counterfeit merchandise"},
		{Key: "other", Text: "Other goods and services"},
	}},
	{Key: "adult", Text: "Illegal adult content", Title: "What kind of adult content?", Children: []Reason{
		{Key: "nonconsensual", Text: "Non-consensual sexual imagery"},
		{Key: "porn", Text: "Pornography"},
		{Key: "services", Text: "Illegal sexual services"},
		{Key: "other", Text: "Other illegal sexual content"},
	}},
	{Key: "personal_data", Text: "Personal data", Title: "What personal data is shared?", Children: []Reason{
		{Key: "identity", Text: "Identity theft"},
		{Key: "phone", Text: "Leaked phone number"},
		{Key: "address", Text: "Leaked address"},
		{Key: "other", Text: "Other personal information"},
	}},
	{Key: "copyright", Text: "Copyright"},
	{Key: "takedown", Text: "It's not illegal, but must be taken down"},
	{Key: "other", Text: "Other", CommentRequired: true},
}}

// Find walks path from Root; ok is false for a path that isn't in the tree.
func Find(path []string) (Reason, bool) {
	r := Root
	for _, key := range path {
		var next *Reason
		for i := range r.Children {
			if r.Children[i].Key == key {
				next = &r.Children[i]
				break
			}
		}
		if next == nil {
			return Reason{}, false
		}
		r = *next
	}
	return r, true
}

// Label is a path for people: "Spam or scam › Phishing".
func Label(path []string) string {
	r := Root
	var parts []string
	for _, key := range path {
		found := false
		for _, c := range r.Children {
			if c.Key == key {
				parts = append(parts, c.Text)
				r, found = c, true
				break
			}
		}
		if !found {
			parts = append(parts, key)
			break
		}
	}
	return strings.Join(parts, " › ")
}

// Options are opaque to the apps; ours are readable. "r:" names a menu
// entry, "c:" the comment step after it.
const (
	optReason  = "r:"
	optComment = "c:"
)

// Step is what an option names.
type Step struct {
	Path    []string
	Comment bool // the comment step: the next call files it
}

// ParseOption reads an option the apps send back; ok is false for one that
// isn't ours.
func ParseOption(opt []byte) (Step, bool) {
	s := string(opt)
	if s == "" {
		return Step{}, true
	}
	var st Step
	switch {
	case strings.HasPrefix(s, optReason):
		s = s[len(optReason):]
	case strings.HasPrefix(s, optComment):
		s, st.Comment = s[len(optComment):], true
	default:
		return Step{}, false
	}
	if s == "" {
		return Step{}, false
	}
	st.Path = strings.Split(s, "/")
	if _, ok := Find(st.Path); !ok {
		return Step{}, false
	}
	return st, true
}

func option(prefix string, path []string) []byte {
	return []byte(prefix + strings.Join(path, "/"))
}

// Menu is the next screen for a menu step.
func Menu(path []string) tg.ReportResultClass {
	r, _ := Find(path)
	if len(r.Children) == 0 {
		res := &tg.ReportResultAddComment{Option: option(optComment, path)}
		res.SetOptional(!r.CommentRequired)
		return res
	}
	out := &tg.ReportResultChooseOption{Title: r.Title, Options: make([]tg.MessageReportOption, 0, len(r.Children))}
	for _, c := range r.Children {
		p := append(append([]string(nil), path...), c.Key)
		out.Options = append(out.Options, tg.MessageReportOption{Text: c.Text, Option: option(optReason, p)})
	}
	return out
}

// Leaf is whether path ends on an entry with nothing under it.
func Leaf(path []string) bool {
	r, ok := Find(path)
	return ok && len(path) > 0 && len(r.Children) == 0
}

// FromReason maps the old inputReportReason* that account.reportPeer and
// account.reportProfilePhoto still take onto the tree.
func FromReason(r tg.ReportReasonClass) []string {
	switch r.(type) {
	case *tg.InputReportReasonSpam:
		return []string{"spam", "spam"}
	case *tg.InputReportReasonViolence:
		return []string{"violence"}
	case *tg.InputReportReasonPornography:
		return []string{"adult", "porn"}
	case *tg.InputReportReasonChildAbuse:
		return []string{"child_abuse"}
	case *tg.InputReportReasonCopyright:
		return []string{"copyright"}
	case *tg.InputReportReasonFake:
		return []string{"spam", "impersonation"}
	case *tg.InputReportReasonIllegalDrugs:
		return []string{"illegal_goods", "drugs"}
	case *tg.InputReportReasonPersonalDetails:
		return []string{"personal_data"}
	}
	return []string{"other"}
}

// Tops is the top level for the panel's filter: key and text.
func Tops() [][2]string {
	out := make([][2]string, 0, len(Root.Children))
	for _, c := range Root.Children {
		out = append(out, [2]string{c.Key, c.Text})
	}
	return out
}
