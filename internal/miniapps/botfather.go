package miniapps

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"unicode/utf8"

	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/media"
)

// maxBotAbout is the short text on a bot's profile, Telegram's limit.
const maxBotAbout = 120

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]any{"ok": false, "error": msg})
}

// botfatherAPI is the backend of the BotFather mini app: greengram-web's
// /api/botfather methods, signed in by the app's initData instead of a
// site cookie. The captured page still needs its adapter script pointed
// here (greengram-web frontend/botfather-adapter.js).
func (s *Server) botfatherAPI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		fail(w, 400, "Can't read the request.")
		return
	}
	var data map[string]any
	if len(raw) > 0 && json.Unmarshal(raw, &data) != nil {
		fail(w, 400, "Send a JSON object.")
		return
	}
	init := r.Header.Get("X-Telegram-Init-Data")
	if v, ok := data["initData"].(string); ok && init == "" {
		init = v
	}
	l, err := s.Bots.Launched(ctx, bots.BotFather, init)
	if err != nil {
		fail(w, 401, "Open BotFather from the app.")
		return
	}
	user := l.User
	str := func(k string) string { v, _ := data[k].(string); return v }
	method := str("method")

	var refusal *bots.Error
	answer := func(v any, err error) {
		switch {
		case errors.As(err, &refusal):
			reply(w, 200, map[string]any{"ok": false, "error": refusal.Message})
		case err != nil:
			slog.Error("miniapps: botfather "+method, "err", err)
			fail(w, 500, "Something went wrong. Try again.")
		default:
			reply(w, 200, v)
		}
	}

	switch method {
	case "auth":
		answer(map[string]any{"ok": true, "api_url": "/__miniapps/botfather/api"}, nil)
		return
	case "getBots":
		list, err := s.Bots.Owned(ctx, user)
		out := []map[string]any{}
		for _, b := range list {
			out = append(out, map[string]any{"id": ids.Client(b.UserID), "username": b.Username, "name": b.Name, "description": b.Description})
		}
		answer(map[string]any{"ok": true, "bots": out}, err)
		return
	case "checkBotUsername":
		err := s.Bots.CheckUsername(ctx, str("username"))
		if err == nil {
			answer(map[string]any{"ok": true}, nil)
			return
		}
		answer(nil, err)
		return
	}
	// everything past here changes something
	if frozen, err := s.Bots.Frozen(ctx, user); err != nil || frozen {
		if err == nil {
			err = bots.ErrFrozen
		}
		answer(nil, err)
		return
	}
	switch method {
	case "createBot":
		if utf8.RuneCountInString(str("about")) > maxBotAbout {
			fail(w, 400, "The about text can be up to 120 characters.")
			return
		}
		b, token, err := s.Bots.Create(ctx, user, str("title"), str("username"))
		if err == nil && str("about") != "" {
			err = s.Bots.SetAbout(ctx, b.UserID, str("about"))
		}
		if err == nil {
			err = s.setBotPicture(ctx, user, b.UserID, str("userpic"))
		}
		answer(map[string]any{"ok": true, "bot_id": ids.Client(b.UserID), "token": token, "msg": "Your bot has been created."}, err)
		return
	}

	bid, _ := strconv.ParseInt(strings.TrimSpace(str("bid")), 10, 64)
	if f, ok := data["bid"].(float64); ok {
		bid = int64(f)
	}
	b, ok, err := s.Bots.Get(ctx, ids.Server(bid))
	if err != nil {
		answer(nil, err)
		return
	}
	if !ok || b.OwnerID != user || b.Disabled {
		fail(w, 404, "Bot not found.")
		return
	}
	saved := map[string]any{"ok": true, "msg": "Changes saved."}
	switch method {
	case "setSecondaryUsername":
		answer(saved, s.Bots.SetSecondaryUsername(ctx, user, b.UserID, str("username")))
	case "revokeAccessToken":
		token, err := s.Bots.Revoke(ctx, b.UserID)
		answer(map[string]any{"ok": true, "token": token}, err)
	case "updateBotProfile":
		title, about := strings.TrimSpace(str("title")), str("about")
		welcome, hasWelcome := data["welcome_msg"].(string)
		if title == "" || utf8.RuneCountInString(title) > 64 || utf8.RuneCountInString(about) > maxBotAbout || utf8.RuneCountInString(welcome) > 512 {
			fail(w, 400, "Check the name and about text.")
			return
		}
		if str("lang_code") != "" {
			fail(w, 501, "Localized info isn't available on this server.")
			return
		}
		err := s.Bots.SetName(ctx, b.UserID, title)
		if err == nil {
			err = s.Bots.SetAbout(ctx, b.UserID, about)
		}
		if err == nil && hasWelcome {
			err = s.Bots.SetDescription(ctx, b.UserID, welcome)
		}
		if err == nil {
			err = s.setBotPicture(ctx, user, b.UserID, str("userpic"))
		}
		if _, has := data["welcome_msg_pic"]; has && err == nil {
			err = s.setWelcomePicture(ctx, user, b, str("welcome_msg_pic"))
		}
		answer(map[string]any{"ok": true, "msg": "Profile updated."}, err)
	case "setCommand":
		list, msg, err := setCommand(b.Commands, strings.TrimPrefix(strings.TrimSpace(str("command")), "/"), strings.TrimSpace(str("description")), str("replace"))
		if err == nil {
			err = s.Bots.SetCommands(ctx, b.UserID, list)
		}
		answer(map[string]any{"ok": true, "msg": msg}, err)
	case "reorderCommands":
		names, _ := data["commands"].([]any)
		list, err := orderCommands(b.Commands, names)
		if err == nil {
			err = s.Bots.SetCommands(ctx, b.UserID, list)
		}
		answer(saved, err)
	case "setCommands":
		list, ok := commandLines(str("commands"))
		if !ok {
			fail(w, 400, "Use command - description, one command per line.")
			return
		}
		answer(saved, s.Bots.SetCommands(ctx, b.UserID, list))
	case "changeSettings":
		set, _ := data["settings"].(map[string]any)
		if len(set) == 0 {
			fail(w, 400, "Nothing to change.")
			return
		}
		var err error
		for key, value := range set {
			if err = s.changeBotSetting(ctx, b, key, value); err != nil {
				break
			}
		}
		answer(map[string]any{"ok": true, "msg": "Settings updated."}, err)
	case "updateMainApp":
		link := str("webview_url")
		if link != "" && !strings.HasPrefix(link, "https://") {
			fail(w, 400, "Use an HTTPS address.")
			return
		}
		mode := str("mode")
		if mode != "" && mode != bots.ModeCompact && mode != bots.ModeFullsize && mode != bots.ModeFullscreen {
			fail(w, 400, "Choose a launch mode.")
			return
		}
		err := s.Bots.SetMainApp(ctx, b.UserID, link)
		if err == nil && mode != "" {
			err = s.Bots.SetMainAppMode(ctx, b.UserID, mode)
		}
		answer(saved, err)
	case "setApp":
		msg, err := s.setBotApp(ctx, user, b, data)
		answer(map[string]any{"ok": true, "msg": msg}, err)
	case "setAccessUsers":
		list, err := s.setAccessUsers(ctx, b, str("add"), str("remove"))
		answer(map[string]any{"ok": true, "users": list}, err)
	case "deleteBot", "requestDeleteBot":
		answer(map[string]any{"ok": true, "msg": "Bot deleted."}, s.Bots.Delete(ctx, b.UserID))
	default:
		fail(w, 501, "This isn't available on this server.")
	}
}

