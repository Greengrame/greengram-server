// Package builtin runs the server's own bots: BotFather, Stickers, Wallet,
// Fragment, SpamBot and BugsBot. They read their getUpdates queue in-process and answer
// through the same Bot API code custom bots use.
package builtin

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/botapi"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/messages"
	"github.com/Greengrame/greengram-server/internal/reports"
)

type Sender interface {
	SendPrivate(ctx context.Context, bot bots.Bot, user int64, o botapi.Out) error
}

type Deps struct {
	Bots    *bots.Service
	Msgs    *messages.Service
	Send    Sender
	BaseURL string
	// set: @SpamBot tells the admins about new reports
	Reports *reports.Service
}

// Run serves every built-in bot until ctx ends.
func Run(ctx context.Context, d Deps) {
	var wg sync.WaitGroup
	for _, kind := range []string{bots.BotFather, bots.Stickers, bots.Wallet, bots.Fragment, bots.SpamBot, bots.BugsBot} {
		b, ok, err := d.Bots.ByKind(ctx, kind)
		if err != nil || !ok {
			if err != nil {
				slog.Warn("builtin: load", "kind", kind, "err", err)
			}
			continue
		}
		r := &runner{Deps: d, bot: b, chats: map[int64]*dialog{}}
		if kind == bots.SpamBot && d.Reports != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r.reportNotices(ctx)
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.loop(ctx)
		}()
	}
	wg.Wait()
}

type runner struct {
	Deps
	bot bots.Bot

	// BotFather's conversations; like Telegram's, they don't outlive a
	// restart of the bot
	mu    sync.Mutex
	chats map[int64]*dialog
}

func (r *runner) loop(ctx context.Context) {
	var offset int64
	for ctx.Err() == nil {
		list, err := r.Bots.WaitUpdates(ctx, r.bot.UserID, offset, 100, 25*time.Second)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("builtin: updates", "bot", r.bot.Username, "err", err)
				time.Sleep(time.Second)
			}
			continue
		}
		done := make([]int64, 0, len(list))
		for _, u := range list {
			offset = u.ID + 1
			if err := r.handle(ctx, u); err != nil {
				slog.Warn("builtin: update", "bot", r.bot.Username, "err", err)
			}
			done = append(done, u.ID)
		}
		// confirm now, not on the next poll: a restart in between would
		// answer the same messages twice
		if len(done) > 0 {
			if err := r.Bots.Drop(ctx, r.bot.UserID, done); err != nil && ctx.Err() == nil {
				slog.Warn("builtin: confirm", "bot", r.bot.Username, "err", err)
			}
		}
	}
}

func (r *runner) handle(ctx context.Context, u bots.Update) error {
	if u.Channel {
		return nil
	}
	if u.Kind == "callback_query" {
		cb, ok, err := r.Bots.Callback(ctx, u.QueryID)
		if err != nil || !ok {
			return err
		}
		return r.press(ctx, cb)
	}
	if u.Kind != "message" {
		return nil
	}
	list, err := r.Msgs.Get(ctx, r.bot.UserID, []int{u.MsgID})
	if err != nil || len(list) == 0 {
		return err
	}
	m := list[0]
	if m.Out {
		return nil
	}
	switch r.bot.Kind {
	case bots.BotFather:
		return r.botfather(ctx, m.From, strings.TrimSpace(m.Text))
	case bots.SpamBot:
		return r.spamBot(ctx, m.From, strings.TrimSpace(m.Text))
	}
	return r.app(ctx, m.From, m.Text)
}

func (r *runner) say(ctx context.Context, user int64, o botapi.Out) error {
	return r.Send.SendPrivate(ctx, r.bot, user, o)
}

// openButton launches the bot's mini app.
func (r *runner) openButton(label string) tg.ReplyMarkupClass {
	url := strings.TrimRight(r.BaseURL, "/") + bots.AppPaths[r.bot.Kind]
	return &tg.ReplyInlineMarkup{Rows: []tg.KeyboardInlineButtonRow{{Buttons: []tg.KeyboardInlineButton{{
		Text: label, Type: &tg.InlineButtonTypeWebView{URL: url},
	}}}}}
}

var appText = map[string][2]string{
	bots.BugsBot:  {"Open Bugs and Suggestions to report a problem or suggest a feature. Reports are submitted through the miniapp.", "Open Bugs and Suggestions"},
	bots.Stickers: {"Open Stickers to create and manage sticker and emoji packs.", "Open Stickers"},
	bots.Wallet:   {"Open Wallet to view your balances, transfer crypto and exchange currencies.", "Open Wallet"},
	bots.Fragment: {"Open Fragment to buy, sell and manage usernames and phone numbers.", "Open Fragment"},
}

// app is Stickers, Wallet and Fragment: everything happens in the mini
// app, the chat only points there (greengram-web did the same on /start).
func (r *runner) app(ctx context.Context, user int64, text string) error {
	t := appText[r.bot.Kind]
	return r.say(ctx, user, botapi.Out{Text: t[0], Markup: r.openButton(t[1])})
}

func (r *runner) press(ctx context.Context, cb bots.Callback) error {
	switch r.bot.Kind {
	case bots.BotFather:
		return r.botfatherPress(ctx, cb)
	case bots.SpamBot:
		return r.spamPress(ctx, cb)
	}
	return r.Bots.Answer(ctx, r.bot.UserID, cb.ID, "", false, "", 0)
}
