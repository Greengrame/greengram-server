package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/messages"
)

func TestBotWidgets(t *testing.T) {
	var s = start(t)
	var browser = s.browser(t)
	var owner = browser.must("/api/register", map[string]string{"username": "widgetowner", "name": "Widget Owner", "password": "correct horse widget"})
	var user = int64(owner["id"].(float64))
	var service = bots.New(s.db.Pool)
	var ctx = context.Background()
	var bot bots.Bot
	var token string
	var err error
	var result map[string]any
	var widget map[string]any
	var list []messages.Msg
	var key string
	var response *http.Response
	var envelope map[string]any
	var request *http.Request
	var updates []any
	var query map[string]any
	var received = make(chan map[string]any, 1)
	var status int
	var update any
	var address *url.URL
	bot, token, err = service.Create(ctx, user, "Widgets", "widget_test_bot")
	if err != nil {
		t.Fatal(err)
	}
	_, err = messages.New(s.db.Pool).Send(ctx, user, bot.UserID, messages.Draft{Text: "/start", RandomID: 1})
	if err != nil {
		t.Fatal(err)
	}
	result = s.bot(t, token, "sendWidget", map[string]any{"chat_id": user, "html": "<button onclick=\"TelegramWidget.request('hello')\">Hello</button>", "public_data": "initial", "chat_data": "private", "allow_queries": true, "is_shared": true}).(map[string]any)
	widget = result["widget"].(map[string]any)
	if widget["id"] == "" || result["has_protected_content"] != true {
		t.Fatalf("widget: %v", result)
	}
	list, err = messages.New(s.db.Pool).Get(ctx, bot.UserID, []int{int(num(result["message_id"]))})
	if err != nil || len(list) != 1 {
		t.Fatalf("widget message: %v %v", list, err)
	}
	key = media.WidgetKey(list[0].Media)
	if key == "" {
		t.Fatal("widget has no message key")
	}
	response, err = browser.c.Get(s.http.URL + "/__greengram/widgets/" + key)
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(response.Body).Decode(&envelope)
	response.Body.Close()
	if response.StatusCode != 200 || envelope["ok"] != true {
		t.Fatalf("widget read: %d %v", response.StatusCode, envelope)
	}
	response, err = http.Get(s.http.URL + "/__greengram/widgets/" + key)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("anonymous user read a widget")
	}
	s.bot(t, token, "editSharedWidgetData", map[string]any{"widget_id": widget["id"], "public_data": "updated", "cache_time": 5})
	s.bot(t, token, "editMessageWidgetData", map[string]any{"chat_id": user, "message_id": result["message_id"], "chat_data": "updated privately"})
	s.bot(t, token, "sendSharedWidget", map[string]any{"chat_id": user, "widget_id": widget["id"], "chat_data": "second"})
	request, err = http.NewRequest("POST", s.http.URL+"/__greengram/widgets/"+key+"/query", strings.NewReader(`{"data":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	address, _ = url.Parse(s.http.URL)
	request.Header.Set("Origin", address.Scheme+"://"+address.Host)
	request.Header.Set("Content-Type", "application/json")
	go func() {
		var response *http.Response
		var err error
		var body map[string]any
		response, err = browser.c.Do(request)
		if err != nil {
			received <- map[string]any{"error": err.Error()}
			return
		}
		defer response.Body.Close()
		json.NewDecoder(response.Body).Decode(&body)
		received <- body
	}()
	updates = s.bot(t, token, "getUpdates", map[string]any{"timeout": 5, "allowed_updates": []string{"widget_query"}}).([]any)
	for _, update = range updates {
		if update.(map[string]any)["widget_query"] != nil {
			query = update.(map[string]any)["widget_query"].(map[string]any)
		}
	}
	if query == nil {
		updates = s.bot(t, token, "getUpdates", map[string]any{"timeout": 5, "allowed_updates": []string{"widget_query"}}).([]any)
		for _, update = range updates {
			if update.(map[string]any)["widget_query"] != nil {
				query = update.(map[string]any)["widget_query"].(map[string]any)
			}
		}
	}
	if query == nil || query["data"] != "hello" {
		t.Fatalf("widget query: %v", updates)
	}
	s.bot(t, token, "answerWidgetQuery", map[string]any{"widget_query_id": query["id"], "data": "world"})
	select {
	case envelope = <-received:
		if envelope["ok"] != true || envelope["result"].(map[string]any)["data"] != "world" {
			t.Fatalf("widget answer: %v", envelope)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("widget did not receive an answer")
	}
	_, status = s.botCall(t, token, "sendWidget", map[string]any{"chat_id": user, "html": strings.Repeat("a", 32769)})
	if status != 400 {
		t.Fatal("oversized widget HTML accepted")
	}
	request, err = http.NewRequest("POST", s.http.URL+"/__greengram/widgets/"+key+"/query", bytes.NewBufferString(`{"data":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "https://untrusted.example")
	response, err = browser.c.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("cross-origin widget query accepted")
	}
}
