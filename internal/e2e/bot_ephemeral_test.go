package e2e

import (
	"context"
	"strconv"
	"testing"

	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/messages"
)

func TestBotEphemeralCallbackReply(t *testing.T) {
	var s = start(t)
	var ada = s.signUp(t, "ephemeralowner")
	var ctx = context.Background()
	var service = bots.New(s.db.Pool)
	var bot bots.Bot
	var token string
	var err error
	var query int64
	var response map[string]any
	var id int
	var record bots.Ephemeral
	var count, before, status int
	bot, token, err = service.Create(ctx, ada.id, "Ephemeral", "ephemeral_reply_bot")
	if err != nil {
		t.Fatal(err)
	}
	_, err = messages.New(s.db.Pool).Send(ctx, ada.id, bot.UserID, messages.Draft{Text: "/start", RandomID: 1})
	if err != nil {
		t.Fatal(err)
	}
	query, err = service.Press(ctx, bots.Callback{Bot: bot.UserID, User: ada.id, ChatID: ada.id, MsgID: 1, Data: []byte("hello")})
	if err != nil {
		t.Fatal(err)
	}
	err = s.db.Pool.QueryRow(ctx, "SELECT count(*) FROM messages WHERE owner_id=$1", ada.id).Scan(&before)
	if err != nil {
		t.Fatal(err)
	}
	response = s.bot(t, token, "sendMessage", map[string]any{"text": "Only you can see this", "ephemeral_message_parameters": map[string]any{"callback_query_id": strconv.FormatInt(query, 10)}}).(map[string]any)
	id = int(num(response["ephemeral_message_id"]))
	if id <= 0 || num(response["message_id"]) != 0 {
		t.Fatalf("ephemeral response: %v", response)
	}
	record, err = service.Ephemeral(ctx, bot.UserID, ada.id, ada.id, false, id)
	if err != nil || record.Message.Message != "Only you can see this" {
		t.Fatalf("stored ephemeral: %v %v", record, err)
	}
	err = s.db.Pool.QueryRow(ctx, "SELECT count(*) FROM messages WHERE owner_id=$1", ada.id).Scan(&count)
	if err != nil || count != before {
		t.Fatalf("ephemeral message was written to ordinary history: %d %d %v", count, before, err)
	}
	s.bot(t, token, "editEphemeralMessageText", map[string]any{"chat_id": ids.Client(ada.id), "receiver_user_id": ids.Client(ada.id), "ephemeral_message_id": id, "text": "Updated privately"})
	record, err = service.Ephemeral(ctx, bot.UserID, ada.id, ada.id, false, id)
	if err != nil || record.Message.Message != "Updated privately" {
		t.Fatalf("edited ephemeral: %v %v", record, err)
	}
	s.bot(t, token, "deleteEphemeralMessage", map[string]any{"chat_id": ids.Client(ada.id), "receiver_user_id": ids.Client(ada.id), "ephemeral_message_id": id})
	_, err = service.Ephemeral(ctx, bot.UserID, ada.id, ada.id, false, id)
	if err == nil {
		t.Fatal("deleted ephemeral remains accessible")
	}
	_, status = s.botCall(t, token, "sendMessage", map[string]any{"chat_id": ids.Client(ada.id), "text": "must not be public", "ephemeral_message_parameters": "invalid json"})
	if status != 400 {
		t.Fatal("invalid ephemeral parameters were ignored")
	}
}
