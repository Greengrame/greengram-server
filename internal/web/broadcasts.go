package web

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/messages"
)

// Sender fans an announcement out from the announcements account
// (internal/api); it returns at once and works in the background.
type Sender interface {
	Broadcast(ctx context.Context, actor int64, audience, text string) (id int64, recipients int, err error)
}

// Broadcasts is the admin console's announcements, at greengram-web's
// paths: GET and POST /api/admin/broadcasts. People get them from 777000.
type Broadcasts struct {
	Accounts *account.Service
	Bots     *bots.Service
	Sender   Sender

	mu   sync.Mutex
	last time.Time
}

func (b *Broadcasts) Register(mux *http.ServeMux) {
	a := &Auth{Accounts: b.Accounts}
	mux.HandleFunc("GET /api/admin/broadcasts", a.handle(b.list))
	mux.HandleFunc("POST /api/admin/broadcasts", a.handle(b.send))
}

func (b *Broadcasts) admin(q *req) (account.User, error) {
	u, _, err := b.Accounts.SessionUser(q.ctx, q.cookie("session"))
	if err != nil {
		return u, err
	}
	if u.Role != "admin" {
		return u, &account.Error{Status: 403, Message: "Only administrators can do that."}
	}
	return u, nil
}

func (b *Broadcasts) list(q *req) (any, error) {
	if _, err := b.admin(q); err != nil {
		return nil, err
	}
	if aud := q.r.URL.Query().Get("audience"); aud != "" {
		if !slices.Contains(bots.Audiences, aud) {
			aud = "all"
		}
		list, err := b.Bots.Recipients(q.ctx, aud)
		return map[string]any{"audience": aud, "recipients": len(list)}, err
	}
	list, err := b.Bots.Broadcasts(q.ctx, 25)
	if list == nil {
		list = []bots.Broadcast{}
	}
	return map[string]any{"broadcasts": list}, err
}

func (b *Broadcasts) send(q *req) (any, error) {
	u, err := b.admin(q)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(q.str("text"))
	if text == "" || utf8.RuneCountInString(text) > messages.MaxLength {
		return nil, &account.Error{Status: 400, Message: "Write an announcement of 1–4096 characters."}
	}
	aud := q.str("audience")
	if !slices.Contains(bots.Audiences, aud) {
		return nil, &account.Error{Status: 400, Message: "Choose who receives the announcement."}
	}
	// one at a time, like greengram-web: a slip of the finger shouldn't
	// reach everyone twice
	b.mu.Lock()
	if time.Since(b.last) < 30*time.Second {
		b.mu.Unlock()
		return nil, &account.Error{Status: 429, Message: "Wait a few seconds before sending another announcement."}
	}
	b.last = time.Now()
	b.mu.Unlock()
	id, n, err := b.Sender.Broadcast(q.ctx, u.ID, aud, text)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "id": id, "recipients": n}, nil
}
