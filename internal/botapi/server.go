// Package botapi serves the Bot API over HTTP (https://<site>/bot<token>/
// <method>) for bots made with BotFather. It covers what greengram-web's
// bot API did plus keyboards and callback queries, on top of the same
// message store the apps use: a bot's message is an ordinary message in
// its box, pushed to people like any other.
package botapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/messages"
)

// Pusher sends journaled events to people's open apps (internal/api).
type Pusher interface {
	Deliver(events []messages.Event)
	DeliverChannel(events []channels.Event)
}

type Server struct {
	Bots     *bots.Service
	Accounts *account.Service
	Msgs     *messages.Service
	Chans    *channels.Service
	Push     Pusher

	// the running getUpdates of each bot; see getUpdates
	mu    sync.Mutex
	polls map[int64]*poll
}

type poll struct {
	stop       context.CancelFunc
	superseded bool
}

var pathRe = regexp.MustCompile(`^/bot([0-9]{1,20}:[A-Za-z0-9_-]{1,128})/([A-Za-z][A-Za-z0-9]*)$`)

// Wrap serves Bot API paths and hands the rest to next. A mux pattern
// can't match "/bot<token>", the token is glued to the segment.
func (s *Server) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/bot") {
			if m := pathRe.FindStringSubmatch(r.URL.Path); m != nil {
				s.serve(w, r, m[1], m[2])
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// apiError is a Bot API failure: error_code and description.
type apiError struct {
	code       int
	desc       string
	retryAfter int
}

func (e *apiError) Error() string { return e.desc }

func bad(desc string) error       { return &apiError{400, "Bad Request: " + desc, 0} }
func forbidden(desc string) error { return &apiError{403, "Forbidden: " + desc, 0} }

var errNotFound = &apiError{404, "Not Found", 0}

// tgDescriptions are what the official server says for the TL errors our
// services return.
var tgDescriptions = map[string]*apiError{
	"MESSAGE_NOT_MODIFIED":     {400, "Bad Request: message is not modified: specified new message content and reply markup are exactly the same as a current content and reply markup of the message", 0},
	"MESSAGE_ID_INVALID":       {400, "Bad Request: message to edit not found", 0},
	"MESSAGE_EMPTY":            {400, "Bad Request: message text is empty", 0},
	"MESSAGE_TOO_LONG":         {400, "Bad Request: message is too long", 0},
	"MESSAGE_AUTHOR_REQUIRED":  {400, "Bad Request: message can't be edited", 0},
	"MESSAGE_DELETE_FORBIDDEN": {400, "Bad Request: message can't be deleted", 0},
	"CHANNEL_PRIVATE":          {403, "Forbidden: bot is not a member of the channel chat", 0},
	"CHANNEL_INVALID":          {400, "Bad Request: chat not found", 0},
	"CHAT_ADMIN_REQUIRED":      {400, "Bad Request: not enough rights to send text messages to the chat", 0},
	"CHAT_WRITE_FORBIDDEN":     {403, "Forbidden: bot can't send messages to the chat", 0},
	"USER_NOT_PARTICIPANT":     {400, "Bad Request: user not found", 0},
	"PEER_ID_INVALID":          {400, "Bad Request: chat not found", 0},
	"RANDOM_ID_DUPLICATE":      {400, "Bad Request: message is a duplicate", 0},
}

func asAPIError(err error) *apiError {
	var e *apiError
	if errors.As(err, &e) {
		return e
	}
	var be *bots.Error
	if errors.As(err, &be) {
		return &apiError{400, "Bad Request: " + be.Message, 0}
	}
	if errors.Is(err, bots.ErrQueryInvalid) {
		return &apiError{400, "Bad Request: " + err.Error(), 0}
	}
	if t, ok := tgerr.As(err); ok {
		if d, ok := tgDescriptions[t.Type]; ok {
			return d
		}
		return &apiError{t.Code, "Bad Request: " + strings.ToLower(strings.ReplaceAll(t.Type, "_", " ")), 0}
	}
	return nil
}

type reply struct {
	OK          bool   `json:"ok"`
	Result      any    `json:"result,omitempty"`
	ErrorCode   int    `json:"error_code,omitempty"`
	Description string `json:"description,omitempty"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after,omitempty"`
	} `json:"parameters,omitempty"`
}

func write(w http.ResponseWriter, status int, v reply) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request, token, method string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		write(w, 405, reply{ErrorCode: 405, Description: "Method Not Allowed"})
		return
	}
	ctx := r.Context()
	bot, ok, err := s.Bots.ByToken(ctx, token)
	if err != nil {
		slog.Error("botapi: token", "err", err)
		write(w, 500, reply{ErrorCode: 500, Description: "Internal Server Error"})
		return
	}
	if !ok {
		write(w, 401, reply{ErrorCode: 401, Description: "Unauthorized"})
		return
	}
	p, err := readParams(r)
	if err != nil {
		e := asAPIError(err)
		write(w, e.code, reply{ErrorCode: e.code, Description: e.desc})
		return
	}
	fn, ok := methods[strings.ToLower(method)]
	if !ok {
		write(w, 404, reply{ErrorCode: 404, Description: "Not Found: method not found"})
		return
	}
	// greengram-web: a frozen bot or owner keeps only the get* methods
	if bot.Frozen && !strings.HasPrefix(strings.ToLower(method), "get") {
		write(w, 403, reply{ErrorCode: 403, Description: "Forbidden: this bot or its owner is frozen"})
		return
	}
	out, err := fn(s, &call{ctx: ctx, bot: bot, p: p})
	if err != nil {
		e := asAPIError(err)
		if e == nil {
			slog.Error("botapi: "+method, "bot", bot.UserID, "err", err)
			e = &apiError{500, "Internal Server Error", 0}
		}
		rep := reply{ErrorCode: e.code, Description: e.desc}
		if e.retryAfter > 0 {
			rep.Parameters = &struct {
				RetryAfter int `json:"retry_after,omitempty"`
			}{e.retryAfter}
		}
		write(w, e.code, rep)
		return
	}
	write(w, 200, reply{OK: true, Result: out})
}

