package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/builtin"
)

func TestBugsBot(t *testing.T) {
	var s = start(t)
	var ada = s.signUp(t, "ada")
	var pushes = make(chan tg.UpdatesClass, 64)
	var c = s.live(t, ada, pushes)
	var ctx, cancel = context.WithTimeout(context.Background(), time.Minute)
	var service = bots.New(s.db.Pool)
	var bot *tg.User
	var full *tg.UsersUserFull
	var info tg.BotInfo
	var found bool
	var err error
	var menu *tg.BotMenuButton
	var message *tg.Message
	var markup *tg.ReplyInlineMarkup
	var webview *tg.InlineButtonTypeWebView
	var count int
	var avatars = t.TempDir()
	defer cancel()
	// the picture is Telegram's and comes from the mini apps bundle
	if err = os.WriteFile(filepath.Join(avatars, bots.BugsBot+".jpg"), testPNG(160, 160), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = builtin.SeedAvatars(ctx, avatars, service, s.media); err != nil {
		t.Fatal(err)
	}
	bot = resolve(ctx, t, c, "bugsbot")
	if !bot.Bot || !bot.Verified {
		t.Fatalf("bot identity: %+v", bot)
	}
	if _, found = bot.GetPhoto(); !found {
		t.Fatal("the bot has no avatar")
	}
	full, err = c.UsersGetFullUser(ctx, &tg.InputUser{UserID: bot.ID, AccessHash: bot.AccessHash})
	if err != nil {
		t.Fatal(err)
	}
	info, found = full.FullUser.GetBotInfo()
	if !found {
		t.Fatal("missing bot info")
	}
	menu, found = info.MenuButton.(*tg.BotMenuButton)
	if !found || !strings.HasSuffix(menu.URL, "/bugs") {
		t.Fatalf("miniapp menu: %+v", info.MenuButton)
	}
	if len(info.Commands) != 0 {
		t.Fatal("the miniapp-only bot advertises commands")
	}
	say(ctx, t, c, bot, "/bug")
	message = waitFrom(t, pushes, bot.ID, "Open Bugs and Suggestions")
	markup, found = message.ReplyMarkup.(*tg.ReplyInlineMarkup)
	if !found || len(markup.Rows) != 1 || len(markup.Rows[0].Buttons) != 1 {
		t.Fatal("missing miniapp launch button")
	}
	webview, found = markup.Rows[0].Buttons[0].Type.(*tg.InlineButtonTypeWebView)
	if !found || !strings.HasSuffix(webview.URL, "/bugs") {
		t.Fatal("the welcome button is not a webview")
	}
	say(ctx, t, c, bot, "a private chat message")
	waitFrom(t, pushes, bot.ID, "Open Bugs and Suggestions")
	if err = s.db.Pool.QueryRow(ctx, "SELECT count(*) FROM bug_reports WHERE user_id=$1", ada.id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("a chat message created a report")
	}
}
