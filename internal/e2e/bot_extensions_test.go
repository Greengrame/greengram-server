package e2e

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/messages"
)

func TestBotShareThemesAndVerification(t *testing.T) {
	var s = start(t)
	var ada = s.signUp(t, "shareowner")
	var ctx = context.Background()
	var service = bots.New(s.db.Pool)
	var bot bots.Bot
	var token string
	var err error
	var response map[string]any
	var verifier tg.BotVerifierSettings
	var verification tg.BotVerification
	var status int
	bot, token, err = service.Create(ctx, ada.id, "Extensions", "extensions_test_bot")
	if err != nil {
		t.Fatal(err)
	}
	_, err = messages.New(s.db.Pool).Send(ctx, ada.id, bot.UserID, messages.Draft{Text: "/start", RandomID: 1})
	if err != nil {
		t.Fatal(err)
	}
	run(t, s.client(ada.storage), func(ctx context.Context, c *tg.Client) error {
		var theme = &tg.ShareThemeEmojiBackdrop{}
		var err error
		theme.SetEmojis([]tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "🌿"}})
		_, err = c.MessagesSetShareTheme(ctx, &tg.MessagesSetShareThemeRequest{Peer: &tg.InputPeerSelf{}, Theme: theme})
		return err
	})
	response = s.bot(t, token, "getChat", map[string]any{"chat_id": ids.Client(ada.id)}).(map[string]any)
	if response["share_theme"].(map[string]any)["type"] != "emoji" {
		t.Fatalf("share theme: %v", response)
	}
	response = s.bot(t, token, "getChatShareThemeBackdrops", nil).(map[string]any)
	if response["backdrops"] == nil {
		t.Fatal("backdrops must be an array")
	}
	_, status = s.botCall(t, token, "verifyUser", map[string]any{"user_id": ids.Client(ada.id)})
	if status == 200 {
		t.Fatal("bot verified a user without an administrator grant")
	}
	verifier = tg.BotVerifierSettings{Icon: 123, Company: "Greengram", CanModifyCustomDescription: true}
	err = service.SetVerifier(ctx, bot.UserID, verifier, true)
	if err != nil {
		t.Fatal(err)
	}
	s.bot(t, token, "verifyUser", map[string]any{"user_id": ids.Client(ada.id), "custom_description": "Verified by <a href=\"https://example.com\">Greengram</a>", "custom_description_parse_mode": "HTML"})
	verification = service.PeerVerification(ctx, ada.id, false)
	if verification.Description.Text != "Verified by Greengram" || len(verification.Description.Entities) != 1 {
		t.Fatalf("formatted verification: %+v", verification)
	}
	_, status = s.botCall(t, token, "verifyUser", map[string]any{"user_id": ids.Client(ada.id), "custom_description": "<b>Not allowed</b>", "custom_description_parse_mode": "HTML"})
	if status != 400 {
		t.Fatal("verification accepted an unsupported entity type")
	}
	s.bot(t, token, "removeUserVerification", map[string]any{"user_id": ids.Client(ada.id)})
	if service.PeerVerification(ctx, ada.id, false).BotID != 0 {
		t.Fatal("verification was not removed")
	}
}
