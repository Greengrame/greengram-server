package builtin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/botapi"
	"github.com/Greengrame/greengram-server/internal/bots"
)

// @SpamBot, like Telegram's: the one chat a frozen account can still write
// in, where it finds out what's wrong and appeals. Admins decide in the
// panel; internal/admin sends the answer.

const (
	spamNoLimits = "Good news, no limits are currently applied to your account. You're free as a bird!"
	spamFrozen   = "Your account was frozen by the moderators of this server. You can still sign in and read your chats, but you can't send messages or change anything.\n\nIf you think this is a mistake, you can appeal and a moderator will review it."
	spamAsk      = "Tell us why you think the freeze is a mistake. Your next message goes to the moderators as your appeal.\n\nSend /cancel to stop."
	spamFiled    = "Thank you. Your appeal is with the moderators, and you'll get their answer in this chat."
	spamWaiting  = "Your appeal is with the moderators. You'll get their answer in this chat."

	appealButton = "appeal"
	appealCmd    = "appeal"
	// the admins' notice carries the appeal; the message limit is 4096
	appealQuoted = 3500
)

func (r *runner) spamBot(ctx context.Context, user int64, text string) error {
	st, err := r.Bots.AppealState(ctx, user)
	if err != nil {
		return err
	}
	pending := r.dialog(user) != nil
	r.setDialog(user, nil)
	if !st.Frozen {
		return r.say(ctx, user, botapi.Out{Text: spamNoLimits})
	}
	if pending && text != "" && !strings.HasPrefix(text, "/") {
		return r.fileAppeal(ctx, user, text)
	}
	return r.spamStatus(ctx, user, st)
}

// spamStatus says where a frozen account stands and offers what it can do.
func (r *runner) spamStatus(ctx context.Context, user int64, st bots.AppealState) error {
	now := time.Now()
	switch {
	case st.Open:
		return r.say(ctx, user, botapi.Out{Text: spamWaiting})
	case st.Cooling(now):
		hours := int(st.RejectedAt.Add(bots.AppealCooldown).Sub(now).Hours()) + 1
		text := "Your last appeal was rejected."
		if st.Reason != "" {
			text = "Your last appeal was rejected: " + st.Reason
		}
		return r.say(ctx, user, botapi.Out{Text: fmt.Sprintf("%s\n\nYou can appeal again in %d h.", text, hours)})
	}
	return r.say(ctx, user, botapi.Out{Text: spamFrozen, Markup: &tg.ReplyInlineMarkup{Rows: []tg.KeyboardInlineButtonRow{{
		Buttons: []tg.KeyboardInlineButton{button("Submit an appeal", appealButton)},
	}}}})
}

func (r *runner) spamPress(ctx context.Context, cb bots.Callback) error {
	if err := r.Bots.Answer(ctx, r.bot.UserID, cb.ID, "", false, "", 0); err != nil && !errors.Is(err, bots.ErrQueryInvalid) {
		return err
	}
	if string(cb.Data) != appealButton {
		return nil
	}
	st, err := r.Bots.AppealState(ctx, cb.User)
	if err != nil {
		return err
	}
	if !st.Frozen {
		return r.say(ctx, cb.User, botapi.Out{Text: spamNoLimits})
	}
	if st.Open || st.Cooling(time.Now()) {
		return r.spamStatus(ctx, cb.User, st)
	}
	r.setDialog(cb.User, &dialog{cmd: appealCmd})
	return r.say(ctx, cb.User, botapi.Out{Text: spamAsk})
}

func (r *runner) fileAppeal(ctx context.Context, user int64, text string) error {
	ok, err := r.Bots.FileAppeal(ctx, user, text)
	if err != nil {
		return err
	}
	if !ok {
		st, err := r.Bots.AppealState(ctx, user)
		if err != nil {
			return err
		}
		return r.spamStatus(ctx, user, st)
	}
	if err := r.say(ctx, user, botapi.Out{Text: spamFiled}); err != nil {
		slog.Warn("spambot: confirm appeal", "user", user, "err", err)
	}
	notice := fmt.Sprintf("New appeal from a frozen account: %s.\n\n%s\n\nDecide in the panel: %s/admin#appeals",
		r.who(ctx, user), quoted(text), strings.TrimRight(r.BaseURL, "/"))
	r.tellAdmins(ctx, botapi.Out{Text: notice})
	return nil
}

// AnswerAppeal tells the person what the admin decided, as the bot.
func AnswerAppeal(ctx context.Context, b *bots.Service, send Sender, user int64, approved bool, reason string) error {
	bot, ok, err := b.ByKind(ctx, bots.SpamBot)
	if err != nil || !ok {
		return err
	}
	text := "Good news: your appeal was accepted and your account is no longer frozen."
	if !approved {
		text = "Your appeal was rejected."
		if reason != "" {
			text = "Your appeal was rejected: " + reason
		}
		text += fmt.Sprintf("\n\nYou can appeal again in %d h.", int(bots.AppealCooldown.Hours()))
	}
	return send.SendPrivate(ctx, bot, user, botapi.Out{Text: text})
}