// botfatherUpload keeps a picture for a bot; it becomes the bot's when the
// page saves (createBot and updateBotProfile's userpic), so backing out of
// the page changes nothing.
func (s *Server) botfatherUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	l, err := s.Bots.Launched(ctx, bots.BotFather, r.Header.Get("X-Telegram-Init-Data"))
	if err != nil {
		fail(w, 401, "Open BotFather from the app.")
		return
	}
	if frozen, err := s.Bots.Frozen(ctx, l.User); err != nil || frozen {
		fail(w, 403, "This account can't change anything right now.")
		return
	}
	target := r.URL.Query().Get("target")
	if target == "launch_icon" {
		d, err := bots.IconPath(io.LimitReader(r.Body, 100<<10))
		var refusal *bots.Error
		if errors.As(err, &refusal) {
			fail(w, 400, refusal.Message)
			return
		}
		if err != nil {
			fail(w, 400, "Can't read this icon.")
			return
		}
		reply(w, 200, map[string]any{"ok": true, "path": d, "svg": splashIcon(d)})
		return
	}
	if target != "bot_userpic" && target != "welcome_msg_pic" && target != "game_pic" {
		fail(w, 501, "Only the bot's profile, welcome and direct link pictures can be uploaded here.")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, media.MaxPhotoBytes+1))
	if err != nil || len(raw) > media.MaxPhotoBytes {
		fail(w, 400, "Pictures can be up to 10 MB.")
		return
	}
	p, err := s.Media.NewPhoto(ctx, l.User, raw)
	if err != nil {
		slog.Info("miniapps: botfather upload", "err", err)
		fail(w, 400, "Can't use this picture. Try a JPEG or PNG.")
		return
	}
	reply(w, 200, map[string]any{"ok": true, "media": map[string]any{"photo_id": strconv.FormatInt(p.ID, 10), "src": photoURL(p.ID)}})
}

