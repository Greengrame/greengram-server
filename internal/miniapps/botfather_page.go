package miniapps

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/ids"
)

// BotFather pages: greengram/botfather_pages/ in the mini apps bundle holds
// the original's markup with this server's values in it (see bundle.go), and
// the pinned botfather.css and botfather.js do the rest through the
// initializers in the state. Controls with nothing behind them here carry
// data-gg-off, which the adapter answers with a toast.

var botRoute = regexp.MustCompile(`^/botfather/bot/(\d+)(?:/(.+))?$`)

// avatarColors are the pairs the original's letter pictures use.
var avatarColors = [][2]string{{"#ff885e", "#ff516a"}, {"#ffcd6a", "#ffa85c"}, {"#82b1ff", "#665fff"}, {"#a0de7e", "#54cb68"}, {"#53edd6", "#28c9b7"}, {"#72d5fd", "#2a9ef1"}, {"#e0a2f3", "#d669ed"}}

// avatarLetters is what the original puts in a letter picture: the first
// letters of the first and last words.
func avatarLetters(name string) string {
	words := strings.Fields(name)
	if len(words) == 0 {
		return "?"
	}
	first := func(w string) string {
		for _, r := range w {
			return strings.ToUpper(string(r))
		}
		return ""
	}
	if len(words) == 1 {
		return first(words[0])
	}
	return first(words[0]) + first(words[len(words)-1])
}

// botAvatar is the original's picture for a bot with no photo: its letters on a
// gradient. CSS makes it round.
func botAvatar(id int64, name string) template.URL {
	c := avatarColors[(id%7+7)%7]
	letter := avatarLetters(name)
	svg := `<svg width="160" height="160" preserveAspectRatio="none" viewBox="0 0 100 100" xmlns="http://www.w3.org/2000/svg"><defs><linearGradient id="g" x1="0%" x2="0%" y1="0%" y2="100%"><stop offset="0%" stop-color="` + c[0] + `"/><stop offset="100%" stop-color="` + c[1] +
		`"/></linearGradient></defs><style>text{font:600 44px -apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif,'Apple Color Emoji','Segoe UI Emoji','Segoe UI Symbol';-webkit-user-select:none;user-select:none}</style><rect width="100" height="100" fill="url(#g)"/><text text-anchor="middle" x="50" y="66" fill="#fff">` +
		template.HTMLEscapeString(letter) + `</text></svg>`
	return template.URL("data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg)))
}

// botPicture is the bot's photo if it has one, else its letter.
func (s *Server) botPicture(ctx context.Context, b bots.Bot) template.URL {
	if u, ok, err := s.Accounts.User(ctx, b.UserID); err == nil && ok && u.PhotoID != 0 {
		return template.URL(photoURL(u.PhotoID))
	}
	return botAvatar(b.UserID, b.Name)
}

type botRow struct {
	ID       string
	Name     string
	Username string
	Avatar   template.URL
}

type rightRow struct {
	Name, Label string
	On          bool
}

type rightsGroup struct {
	Rows  []rightRow
	Count int
}

func rightRows(names []string, labels map[string]string, on []string) rightsGroup {
	g := rightsGroup{}
	for _, n := range names {
		r := rightRow{Name: n, Label: labels[n], On: slices.Contains(on, n)}
		if r.On {
			g.Count++
		}
		g.Rows = append(g.Rows, r)
	}
	return g
}

var (
	groupRightLabels = map[string]string{"change_info": "Change Group Info", "delete_messages": "Delete Messages", "ban_users": "Restrict Users", "invite_users": "Invite New Users", "pin_messages": "Pin Messages",
		"add_admins": "Add New Admins", "manage_call": "Manage Video Chats", "anonymous": "Promote Anonymous Admins", "manage_topics": "Manage Topics", "post_stories": "Post stories", "edit_stories": "Edit stories", "delete_stories": "Delete stories", "other": "Manage Group"}
	channelRightLabels = map[string]string{"change_info": "Change Channel Info", "post_messages": "Post Messages", "edit_messages": "Edit Messages", "delete_messages": "Delete Messages", "ban_users": "Restrict Users",
		"add_admins": "Add New Admins", "invite_users": "Invite New Users", "manage_call": "Manage Live Streams", "manage_direct_messages": "Manage direct messages", "post_stories": "Post stories", "edit_stories": "Edit stories", "delete_stories": "Delete stories", "other": "Manage Channel"}
)

// feedbackRate is one choice in Inline Feedback: Value is the original's "1 in n".
type feedbackRate struct {
	Value, Label string
	On           bool
}

func feedbackRates(every int) ([]feedbackRate, string) {
	list := []feedbackRate{{"0", "0%", false}, {"1000", "0.1%", false}, {"100", "1%", false}, {"10", "10%", false}, {"1", "100%", false}}
	label := "0%"
	for i := range list {
		if list[i].Value == strconv.Itoa(every) {
			list[i].On, label = true, list[i].Label
		}
	}
	return list, label
}

// appRow is a direct link on the Mini Apps page and in its editor.
type appRow struct {
	ID          int64
	Title       string
	Description string
	URL         string
	ShortName   string
	Link        string
	Display     string
	Picture     template.URL
}

// splashIcon is the launch screen icon for the page's preview. d went through
// bots.IconPath, so it's only path characters.
func splashIcon(d string) template.HTML {
	return template.HTML(`<svg class="tm-launch-screen-preview-icon" width="44" height="44" viewBox="0 0 512 512" xmlns="http://www.w3.org/2000/svg"><path fill="currentColor" d="M` + template.HTMLEscapeString(d) + `z"/></svg>`)
}

func linkScheme(r *http.Request) string {
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		return "https://"
	}
	return "http://"
}

