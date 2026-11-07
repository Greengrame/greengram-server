package bots

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// channel is the LISTEN name the triggers and Answer notify on. Payloads:
// "<bot id>" for new updates, "q<query id>" for an answered callback.
const channel = "greengram_bots"

// Notifier fans Postgres notifications out to waiters in this process.
// Waiters also recheck on a timer, so a dropped LISTEN connection only
// makes things slower, never lost.
type Notifier struct {
	db *pgxpool.Pool

	mu sync.Mutex
	m  map[string]map[chan struct{}]bool
}

func newNotifier(db *pgxpool.Pool) *Notifier {
	return &Notifier{db: db, m: map[string]map[chan struct{}]bool{}}
}

// Run listens until ctx ends, reconnecting after errors.
func (n *Notifier) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := n.listen(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("bots: listen", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func (n *Notifier) listen(ctx context.Context) error {
	pc, err := n.db.Acquire(ctx)
	if err != nil {
		return err
	}
	// the connection carries LISTEN state; don't hand it back to the pool
	conn := pc.Hijack()
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, "LISTEN "+channel); err != nil {
		return err
	}
	for {
		note, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		n.fire(note.Payload)
	}
}

func (n *Notifier) fire(key string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for ch := range n.m[key] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// subscribe returns a channel that gets a value when key fires. Subscribe
// before checking the database, then wait, so nothing slips between.
func (n *Notifier) subscribe(key string) (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	n.mu.Lock()
	if n.m[key] == nil {
		n.m[key] = map[chan struct{}]bool{}
	}
	n.m[key][ch] = true
	n.mu.Unlock()
	return ch, func() {
		n.mu.Lock()
		delete(n.m[key], ch)
		if len(n.m[key]) == 0 {
			delete(n.m, key)
		}
		n.mu.Unlock()
	}
}

// poll calls check until it reports done or the deadline passes, waking
// early on key.
func (n *Notifier) poll(ctx context.Context, key string, deadline time.Time, check func() (bool, error)) error {
	ch, done := n.subscribe(key)
	defer done()
	for {
		ok, err := check()
		if err != nil || ok {
			return err
		}
		left := time.Until(deadline)
		if left <= 0 {
			return nil
		}
		t := time.NewTimer(min(left, time.Second))
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-ch:
		case <-t.C:
		}
		t.Stop()
	}
}
