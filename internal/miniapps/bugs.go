package miniapps

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/media"
)

type bugsAttachment struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

type bugsRequest struct {
	Action string           `json:"action"`
	ID     int64            `json:"id"`
	Kind   string           `json:"kind"`
	Title  string           `json:"title"`
	Text   string           `json:"text"`
	Vote   int              `json:"vote"`
	Status string           `json:"status"`
	Files  []bugsAttachment `json:"files"`
}

// bugsAsset serves platform.js from the repository and everything else from
// the bundle's greengram/bugs/: the page's stylesheets, fonts and pictures are
// captured from bugs.telegram.org, so they aren't in this repository.
func (s *Server) bugsAsset(w http.ResponseWriter, r *http.Request) {
	var key = r.PathValue("path")
	var raw []byte
	var err error
	if !validKey(key) || strings.HasSuffix(key, ".html") {
		http.NotFound(w, r)
		return
	}
	if key == "platform.js" {
		raw, err = staticFiles.ReadFile("static/bugs/platform.js")
	} else {
		raw, err = s.bugsFileFromBundle(key)
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(key)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(raw)
}

// bugsFileFromBundle reads <dir>/greengram/bugs/<key>; key is a validKey.
func (s *Server) bugsFileFromBundle(key string) ([]byte, error) {
	if s.Dir == "" {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(filepath.Join(s.Dir, ExtrasDir, "bugs", filepath.FromSlash(key)))
}

func (s *Server) bugsDocument(w http.ResponseWriter, r *http.Request) {
	var raw []byte
	var err error
	raw, err = s.bugsFileFromBundle("index.html")
	if err != nil {
		missingPage(w, "bugs")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'self'; form-action 'self'; base-uri 'none'; object-src 'none'")
	w.Write(raw)
}

func (s *Server) bugsUser(r *http.Request) (account.User, error) {
	var raw = r.Header.Get("X-Telegram-Init-Data")
	var launched bots.Launch
	var user account.User
	var found bool
	var err error
	var slot = r.URL.Query().Get("account")
	var number int
	var cookieName = "session"
	var cookie *http.Cookie
	if raw != "" {
		launched, err = s.Bots.Launched(r.Context(), bots.BugsBot, raw)
		if err != nil {
			return user, &account.Error{Status: 401, Message: "Restart the miniapp to sign in."}
		}
		user, found, err = s.Accounts.User(r.Context(), launched.User)
		if err != nil {
			return user, err
		}
		if !found {
			return user, &account.Error{Status: 401, Message: "Sign in to continue."}
		}
	} else {
		if slot != "" {
			number, err = strconv.Atoi(slot)
			if err != nil || number < 1 || number > 6 {
				return user, &account.Error{Status: 400, Message: "Choose an account slot between 1 and 6."}
			}
			if number > 1 {
				cookieName += strconv.Itoa(number)
			}
		}
		cookie, err = r.Cookie(cookieName)
		if err != nil {
			return user, &account.Error{Status: 401, Message: "Sign in to Greengram to continue."}
		}
		user, _, err = s.Accounts.SessionUser(r.Context(), cookie.Value)
		if err != nil {
			return user, err
		}
	}
	if user.IsBot || user.IsSystem || user.Disabled || user.State == "deleted" || user.State == "banned" || user.State == "locked" {
		return account.User{}, &account.Error{Status: 403, Message: "An active person account is required."}
	}
	return user, nil
}

func (s *Server) bugsAPI(w http.ResponseWriter, r *http.Request) {
	var user account.User
	var err error
	var refusal *account.Error
	var data bugsRequest
	var filter bots.BugFilter
	var cards []bots.BugCard
	var comments []bots.BugComment
	var id int64
	var result any
	var origin *url.URL
	var status = http.StatusOK
	var date string
	var parseErr error
	var attachments []bots.BugFile
	var uploads []bots.BugUpload
	user, err = s.bugsUser(r)
	if err != nil && r.Method == http.MethodGet && errors.As(err, &refusal) && refusal.Status == 401 {
		user, err = account.User{}, nil
	}
	if err == nil && r.Method == http.MethodPost {
		if r.Header.Get("X-Greengram-Bugs") != "1" || strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
			err = &account.Error{Status: 400, Message: "Use the Bugs and Suggestions miniapp."}
		} else if r.Header.Get("Origin") != "" {
			origin, err = url.Parse(r.Header.Get("Origin"))
			if err != nil || origin.Host != r.Host || (origin.Scheme != "http" && origin.Scheme != "https") {
				err = &account.Error{Status: 403, Message: "Invalid request origin."}
			}
		}
		if err == nil && user.Frozen() {
			err = &account.Error{Status: 403, Message: "This account is read-only."}
		}
		if err == nil {
			if parseErr = json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&data); parseErr != nil {
				err = &account.Error{Status: 400, Message: "Send a valid request of at most 8 MB."}
			}
		}
		if err == nil {
			switch data.Action {
			case "create":
				err = bots.ValidateBugCard(data.Kind, data.Title, data.Text)
				if err == nil {
					err = s.Bots.CheckBugCardAllowance(r.Context(), user.ID)
				}
				if err == nil {
					uploads, err = s.prepareBugUploads(r, user.ID, data.Files)
				}
				if err == nil {
					id, err = s.Bots.CreateBugCardWithFiles(r.Context(), user.ID, data.Kind, data.Title, data.Text, uploads)
				}
				result = map[string]any{"id": id}
			case "edit":
				err = s.Bots.EditBugCard(r.Context(), user, data.ID, data.Title, data.Text)
				result = map[string]any{"ok": true}
			case "delete":
				err = s.Bots.DeleteBugCard(r.Context(), user, data.ID)
				result = map[string]any{"ok": true}
			case "vote":
				err = s.Bots.VoteBugCard(r.Context(), user.ID, data.ID, data.Vote)
				result = map[string]any{"ok": true}
			case "comment":
				err = s.Bots.CommentBugCard(r.Context(), user.ID, data.ID, data.Text)
				result = map[string]any{"ok": true}
			case "status":
				err = s.Bots.SetBugCardStatus(r.Context(), user, data.ID, data.Status)
				result = map[string]any{"ok": true}
			default:
				err = &account.Error{Status: 400, Message: "Choose a supported action."}
			}
		}
	} else if err == nil {
		filter = bots.BugFilter{Query: strings.TrimSpace(r.URL.Query().Get("query")), Sort: r.URL.Query().Get("sort"), Status: r.URL.Query().Get("status"), From: r.URL.Query().Get("date_from"), To: r.URL.Query().Get("date_to")}
		filter.Offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
		if filter.Offset < 0 {
			filter.Offset = 0
		}
		switch r.URL.Query().Get("type") {
		case "issues":
			filter.Kind = bots.ReportBug
		case "suggestions":
			filter.Kind = bots.ReportSuggestion
		}
		if filter.Sort != "time" {
			filter.Sort = "rate"
		}
		for _, date = range []string{filter.From, filter.To} {
			if date != "" {
				if _, parseErr = time.Parse("2006-01-02", date); parseErr != nil {
					err = &account.Error{Status: 400, Message: "Use a valid calendar date."}
				}
			}
		}
		if err == nil && r.URL.Query().Get("id") != "" {
			id, err = strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
			if err != nil || id < 1 {
				err = &account.Error{Status: 400, Message: "Choose a valid card."}
			}
		}
		if err == nil {
			cards, err = s.Bots.BugCards(r.Context(), user.ID, filter, id)
		}
		if err == nil && id != 0 {
			if len(cards) == 0 {
				err = &account.Error{Status: 404, Message: "Card not found."}
			} else {
				comments, err = s.Bots.BugComments(r.Context(), id)
				if err == nil {
					attachments, err = s.Bots.BugFiles(r.Context(), id, 0)
				}
			}
		}
		result = map[string]any{"cards": cards, "comments": comments, "files": attachments, "user": map[string]any{"id": user.ID, "name": user.Name, "isAdmin": user.Role == "admin"}}
	}
	if err != nil {
		if errors.As(err, &refusal) {
			status, result = refusal.Status, map[string]any{"error": refusal.Message}
		} else {
			status, result = 500, map[string]any{"error": "Something went wrong. Try again."}
			slog.Error("bugs platform", "err", err)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(result)
}

func (s *Server) prepareBugUploads(r *http.Request, user int64, files []bugsAttachment) ([]bots.BugUpload, error) {
	var uploads = []bots.BugUpload{}
	var file bugsAttachment
	var document media.Document
	var err error
	if len(files) > 5 {
		return nil, &account.Error{Status: 400, Message: "Attach at most five files."}
	}
	for _, file = range files {
		if len(file.Data) == 0 || len(file.Data) > 1<<20 || len(file.Name) == 0 || len(file.Name) > 240 || strings.ContainsAny(file.Name, "\x00\r\n") {
			return nil, &account.Error{Status: 400, Message: "Use filenames up to 240 characters and files up to 1 MB each."}
		}
	}
	for _, file = range files {
		document, err = s.Media.NewDocument(r.Context(), user, file.Name, file.Data)
		if err != nil {
			return nil, err
		}
		uploads = append(uploads, bots.BugUpload{ID: document.ID, Name: filepath.Base(strings.ReplaceAll(file.Name, "\\", "/"))})
	}
	return uploads, nil
}

func (s *Server) bugsFile(w http.ResponseWriter, r *http.Request) {
	var id int64
	var err error
	var files []bots.BugFile
	var document media.Document
	var raw []byte
	var disposition = "attachment"
	var contentType string
	var thumbnail = r.URL.Query().Get("thumb") == "1"
	id, err = strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	files, err = s.Bots.BugFiles(r.Context(), 0, id)
	if err != nil || len(files) != 1 {
		http.NotFound(w, r)
		return
	}
	document, raw, err = s.Media.DocumentFile(r.Context(), id, thumbnail)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	contentType = document.Mime
	if thumbnail && document.Thumb != nil {
		contentType = "image/jpeg"
	}
	switch contentType {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "video/mp4", "video/webm":
		disposition = "inline"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": files[0].Name}))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, files[0].Name, time.Unix(int64(document.Date), 0), bytes.NewReader(raw))
}
