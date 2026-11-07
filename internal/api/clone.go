package api

import (
	"log/slog"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/channels"
)

// cloneUpdate is a deep copy through TL. td writes the flags field while
// encoding, so one object must not go to the reply and a push job, or to
// two jobs: each gets its own.
func cloneUpdate(u tg.UpdateClass) tg.UpdateClass {
	var b bin.Buffer
	if err := u.Encode(&b); err != nil {
		slog.Warn("api: clone update", "err", err)
		return u
	}
	c, err := tg.DecodeUpdate(&b)
	if err != nil {
		slog.Warn("api: clone update", "err", err)
		return u
	}
	return c
}

// oneEach splits channel events into one per online recipient, each with
// its own copy of the update.
func (a *API) oneEach(events []channels.Event) []channels.Event {
	var out []channels.Event
	for _, e := range events {
		for _, u := range e.Users {
			if a.hub.online(u) {
				out = append(out, channels.Event{Users: []int64{u}, Update: cloneUpdate(e.Update)})
			}
		}
	}
	return out
}