// params are a request's fields; JSON bodies keep their types, forms and
// query strings are all strings, and the getters accept either.
type params map[string]any

const maxBody = 1 << 20

func readParams(r *http.Request) (params, error) {
	p := params{}
	for k, v := range r.URL.Query() {
		p[k] = v[0]
	}
	if r.Method != http.MethodPost {
		return p, nil
	}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch ct {
	case "", "application/json":
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
		if err != nil {
			return nil, bad("can't read the request")
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			return p, nil
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var body map[string]any
		if err := d.Decode(&body); err != nil {
			return nil, bad("can't parse JSON object")
		}
		for k, v := range body {
			p[k] = v
		}
	case "application/x-www-form-urlencoded":
		r.Body = http.MaxBytesReader(nil, r.Body, maxBody)
		if err := r.ParseForm(); err != nil {
			return nil, bad("can't parse the form")
		}
		for k, v := range r.PostForm {
			p[k] = v[0]
		}
	case "multipart/form-data":
		// uploads come with media (phase 6); fields work already. Without
		// the cap, file parts past maxBody go to temp files of any size.
		r.Body = http.MaxBytesReader(nil, r.Body, maxBody)
		if err := r.ParseMultipartForm(maxBody); err != nil {
			return nil, bad("can't parse the form")
		}
		for k, v := range r.MultipartForm.Value {
			p[k] = v[0]
		}
	default:
		return nil, bad("unsupported content type")
	}
	return p, nil
}

func (p params) has(k string) bool {
	v, ok := p[k]
	return ok && v != nil && v != ""
}

func (p params) str(k string) string {
	switch v := p[k].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return ""
	default:
		raw, _ := json.Marshal(v)
		return string(raw)
	}
}

func (p params) int64(k string) (int64, error) {
	s := strings.TrimSpace(p.str(k))
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, bad("wrong " + k + " specified")
	}
	return n, nil
}

func (p params) int(k string) (int, error) {
	n, err := p.int64(k)
	return int(n), err
}

func (p params) bool(k string) bool {
	switch strings.ToLower(p.str(k)) {
	case "true", "1":
		return true
	}
	return false
}

// json decodes a field that is JSON either as is or inside a string.
func (p params) json(k string, dst any) (bool, error) {
	v, ok := p[k]
	if !ok || v == nil || v == "" {
		return false, nil
	}
	var raw []byte
	if s, isStr := v.(string); isStr {
		raw = []byte(s)
	} else {
		raw, _ = json.Marshal(v)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return true, bad("can't parse " + k + " JSON object")
	}
	return true, nil
}

type call struct {
	ctx context.Context
	bot bots.Bot
	p   params
}

type method func(*Server, *call) (any, error)

var methods map[string]method

func init() {
	methods = map[string]method{
		"getme":                  (*Server).getMe,
		"logout":                 func(*Server, *call) (any, error) { return true, nil },
		"close":                  func(*Server, *call) (any, error) { return true, nil },
		"getupdates":             (*Server).getUpdates,
		"setwebhook":             (*Server).setWebhook,
		"deletewebhook":          (*Server).deleteWebhook,
		"getwebhookinfo":         (*Server).getWebhookInfo,
		"sendmessage":            (*Server).sendMessage,
		"editmessagetext":        (*Server).editMessageText,
		"editmessagereplymarkup": (*Server).editMessageReplyMarkup,
		"deletemessage":          (*Server).deleteMessage,
		"deletemessages":         (*Server).deleteMessages,
		"answercallbackquery":    (*Server).answerCallbackQuery,
		"answerinlinequery":      (*Server).answerInlineQuery,
		"sendchataction":         (*Server).sendChatAction,
		"getchat":                (*Server).getChat,
		"getchatmember":          (*Server).getChatMember,
		"getchatmembercount":     (*Server).getChatMemberCount,
		"getchatadministrators":  (*Server).getChatAdministrators,
		"leavechat":              (*Server).leaveChat,
		"setmycommands":          (*Server).setMyCommands,
		"getmycommands":          (*Server).getMyCommands,
		"deletemycommands":       (*Server).deleteMyCommands,
		"setchatmenubutton":      (*Server).setChatMenuButton,
		"getchatmenubutton":      (*Server).getChatMenuButton,
		"setmyname":              (*Server).setMyName,
		"getmyname":              (*Server).getMyName,
		"setmydescription":       (*Server).setMyDescription,
		"getmydescription":       (*Server).getMyDescription,
		"setmyshortdescription":  (*Server).setMyShortDescription,
		"getmyshortdescription":  (*Server).getMyShortDescription,
	}
	// files arrive with phase 6
	for _, m := range []string{"sendphoto", "senddocument", "sendvideo", "sendaudio", "sendvoice", "sendanimation", "sendsticker",
		"sendmediagroup", "sendvideonote", "getfile", "editmessagecaption", "editmessagemedia"} {
		methods[m] = func(*Server, *call) (any, error) { return nil, bad("files aren't supported on this server yet") }
	}
}

func (s *Server) now() int { return int(time.Now().Unix()) }
