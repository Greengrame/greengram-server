package e2e

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/messages"
)

func TestBotRichRepliesAndPrivateTopics(t *testing.T) {
	var s = start(t)
	var ada = s.signUp(t, "topicowner")
	var bob = s.signUp(t, "topicoutsider")
	var ctx = context.Background()
	var service = bots.New(s.db.Pool)
	var bot bots.Bot
	var token string
	var err error
	var response map[string]any
	var quote map[string]any
	var id, thread, status int
	var stored []messages.Msg
	var topics []tg.ForumTopic
	bot, token, err = service.Create(ctx, ada.id, "Topics", "rich_topic_bot")
	if err != nil {
		t.Fatal(err)
	}
	err = service.SetSetting(ctx, bot.UserID, "topics", true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = messages.New(s.db.Pool).Send(ctx, ada.id, bot.UserID, messages.Draft{Text: "/start"})
	if err != nil {
		t.Fatal(err)
	}
	response = s.bot(t, token, "createForumTopic", map[string]any{"chat_id": ids.Client(ada.id), "name": "Private support"}).(map[string]any)
	thread = int(num(response["message_thread_id"]))
	topics, err = messages.New(s.db.Pool).PrivateTopics(ctx, ada.id, bot.UserID, []int{thread}, "", 0, 10)
	if err != nil || len(topics) != 1 {
		t.Fatalf("own topic missing: %v %v", topics, err)
	}
	topics, err = messages.New(s.db.Pool).PrivateTopics(ctx, bob.id, bot.UserID, []int{thread}, "", 0, 10)
	if err != nil || len(topics) != 0 {
		t.Fatalf("topic leaked to another user: %v %v", topics, err)
	}
	response = s.bot(t, token, "sendRichMessage", map[string]any{"chat_id": ids.Client(ada.id), "message_thread_id": thread, "rich_message": map[string]any{"blocks": []any{map[string]any{"type": "paragraph", "text": []any{"Pay to ", map[string]any{"type": "ton_address", "text": "local address", "address": "gram:example"}}}}}}).(map[string]any)
	id = int(num(response["message_id"]))
	if response["rich_message"] == nil || int(num(response["message_thread_id"])) != thread {
		t.Fatalf("rich response: %v", response)
	}
	stored, err = messages.New(s.db.Pool).Get(ctx, bot.UserID, []int{id})
	if err != nil || len(stored) != 1 || len(stored[0].Rich) == 0 || stored[0].Topic != thread {
		t.Fatal("rich message was not persisted")
	}
	response = s.bot(t, token, "sendMessage", map[string]any{"chat_id": ids.Client(ada.id), "message_thread_id": thread, "text": "Quoted reply", "reply_parameters": map[string]any{"message_id": id, "quote_rich_texts": []any{"Pay to "}}}).(map[string]any)
	quote, _ = response["quote"].(map[string]any)
	if quote == nil || quote["rich_texts"] == nil {
		t.Fatalf("missing rich quote: %v", response)
	}
	s.bot(t, token, "closeForumTopic", map[string]any{"chat_id": ids.Client(ada.id), "message_thread_id": thread})
	_, status = s.botCall(t, token, "sendMessage", map[string]any{"chat_id": ids.Client(ada.id), "message_thread_id": thread, "text": "Closed"})
	if status == 200 {
		t.Fatal("sent into a closed private topic")
	}
	s.bot(t, token, "reopenForumTopic", map[string]any{"chat_id": ids.Client(ada.id), "message_thread_id": thread})
	run(t, s.client(ada.storage), func(ctx context.Context, c *tg.Client) error {
		var result *tg.MessagesForumTopics
		var err error
		result, err = c.MessagesGetForumTopicsByID(ctx, &tg.MessagesGetForumTopicsByIDRequest{Peer: &tg.InputPeerUser{UserID: ids.Client(bot.UserID)}, Topics: []int{thread}})
		if err == nil && len(result.Topics) != 1 {
			t.Fatal("native topic link cannot resolve own topic")
		}
		return err
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		var result *tg.MessagesForumTopics
		var err error
		result, err = c.MessagesGetForumTopicsByID(ctx, &tg.MessagesGetForumTopicsByIDRequest{Peer: &tg.InputPeerUser{UserID: ids.Client(bot.UserID)}, Topics: []int{thread}})
		if err == nil && len(result.Topics) != 0 {
			t.Fatal("native topic link leaked to another account")
		}
		return err
	})
	_, err = s.db.Pool.Exec(ctx, "UPDATE bot_private_topics SET deleted=true WHERE bot_id=$1 AND id=$2", bot.UserID, thread)
	if err != nil {
		t.Fatal(err)
	}
	_, status = s.botCall(t, token, "sendMessage", map[string]any{"chat_id": ids.Client(ada.id), "message_thread_id": thread, "text": "Deletion in progress"})
	if status == 200 {
		t.Fatal("sent into a topic being deleted")
	}
	s.bot(t, token, "deleteForumTopic", map[string]any{"chat_id": ids.Client(ada.id), "message_thread_id": thread})
	topics, err = messages.New(s.db.Pool).PrivateTopics(ctx, ada.id, bot.UserID, []int{thread}, "", 0, 10)
	if err != nil || len(topics) != 0 {
		t.Fatal("topic was not deleted")
	}
}

func TestBotSecondaryUsernames(t *testing.T) {
	var s = start(t)
	var ada = s.signUp(t, "secondaryowner")
	var ctx = context.Background()
	var service = bots.New(s.db.Pool)
	var manager bots.Bot
	var managed, second, third bots.Bot
	var token string
	var err error
	var found bool
	manager, token, err = service.Create(ctx, ada.id, "Manager", "secondary_manager_bot")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = service.CreateManaged(ctx, ada.id, manager.UserID, "Managed", "secondarytestai")
	if err == nil {
		t.Fatal("non-Premium owner obtained secondary username")
	}
	_, err = s.db.Pool.Exec(ctx, "UPDATE users SET premium_until=extract(epoch FROM now())::bigint+86400 WHERE id=$1", ada.id)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = service.Create(ctx, ada.id, "Reserved primary", "secondarytestaibot")
	if err != nil {
		t.Fatal(err)
	}
	managed, _, err = service.CreateManaged(ctx, ada.id, manager.UserID, "Managed", "secondarytestai")
	if err != nil {
		t.Fatal(err)
	}
	_, found, err = service.ByUsername(ctx, "secondarytestai")
	if err != nil || !found {
		t.Fatal("secondary bot username does not resolve")
	}
	_, found, err = s.accounts.UserByAlias(ctx, "secondarytestai")
	if err != nil || !found {
		t.Fatal("secondary username does not resolve through contacts")
	}
	second, _, err = service.Create(ctx, ada.id, "Second", "secondary_second_bot")
	if err != nil {
		t.Fatal(err)
	}
	err = service.SetSecondaryUsername(ctx, ada.id, second.UserID, "secondarytestapp")
	if err != nil {
		t.Fatal(err)
	}
	third, _, err = service.Create(ctx, ada.id, "Third", "secondary_third_bot")
	if err != nil {
		t.Fatal(err)
	}
	err = service.SetSecondaryUsername(ctx, ada.id, third.UserID, "secondarytestgame")
	if err == nil {
		t.Fatal("owner assigned secondary usernames to more than two bots")
	}
	found, err = service.Managed(ctx, manager.UserID, managed.UserID)
	if err != nil || !found {
		t.Fatal("managed bot ownership not persisted")
	}
	s.bot(t, token, "replaceManagedBotToken", map[string]any{"user_id": ids.Client(managed.UserID)})
}