type provider struct {
	Slug, Name string
	Icon       string
}

// paymentProviders is the original's list. Icons are greengram-web's captured
// ones, under /__greengram/; Unlimint's is the original's letter picture.
var paymentProviders = []provider{{"prtm", "Portmone", "jpg"}, {"redsys", "Redsys", "jpg"}, {"yam", "ЮKassa", "jpg"}, {"rbkz", "Robokassa KZ", "jpg"}, {"b131", "Bank 131", "jpg"}, {"pmuz", "Paycom.Uz", "jpg"},
	{"gpuz", "Global Pay Uzbekistan", "jpg"}, {"ecp", "ECOMMPAY", "jpg"}, {"pmaster", "PayMaster", "jpg"}, {"frdkg", "FreedomPay KG", "jpg"}, {"frduz", "FreedomPay UZ", "jpg"}, {"cluz", "CLICK Uzbekistan", "jpg"},
	{"ip88", "iPay88", "jpg"}, {"rbks", "Robokassa", "jpg"}, {"bpdby", "bePaid.by", "jpg"}, {"alfby", "Alfa Bank Belarus", "jpg"}, {"rnpay", "RunPay", "jpg"}, {"pyks", "Paykassma", "jpg"}, {"pbmn", "PayBox.money", "jpg"},
	{"chapa", "Chapa", "jpg"}, {"smartg", "Smart Glocal", "jpg"}, {"unln", "Unlimint", "svg"}, {"amrp", "Ammer Pay", "jpg"}}

// notHere explains a page whose feature this server doesn't have.
var notHere = map[string]struct{ Title, Icon, Text, Back string }{
	"gamescreate":      {"Games", "games", "Games aren't available on this server.", "games"},
	"transfer":         {"Transfer Ownership", "transfer-ownership", "Transferring a bot isn't available on this server.", ""},
	"usernames/create": {"Premium Usernames", "at", "Premium usernames for bots aren't available on this server.", "usernames"},
}

type botView struct {
	ID       string
	Name     string
	Username string
	Avatar   template.URL
	Host     string // what the original shows as t.me/
	AppHost  string // the same for direct links, see newAppRow
	About    string
	Welcome  string
	Token    string
	Commands []bots.Command
	Bot      bots.Bot

	EditingCommand string
	Description    string
	MainMode       string
	GroupRights    rightsGroup
	ChannelRights  rightsGroup
	AllowGroups    bool
	GroupPrivacy   bool
	Access         bool
	AccessUsers    []accessUser
	BotToBot       bool
	InlineOn       bool
	Apps           []appRow
	App            appRow
	SameOrigin     bool
	Splash         bots.Splash
	SplashIcon     template.HTML
	InlineHint     string
	Feedback       []feedbackRate
	FeedbackLabel  string
	PrivacyURL     string
	Providers      []provider
	Provider       provider
	Note           struct{ Title, Icon, Text string }
}

// newAppRow's links are /<bot>/<app>, not the /u/ form the rest of the page uses:
// Web A only knows /u/<name> inside a chat, and would drop the app.
func newAppRow(a bots.App, username, host, scheme string) appRow {
	return appRow{ID: a.ID, Title: a.Title, Description: a.Description, URL: a.URL, ShortName: a.ShortName, Display: host + username + "/" + a.ShortName,
		Link: scheme + host + username + "/" + a.ShortName, Picture: template.URL(photoURL(a.Photo))}
}

