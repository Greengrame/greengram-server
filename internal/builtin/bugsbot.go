package builtin

import (
	"context"
	"fmt"
	"log/slog"
	"unicode/utf8"

	"github.com/Greengrame/greengram-server/internal/botapi"
)

func (r *runner) who(ctx context.Context, user int64) string {
	name, username, err := r.Bots.Appealer(ctx, user)
	if err != nil {
		slog.Warn("builtin: who", "user", user, "err", err)
	}
	if username != "" {
		name += " (@" + username + ")"
	}
	return fmt.Sprintf("%s, id %d", name, user)
}

// quoted fits someone's text into a notice; the message limit is 4096.
func quoted(text string) string {
	if utf8.RuneCountInString(text) > appealQuoted {
		return string([]rune(text)[:appealQuoted]) + "..."
	}
	return text
}

// tellAdmins is how a notice reaches people who aren't watching the panel.
func (r *runner) tellAdmins(ctx context.Context, notice botapi.Out) {
	admins, err := r.Bots.Recipients(ctx, "admins")
	if err != nil {
		slog.Warn("builtin: admins", "bot", r.bot.Username, "err", err)
		return
	}
	for _, admin := range admins {
		if err := r.say(ctx, admin, notice); err != nil {
			slog.Warn("builtin: tell admin", "bot", r.bot.Username, "admin", admin, "err", err)
		}
	}
}