func photoURL(id int64) string { return signedURL("/__miniapps/botfather/photo/", id) }

// PhotoURL is how another mini app's page loads a profile photo.
func PhotoURL(id int64) string { return photoURL(id) }

// botfatherPhoto serves a bot's picture to the pages; signed like sticker
// previews, since images load without the launch data.
func (s *Server) botfatherPhoto(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("photo"), 10, 64)
	if err != nil || !hmac.Equal([]byte(photoURL(id)), []byte(r.URL.Path+"?s="+r.URL.Query().Get("s"))) {
		http.NotFound(w, r)
		return
	}
	data, err := s.Media.PhotoFile(r.Context(), id, 320)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Write(data)
}

// setBotPicture makes an uploaded picture the bot's. raw "" leaves it; a
// photo the person didn't upload themselves is refused.
func (s *Server) setBotPicture(ctx context.Context, user, bot int64, raw string) error {
	if raw == "" {
		return nil
	}
	id, err := s.ownPicture(ctx, user, bot, raw)
	if err != nil {
		return err
	}
	return s.Media.SetUserPhoto(ctx, bot, id)
}

// setWelcomePicture is the picture above the welcome message; raw "" removes it.
// The page sends back the one it was given, which the bot may hold since import.
func (s *Server) setWelcomePicture(ctx context.Context, user int64, b bots.Bot, raw string) error {
	if raw == "" {
		return s.Bots.SetWelcomePhoto(ctx, b.UserID, 0)
	}
	if raw == strconv.FormatInt(b.State.WelcomePhoto, 10) {
		return nil
	}
	id, err := s.ownPicture(ctx, user, b.UserID, raw)
	if err != nil {
		return err
	}
	return s.Bots.SetWelcomePhoto(ctx, b.UserID, id)
}

func (s *Server) ownPicture(ctx context.Context, user, bot int64, raw string) (int64, error) {
	id, _ := strconv.ParseInt(raw, 10, 64)
	p, ok, err := s.Media.Photo(ctx, id)
	if err != nil {
		return 0, err
	}
	if !ok || p.Owner != user && p.Owner != bot {
		return 0, &bots.Error{Message: "Choose the picture again."}
	}
	return p.ID, nil
}

