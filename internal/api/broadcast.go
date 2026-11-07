package api

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/messages"
)

// Fan-out pace: Telegram lets a bot broadcast about 30 messages a second.
const (
	broadcastBatch = 30
	broadcastPause = time.Second
)

// Broadcast sends text from the announcements account to everyone in
// audience: one ordinary private message each, so it lands in the 777000
// chat with its push and journal entry. It returns once the run is
// recorded; the sending goes on in the background, one run at a time.
func (a *API) Broadcast(ctx context.Context, actor int64, audience, text string) (int64, int, error) {
	from := ids.Announcements()
	if a.bots == nil || from == 0 {
		return 0, 0, &account.Error{Status: 503, Message: "Announcements are not set up."}
	}
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > messages.MaxLength {
		return 0, 0, &account.Error{Status: 400, Message: "Write an announcement of 1–4096 characters."}
	}
	if !a.broadcasting.CompareAndSwap(false, true) {
		return 0, 0, &account.Error{Status: 409, Message: "The last announcement is still being sent."}
	}
	list, err := a.bots.Recipients(ctx, audience)
	if err == nil && len(list) == 0 {
		err = &account.Error{Status: 400, Message: "Nobody matches this audience."}
	}
	var id int64
	if err == nil {
		id, err = a.bots.StartBroadcast(ctx, actor, audience, text, len(list))
	}
	if err != nil {
		a.broadcasting.Store(false)
		return 0, 0, err
	}
	go func() {
		defer a.broadcasting.Store(false)
		ctx := context.Background()
		sent := 0
		for i, to := range list {
			s, err := a.msgs.Send(ctx, from, to, messages.Draft{Text: text})
			if err != nil {
				slog.Warn("api: broadcast", "id", id, "to", to, "err", err)
			} else {
				sent++
				a.deliver(s.Events, edge.Session{})
			}
			if i%broadcastBatch == broadcastBatch-1 && i < len(list)-1 {
				a.bots.BroadcastProgress(ctx, id, sent, false)
				time.Sleep(broadcastPause)
			}
		}
		if err := a.bots.BroadcastProgress(ctx, id, sent, true); err != nil {
			slog.Warn("api: broadcast progress", "id", id, "err", err)
		}
	}()
	return id, len(list), nil
}