func (s *Server) renderBotPage(name string, v any) (string, error) {
	pages, err := s.pages.get(s.Dir)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := pages.ExecuteTemplate(&out, name+".html", v); err != nil {
		return "", fmt.Errorf("botfather page %s: %w", name, err)
	}
	return out.String(), nil
}

func (s *Server) botfatherPage(r *http.Request, u account.User) (pageView, error) {
	ctx := r.Context()
	p := strings.TrimSuffix(r.URL.Path, "/")
	host := r.Host + "/u/"
	switch p {
	case "/botfather":
		list, err := s.Bots.Owned(ctx, u.ID)
		if err != nil {
			return pageView{}, err
		}
		rows := []botRow{}
		for _, b := range list {
			if !b.Disabled {
				rows = append(rows, botRow{strconv.FormatInt(ids.Client(b.UserID), 10), b.Name, b.Username, s.botPicture(ctx, b)})
			}
		}
		markup, err := s.renderBotPage("list", rows)
		return pageView{Title: "BotFather", Markup: markup, State: map[string]any{"page": "list", "captured": true, "initializers": []string{"BotList.init"}}}, err
	case "/botfather/create", "/botfather/new":
		markup, err := s.renderBotPage("create", map[string]string{"Host": host})
		return pageView{Title: "New bot", Markup: markup, State: map[string]any{"page": "create", "captured": true, "initializers": []string{"CreateBot.init"}, "backButton": "/botfather"}}, err
	}
	m := botRoute.FindStringSubmatch(p)
	if m == nil {
		return pageView{}, &account.Error{Status: 404, Message: "This menu is not available on this instance yet."}
	}
	cid, _ := strconv.ParseInt(m[1], 10, 64)
	b, ok, err := s.Bots.Get(ctx, ids.Server(cid))
	if err != nil {
		return pageView{}, err
	}
	if !ok || b.OwnerID != u.ID || b.Disabled || b.Kind != bots.Custom {
		return pageView{}, &account.Error{Status: 404, Message: "Bot not found."}
	}
	id := m[1]
	section := m[2]
	home := "/botfather/bot/" + id
	v := botView{ID: id, Name: b.Name, Username: b.Username, Avatar: s.botPicture(ctx, b), Host: host, AppHost: r.Host + "/", About: b.About, Welcome: b.Description, Commands: b.Commands, Bot: b}
	state := map[string]any{"captured": true, "botId": id, "username": b.Username, "backButton": home, "page": section}
	init := []string{}
	page := section
	switch {
	case section == "":
		page, state["page"], state["backButton"] = "bot", "bot", "/botfather"
		init = []string{"BotGeneral.init"}
		if v.Token, _, err = s.Bots.Token(ctx, b.UserID); err != nil {
			return pageView{}, err
		}
	case section == "profile" || section == "edit":
		page = "profile"
		init = []string{"BotProfile.init"}
		state["lang"] = nil
		if photo := b.State.WelcomePhoto; photo != 0 {
			state["files"] = map[string]any{"welcome_msg_pic": map[string]any{"photo_id": strconv.FormatInt(photo, 10)}}
			state["welcomePic"] = photoURL(photo)
		}
	case section == "usernames":
		init = []string{"BotUsernames.init"}
		state["backButton"] = home + "/profile"
	case section == "commands":
		init = []string{"BotCommandsList.init"}
		state["lang"] = nil
	case section == "commandcreate" || strings.HasPrefix(section, "commands/"):
		page = "commandcreate"
		init = []string{"BotCommandEdit.init"}
		state["backButton"] = home + "/commands"
		state["lang"] = ""
		state["editingCommand"] = ""
		if name, ok := strings.CutPrefix(section, "commands/"); ok {
			i := slices.IndexFunc(b.Commands, func(c bots.Command) bool { return c.Command == name })
			if i < 0 {
				return pageView{}, &account.Error{Status: 404, Message: "This command doesn't exist."}
			}
			v.EditingCommand, v.Description = name, b.Commands[i].Description
			state["editingCommand"] = name
		}
	case section == "apps":
		init = []string{"BotApps.init"}
		state["appsFrom"] = ""
		v.SameOrigin = bool(b.State.Settings.SameOrigin)
		list, err := s.Bots.Apps(ctx, b.UserID)
		if err != nil {
			return pageView{}, err
		}
		for _, a := range list {
			v.Apps = append(v.Apps, newAppRow(a, b.Username, r.Host+"/", linkScheme(r)))
		}
	case section == "appcreate" || strings.HasPrefix(section, "apps/"):
		page = "appedit"
		init = []string{"BotAppEdit.init"}
		state["backButton"], state["appsFrom"], state["editingGame"] = home+"/apps", "", false
		state["files"] = map[string]any{}
		if raw, ok := strings.CutPrefix(section, "apps/"); ok {
			appID, _ := strconv.ParseInt(raw, 10, 64)
			a, ok, err := s.Bots.AppByID(ctx, appID)
			if err != nil {
				return pageView{}, err
			}
			if !ok || a.Bot != b.UserID {
				return pageView{}, &account.Error{Status: 404, Message: "This link doesn't exist."}
			}
			v.App = newAppRow(a, b.Username, r.Host+"/", linkScheme(r))
			state["editingGame"] = raw
			state["files"] = map[string]any{"game_pic": map[string]any{"photo_id": strconv.FormatInt(a.Photo, 10)}}
		}
	case section == "launchscreen":
		init = []string{"BotLaunchScreen.init"}
		state["backButton"] = home + "/mainapp"
		v.Splash = b.State.Settings.Splash
		if v.Splash.Path != "" {
			v.SplashIcon = splashIcon(v.Splash.Path)
		}
		state["mode"] = "dark"
		state["bg_color"], state["bg_dark_color"] = v.Splash.Background, v.Splash.BackgroundDark
		state["header_color"], state["header_dark_color"] = v.Splash.Header, v.Splash.HeaderDark
		state["placeholder_path"] = v.Splash.Path
	case section == "menuapp":
		init = []string{"BotMenuApp.init"}
		state["backButton"], state["appsFrom"] = home+"/apps", ""
	case section == "mainapp":
		init = []string{"BotMainApp.init"}
		state["backButton"], state["appsFrom"] = home+"/apps", ""
		v.MainMode = bots.ModeCompact
		if b.MainAppURL != "" {
			v.MainMode = b.MainMode()
		}
		state["mode"] = v.MainMode
	case section == "settings":
		init = []string{"BotSettings.init"}
		v.AllowGroups = b.State.AllowGroups()
		v.GroupPrivacy = b.State.GroupPrivacy()
		v.BotToBot = bool(b.State.Settings.BotToBot)
		v.Access = bool(b.State.Settings.Access)
		if v.AccessUsers, err = s.accessRows(ctx, b.State.Settings.AccessUsers); err != nil {
			return pageView{}, err
		}
		v.InlineOn = bool(b.State.Settings.Inline)
		v.PrivacyURL = b.State.Settings.PrivacyURL
		v.GroupRights = rightRows(bots.GroupRightNames, groupRightLabels, b.State.Settings.GroupRights)
		v.ChannelRights = rightRows(bots.ChannelRightNames, channelRightLabels, b.State.Settings.ChannelRights)
	case section == "inline":
		state["backButton"] = home + "/settings"
		init = []string{"BotSettingsInline.init"}
		v.InlineOn = bool(b.State.Settings.Inline)
		v.InlineHint = b.State.Settings.Placeholder
		v.Feedback, v.FeedbackLabel = feedbackRates(b.State.FeedbackEvery())
	case section == "login", section == "cloud", section == "games", section == "stars":
	case section == "payments":
		v.Providers = paymentProviders
	case strings.HasPrefix(section, "payments/"):
		slug := strings.TrimPrefix(section, "payments/")
		i := slices.IndexFunc(paymentProviders, func(p provider) bool { return p.Slug == slug })
		if i < 0 {
			return pageView{}, &account.Error{Status: 404, Message: "This menu is not available on this instance yet."}
		}
		page, v.Provider = "provider", paymentProviders[i]
		state["backButton"] = home + "/payments"
	default:
		n, ok := notHere[section]
		if !ok {
			return pageView{}, &account.Error{Status: 404, Message: "This menu is not available on this instance yet."}
		}
		page = "unavailable"
		v.Note.Title, v.Note.Icon, v.Note.Text = n.Title, n.Icon, n.Text
		if n.Back != "" {
			state["backButton"] = home + "/" + n.Back
		}
	}
	state["initializers"] = init
	markup, err := s.renderBotPage(page, v)
	return pageView{Title: b.Name, Markup: markup, State: state}, err
}
