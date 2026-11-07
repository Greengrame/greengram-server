package e2e

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Greengrame/greengram-server/internal/account"
)

// Finishing one sign-in flow from several requests at once gives one
// session, and a registration flow one account.
func TestFlowFinishesOnce(t *testing.T) {
	s := start(t)
	ctx := context.Background()
	c := account.Client{IP: "203.0.113.9", UserAgent: "race"}
	const n = 6

	ada, err := s.accounts.RegisterUsername(ctx, "ada_race", "Ada", "correct horse 4", c)
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.accounts.StartFlow(ctx, "qr", "", c)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.accounts.ApproveQR(ctx, ada.User.ID, f.QR); err != nil {
		t.Fatal(err)
	}
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		sessions int
	)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, sess, err := s.accounts.PollQR(ctx, f.Token, c); err == nil && sess != nil {
				mu.Lock()
				sessions++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	var rows int
	if err := s.db.Pool.QueryRow(ctx, "SELECT count(*) FROM sessions WHERE user_id=$1", ada.User.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	// RegisterUsername's own session plus one from the QR flow
	if sessions != 1 || rows != 2 {
		t.Fatalf("QR flow polled %d times at once: %d sessions handed out, %d rows", n, sessions, rows)
	}

	f, err = s.accounts.StartFlow(ctx, "phone", "", c)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.accounts.SetIntent(ctx, f.Token, "register"); err != nil {
		t.Fatal(err)
	}
	if state, err := s.accounts.GivePhone(ctx, f.Token, "+15550177", c); err != nil || state != "registration" {
		t.Fatalf("phone: %q %v", state, err)
	}
	var made int
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := account.Registration{FirstName: "Twin", Username: fmt.Sprintf("twin_%d", i), Password: "correct horse 5"}
			if _, err := s.accounts.Register(ctx, f.Token, r, account.Client{IP: fmt.Sprintf("203.0.113.%d", 20+i)}); err == nil {
				mu.Lock()
				made++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if err := s.db.Pool.QueryRow(ctx, "SELECT count(*) FROM users WHERE username LIKE 'twin_%'").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if made != 1 || rows != 1 {
		t.Fatalf("registration flow finished %d times at once: %d ok, %d accounts", n, made, rows)
	}
}