// changeBotSetting is one key of changeSettings. Keys with nothing behind them
// on this server are refused rather than stored, so a switch can't claim to work.
func (s *Server) changeBotSetting(ctx context.Context, b bots.Bot, key string, value any) error {
	switch key {
	case "menu_button":
		var menu *bots.MenuButton
		if m, ok := value.(map[string]any); ok {
			title, _ := m["title"].(string)
			link, _ := m["url"].(string)
			if title == "" || !strings.HasPrefix(link, "https://") && !strings.HasPrefix(link, "http://") {
				return &bots.Error{Message: "Choose a valid menu button."}
			}
			menu = &bots.MenuButton{Text: title, URL: link}
		}
		return s.Bots.SetMenu(ctx, b.UserID, menu)
	case "grps", "priv", "loop", "inline", "access":
		// the page posts true or 0
		on, _ := value.(bool)
		return s.Bots.SetSetting(ctx, b.UserID, key, on)
	case "inph":
		text, _ := value.(string)
		text = strings.TrimSpace(text)
		if utf8.RuneCountInString(text) > 64 {
			return &bots.Error{Message: "The placeholder can be up to 64 characters."}
		}
		if text == "" {
			return s.Bots.SetSetting(ctx, b.UserID, key, nil)
		}
		return s.Bots.SetSetting(ctx, b.UserID, key, text)
	case "infdb":
		rate, _ := value.(string)
		if !slices.Contains([]string{"0", "1", "10", "100", "1000"}, rate) {
			return &bots.Error{Message: "Choose a feedback rate."}
		}
		return s.Bots.SetSetting(ctx, b.UserID, key, rate)
	case "privacy_policy_url":
		link, _ := value.(string)
		link = strings.TrimSpace(link)
		if link == "" {
			return s.Bots.SetSetting(ctx, b.UserID, key, nil)
		}
		if u, err := url.Parse(link); err != nil || u.Host == "" || u.Scheme != "https" && u.Scheme != "http" {
			return &bots.Error{Message: "URL is invalid"}
		}
		return s.Bots.SetSetting(ctx, b.UserID, key, link)
	case "sor":
		on, _ := value.(bool)
		return s.Bots.SetSetting(ctx, b.UserID, key, on)
	case "splashscreen":
		raw, _ := json.Marshal(value)
		var set bots.Splash
		if json.Unmarshal(raw, &set) != nil || !bots.ValidPath(set.Path) {
			return &bots.Error{Message: "Choose the icon again."}
		}
		for _, c := range []string{set.Background, set.BackgroundDark, set.Header, set.HeaderDark} {
			if !bots.ValidColor(c) {
				return &bots.Error{Message: "Colors look like #rrggbb."}
			}
		}
		return s.Bots.SetSetting(ctx, b.UserID, key, set)
	case "group_admin_rights", "broadcast_admin_rights":
		known := bots.GroupRightNames
		if key == "broadcast_admin_rights" {
			known = bots.ChannelRightNames
		}
		list, _ := value.([]any)
		names := make([]string, 0, len(list))
		for _, v := range list {
			n, _ := v.(string)
			if !slices.Contains(known, n) {
				return &bots.Error{Message: "That right doesn't exist."}
			}
			names = append(names, n)
		}
		if len(names) == 0 {
			return s.Bots.SetSetting(ctx, b.UserID, key, nil)
		}
		return s.Bots.SetSetting(ctx, b.UserID, key, names)
	}
	return &bots.Error{Message: "This setting isn't available on this server."}
}

// accessUser is one row of Restrict bot usage's list.
type accessUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

const maxAccessUsers = 100

