package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"testing"

	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/messages"
)

func TestBotFileRoundtrip(t *testing.T) {
	var s = start(t)
	var ada = s.signUp(t, "fileowner")
	var ctx = context.Background()
	var service = bots.New(s.db.Pool)
	var bot bots.Bot
	var token, otherToken string
	var err error
	var status int
	var body bytes.Buffer
	var writer *multipart.Writer
	var file io.Writer
	var response *http.Response
	var envelope map[string]any
	var result, document, download map[string]any
	var raw []byte
	var payload = []byte("Greengram bot upload and download")
	bot, token, err = service.Create(ctx, ada.id, "Files", "roundtrip_files_bot")
	if err != nil {
		t.Fatal(err)
	}
	_, otherToken, err = service.Create(ctx, ada.id, "Other", "roundtrip_other_bot")
	if err != nil {
		t.Fatal(err)
	}
	_, err = messages.New(s.db.Pool).Send(ctx, ada.id, bot.UserID, messages.Draft{Text: "/start", RandomID: 1})
	if err != nil {
		t.Fatal(err)
	}
	writer = multipart.NewWriter(&body)
	writer.WriteField("chat_id", strconv.FormatInt(ids.Client(ada.id), 10))
	writer.WriteField("caption", "Your file")
	file, err = writer.CreateFormFile("document", "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	file.Write(payload)
	writer.Close()
	response, err = http.Post(s.http.URL+"/bot"+token+"/sendDocument", writer.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	err = json.NewDecoder(response.Body).Decode(&envelope)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || envelope["ok"] != true {
		t.Fatalf("upload: %v %v", envelope, err)
	}
	result = envelope["result"].(map[string]any)
	document = result["document"].(map[string]any)
	if result["caption"] != "Your file" || document["file_name"] != "hello.txt" {
		t.Fatalf("message: %v", result)
	}
	download = s.bot(t, token, "getFile", map[string]any{"file_id": document["file_id"]}).(map[string]any)
	response, err = http.Get(s.http.URL + "/file/bot" + token + "/" + download["file_path"].(string))
	if err != nil {
		t.Fatal(err)
	}
	raw, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !bytes.Equal(raw, payload) {
		t.Fatalf("download: status %d bytes %q error %v", response.StatusCode, raw, err)
	}
	s.bot(t, token, "sendDocument", map[string]any{"chat_id": ids.Client(ada.id), "document": document["file_id"]})
	_, status = s.botCall(t, otherToken, "getFile", map[string]any{"file_id": document["file_id"]})
	if status != 400 {
		t.Fatal("another bot reused the file ID")
	}
	_, status = s.botCall(t, token, "sendMessage", map[string]any{"chat_id": ids.Client(ada.id), "text": "private", "ephemeral_message_parameters": map[string]any{"callback_query_id": "123"}})
	if status != 400 {
		t.Fatal("ephemeral parameters were silently ignored")
	}
}
