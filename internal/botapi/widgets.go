package botapi

import (
	"bytes"
	"compress/zlib"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/media"
)

type apiWidget struct {
	ID         string `json:"id,omitempty"`
	HTML       string `json:"html"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	PublicData string `json:"public_data,omitempty"`
	ChatData   string `json:"chat_data,omitempty"`
}

func widgetJSON(m bots.WidgetMessage) apiWidget {
	var out = apiWidget{HTML: m.Widget.HTML, Width: m.Widget.Width, Height: m.Widget.Height, PublicData: m.Widget.PublicData, ChatData: m.ChatData}
	if m.Widget.Shared {
		out.ID = m.Widget.ID
	}
	return out
}

func validateWidgetHTML(html string) error {
	var compressed bytes.Buffer
	var writer *zlib.Writer
	var err error
	if html == "" || !utf8.ValidString(html) || strings.ContainsRune(html, 0) || len(html) > 32768 {
		return bad("INPUT_MEDIA_WIDGET_HTML_INVALID")
	}
	writer, err = zlib.NewWriterLevel(&compressed, zlib.BestCompression)
	if err != nil {
		return err
	}
	_, err = writer.Write([]byte(html))
	if err != nil {
		return err
	}
	err = writer.Close()
	if err != nil {
		return err
	}
	if compressed.Len() > 4096 {
		return bad("INPUT_MEDIA_WIDGET_TOO_LARGE")
	}
	return nil
}

func (s *Server) sendWidget(c *call, shared bool) (any, error) {
	var to chat
	var err error
	var widget bots.Widget
	var message bots.WidgetMessage
	var out Out
	var sent *apiMessage
	var body apiWidget
	to, err = s.chat(c.ctx, c.bot, c.p, "chat_id")
	if err != nil {
		return nil, err
	}
	if to.channel != 0 {
		return nil, bad("widgets can only be sent to private chats")
	}
	err = s.writable(c.ctx, c.bot, to)
	if err != nil {
		return nil, err
	}
	if !validWidgetData(c.p.str("chat_data"), 1024) || !validWidgetData(c.p.str("public_data"), 1024) {
		return nil, bad("widget data is too large")
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
	if shared {
		widget, err = s.Bots.Widget(c.ctx, c.bot.UserID, c.p.str("widget_id"))
		if err != nil {
			return nil, err
		}
		if !widget.Shared {
			return nil, bad("widget is not shared")
		}
	} else {
		widget = bots.Widget{Bot: c.bot.UserID, HTML: c.p.str("html"), PublicData: c.p.str("public_data"), Shared: c.p.bool("is_shared"), AllowQueries: c.p.bool("allow_queries")}
		err = validateWidgetHTML(widget.HTML)
		if err != nil {
			return nil, err
		}
		widget.Width, err = c.p.int("width")
		if err != nil || widget.Width < 0 {
			return nil, bad("invalid width")
		}
		widget.Height, err = c.p.int("height")
		if err != nil || widget.Height < 0 {
			return nil, bad("invalid height")
		}
		widget, err = s.Bots.CreateWidget(c.ctx, widget)
		if err != nil {
			return nil, err
		}
	}
	message, err = s.Bots.CreateWidgetMessage(c.ctx, widget, to.user, c.p.str("chat_data"))
	if err != nil {
		return nil, err
	}
	out.Topic, err = c.p.int("message_thread_id")
	if err != nil || out.Topic < 0 {
		return nil, bad("invalid message_thread_id")
	}
	err = s.readReplyQuote(c, to, &out)
	if err != nil {
		return nil, err
	}
	out.Media = media.WidgetRef(message.ID, strings.TrimRight(s.BaseURL, "/")+"/__greengram/widgets/"+message.ID)
	sent, err = s.send(c.ctx, c.bot, to, out)
	if err != nil {
		return nil, err
	}
	err = s.Bots.SetWidgetMessageID(c.ctx, message.ID, sent.MessageID)
	if err != nil {
		return nil, err
	}
	body = widgetJSON(message)
	sent.Widget, sent.HasProtectedContent = &body, true
	return sent, nil
}

func (s *Server) editWidget(c *call, shared bool) (any, error) {
	var message bots.WidgetMessage
	var widget bots.Widget
	var to chat
	var id, cache int
	var data string
	var publicData *string
	var err error
	var present bool
	if !validWidgetData(c.p.str("public_data"), 1024) || !validWidgetData(c.p.str("chat_data"), 1024) {
		return nil, bad("widget data is too large")
	}
	if shared {
		widget, err = s.Bots.Widget(c.ctx, c.bot.UserID, c.p.str("widget_id"))
		if err != nil {
			return nil, err
		}
		if !widget.Shared {
			return nil, bad("widget is not shared")
		}
		if _, present = c.p["public_data"]; !present {
			return nil, bad("public_data is required")
		}
	} else {
		to, err = s.chat(c.ctx, c.bot, c.p, "chat_id")
		if err != nil {
			return nil, err
		}
		if to.channel != 0 {
			return nil, bad("widgets can only be sent to private chats")
		}
		id, err = c.p.int("message_id")
		if err != nil || id <= 0 {
			return nil, bad("message_id is required")
		}
		message, err = s.Bots.WidgetMessageByID(c.ctx, c.bot.UserID, to.user, id)
		if err != nil {
			return nil, err
		}
		widget = message.Widget
	}
	cache, err = c.p.int("cache_time")
	if err != nil || cache != 0 && cache < 5 {
		return nil, bad("cache_time must be 5-3600 seconds")
	}
	cache = min(cache, 3600)
	if _, present = c.p["public_data"]; present {
		data = c.p.str("public_data")
		publicData = &data
		err = s.Bots.EditWidget(c.ctx, c.bot.UserID, widget.ID, publicData, cache)
		if err != nil {
			return nil, err
		}
	}
	if _, present = c.p["chat_data"]; present && !shared {
		err = s.Bots.EditWidgetMessage(c.ctx, message.ID, c.p.str("chat_data"))
		if err != nil {
			return nil, err
		}
	}
	return true, nil
}

func (s *Server) answerWidgetQuery(c *call) (any, error) {
	var id int64
	var err error
	var data = c.p.str("data")
	if !validWidgetData(data, 1024) {
		return nil, bad("widget answer is too large")
	}
	id, err = c.p.int64("widget_query_id")
	if err != nil || id <= 0 {
		return nil, bad("invalid widget query")
	}
	return true, s.Bots.AnswerWidgetQuery(c.ctx, c.bot.UserID, id, data)
}

func widgetSendMethod(shared bool) method {
	return func(s *Server, c *call) (any, error) { return s.sendWidget(c, shared) }
}

func widgetEditMethod(shared bool) method {
	return func(s *Server, c *call) (any, error) { return s.editWidget(c, shared) }
}

func (s *Server) serveWidget(w http.ResponseWriter, r *http.Request) {
	var key = strings.TrimPrefix(r.URL.Path, "/__greengram/widgets/")
	var query = strings.HasSuffix(key, "/query")
	var walletQuery = strings.HasSuffix(key, "/wallet")
	var cookie *http.Cookie
	var user account.User
	var message bots.WidgetMessage
	var bot bots.Bot
	var found bool
	var p params
	var err error
	var origin *url.URL
	var payload map[string]any
	var id int64
	var answer string
	var apiFailure *apiError
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		write(w, 405, reply{ErrorCode: 405, Description: "Method Not Allowed"})
		return
	}
	cookie, err = r.Cookie("session")
	if err != nil {
		write(w, 401, reply{ErrorCode: 401, Description: "Unauthorized"})
		return
	}
	user, _, err = s.Accounts.SessionUser(r.Context(), cookie.Value)
	if err != nil || user.ID == 0 {
		write(w, 401, reply{ErrorCode: 401, Description: "Unauthorized"})
		return
	}
	key = strings.TrimSuffix(strings.TrimSuffix(key, "/query"), "/wallet")
	message, err = s.Bots.WidgetMessage(r.Context(), key, user.ID, 0)
	if err != nil || message.MessageID == 0 {
		write(w, 404, reply{ErrorCode: 404, Description: "Widget not found"})
		return
	}
	if r.Method == http.MethodGet && !query && !walletQuery {
		write(w, 200, reply{OK: true, Result: map[string]any{"widget": widgetJSON(message), "allow_queries": message.Widget.AllowQueries, "cache_time": message.Widget.CacheTime, "viewer": userJSON(user), "wallet_available": s.Wallet != nil, "bot_name": s.view(r.Context(), bot).user(message.Widget.Bot).FirstName}})
		return
	}
	if (!query && !walletQuery) || r.Method != http.MethodPost {
		write(w, 405, reply{ErrorCode: 405, Description: "Method Not Allowed"})
		return
	}
	origin, err = url.Parse(r.Header.Get("Origin"))
	if err != nil || origin.Host != r.Host {
		write(w, 403, reply{ErrorCode: 403, Description: "Invalid origin"})
		return
	}
	bot, found, err = s.Bots.Get(r.Context(), message.Widget.Bot)
	if err != nil || !found || bot.Frozen || bot.Disabled || !bot.Usable(user.ID) || !message.Widget.AllowQueries {
		write(w, 403, reply{ErrorCode: 403, Description: "Widget queries are disabled"})
		return
	}
	p, err = readParams(r)
	if err != nil || len(p.str("data")) == 0 || !validWidgetData(p.str("data"), 64) {
		write(w, 400, reply{ErrorCode: 400, Description: "Query data must contain 1-64 bytes"})
		return
	}
	payload = map[string]any{"from": userJSON(user), "chat": map[string]any{"id": ids.Client(user.ID), "type": "private", "first_name": user.Name}, "message_id": message.MessageID, "chat_instance": chatInstance(bot.UserID, chat{user: user.ID}), "type": map[string]any{"type": "data"}, "data": p.str("data")}
	if message.Widget.Shared {
		payload["widget_id"] = message.Widget.ID
	}
	if walletQuery {
		id, err = s.widgetWalletQuery(r, user, message, p, payload)
	} else {
		id, err = s.Bots.QueueWidgetQuery(r.Context(), message, user.ID, payload)
	}
	if err != nil {
		apiFailure = asAPIError(err)
		if apiFailure == nil {
			apiFailure = &apiError{500, "Internal Server Error", 0}
		}
		write(w, apiFailure.code, reply{ErrorCode: apiFailure.code, Description: apiFailure.desc})
		return
	}
	answer, err = s.Bots.WaitWidgetAnswer(r.Context(), id)
	if err != nil {
		write(w, 408, reply{ErrorCode: 408, Description: "Widget query timed out"})
		return
	}
	write(w, 200, reply{OK: true, Result: map[string]any{"data": answer, "query_id": strconv.FormatInt(id, 10)}})
}

func validWidgetData(value string, limit int) bool {
	return len(value) <= limit && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