func (s *Server) accessRows(ctx context.Context, list []int64) ([]accessUser, error) {
	out := []accessUser{}
	for _, id := range list {
		u, ok, err := s.Accounts.User(ctx, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		name := u.Name
		if u.Username != "" {
			name += " (@" + u.Username + ")"
		}
		out = append(out, accessUser{strconv.FormatInt(id, 10), name})
	}
	return out, nil
}

// setAccessUsers adds by @username or removes by id. The original picks people
// with WebApp.requestChat, which Web A doesn't have, so the page takes a name.
func (s *Server) setAccessUsers(ctx context.Context, b bots.Bot, add, remove string) ([]accessUser, error) {
	list := slices.Clone(b.State.Settings.AccessUsers)
	if remove != "" {
		id, _ := strconv.ParseInt(remove, 10, 64)
		list = slices.DeleteFunc(list, func(v int64) bool { return v == id })
	} else {
		u, ok, err := s.Accounts.UserByUsername(ctx, strings.TrimPrefix(strings.TrimSpace(add), "@"))
		if err != nil {
			return nil, err
		}
		switch {
		case !ok:
			return nil, &bots.Error{Message: "There's nobody with this username."}
		case u.ID == b.OwnerID:
			return nil, &bots.Error{Message: "You always have access."}
		case len(list) >= maxAccessUsers:
			return nil, &bots.Error{Message: "A bot can allow up to 100 users."}
		case !slices.Contains(list, u.ID):
			list = append(list, u.ID)
		}
	}
	if err := s.Bots.SetSetting(ctx, b.UserID, "access_users", list); err != nil {
		return nil, err
	}
	return s.accessRows(ctx, list)
}

// setBotApp is the Direct Link page's save and the Mini Apps list's delete.
func (s *Server) setBotApp(ctx context.Context, user int64, b bots.Bot, data map[string]any) (string, error) {
	str := func(k string) string { v, _ := data[k].(string); return v }
	if str("type") != "app" {
		return "", &bots.Error{Message: "Games aren't available on this server."}
	}
	var id int64
	if v := str("game_id"); v != "" {
		var err error
		if id, err = strconv.ParseInt(v, 10, 64); err != nil {
			return "", &bots.Error{Message: "This link doesn't exist."}
		}
	}
	if del, _ := data["delete"].(bool); del {
		return "Link deleted.", s.Bots.DeleteApp(ctx, b.UserID, id)
	}
	a := bots.App{ID: id, Bot: b.UserID, ShortName: strings.TrimSpace(str("short_name")), Title: strings.TrimSpace(str("title")), Description: strings.TrimSpace(str("desc")), URL: strings.TrimSpace(str("webview_url"))}
	// the page sends back the picture it was given, which may be the bot's own by now
	if id != 0 {
		old, ok, err := s.Bots.AppByID(ctx, id)
		if err != nil {
			return "", err
		}
		if !ok || old.Bot != b.UserID {
			return "", &bots.Error{Message: "This link doesn't exist."}
		}
		if str("app_pic") == strconv.FormatInt(old.Photo, 10) {
			a.Photo = old.Photo
		}
	}
	if a.Photo == 0 {
		var err error
		if a.Photo, err = s.ownPicture(ctx, user, b.UserID, str("app_pic")); err != nil {
			return "", err
		}
	}
	if id != 0 {
		return "Link updated.", s.Bots.UpdateApp(ctx, a)
	}
	_, err := s.Bots.CreateApp(ctx, a)
	return "Link created.", err
}

var commandName = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// setCommand adds a command or, with replace, edits that one in place.
func setCommand(list []bots.Command, name, desc, replace string) ([]bots.Command, string, error) {
	if !commandName.MatchString(name) {
		return nil, "", &bots.Error{Message: "Commands use up to 32 Latin letters, numbers and underscores."}
	}
	if desc == "" || utf8.RuneCountInString(desc) > 256 {
		return nil, "", &bots.Error{Message: "The description can be up to 256 characters."}
	}
	at := -1
	for i, c := range list {
		if c.Command == replace && replace != "" {
			at = i
		} else if c.Command == name {
			return nil, "", &bots.Error{Message: "This command already exists."}
		}
	}
	out := slices.Clone(list)
	if replace != "" {
		if at < 0 {
			return nil, "", &bots.Error{Message: "This command doesn't exist."}
		}
		out[at] = bots.Command{Command: name, Description: desc}
		return out, "Command updated.", nil
	}
	if len(out) >= 100 {
		return nil, "", &bots.Error{Message: "A bot can have up to 100 commands."}
	}
	return append(out, bots.Command{Command: name, Description: desc}), "Command added.", nil
}

// orderCommands is the Edit list's Done: the order it shows, and a command that
// isn't in it was removed.
func orderCommands(list []bots.Command, names []any) ([]bots.Command, error) {
	out := []bots.Command{}
	for _, v := range names {
		n, _ := v.(string)
		i := slices.IndexFunc(list, func(c bots.Command) bool { return c.Command == n })
		if i < 0 {
			return nil, &bots.Error{Message: "This command doesn't exist."}
		}
		out = append(out, list[i])
	}
	return out, nil
}

// commandLines reads BotFather's "command - description" lines.
func commandLines(text string) ([]bots.Command, bool) {
	var out []bots.Command
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		c, d, ok := strings.Cut(line, "-")
		c = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(c), "/"))
		d = strings.TrimSpace(d)
		if !ok || c == "" || len(c) > 32 || d == "" || utf8.RuneCountInString(d) > 256 || seen[c] {
			return nil, false
		}
		for _, r := range c {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
				return nil, false
			}
		}
		seen[c] = true
		out = append(out, bots.Command{Command: c, Description: d})
	}
	return out, len(out) <= 100
}
