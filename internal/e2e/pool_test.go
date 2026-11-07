package e2e

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/settings"
)

// With one connection in the pool, code that asks for another while holding it
// hangs until its deadline on every run, no race needed. That nesting is what
// froze a 4-connection pool in production: a group send fans out to every online
// member at once, and with all connections held everything else waits too.
func TestOneConnectionIsEnough(t *testing.T) {
	inst := make([]byte, 32)
	rand.Read(inst)
	s := startOn(t, dbWith(t, "pool_max_conns=1"), inst)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var (
		people  []person
		clients []*tg.Client
		pushes  []chan tg.UpdatesClass
	)
	for i := range 4 {
		p := s.signUp(t, fmt.Sprintf("pool_%d", i))
		got := make(chan tg.UpdatesClass, 512)
		people, clients, pushes = append(people, p), append(clients, s.live(t, p, got)), append(pushes, got)
	}
	group, err := newGroup(ctx, clients[0], t, "Pool", people[1].id, people[2].id, people[3].id)
	if err != nil {
		t.Fatal(err)
	}

	for round := range 3 {
		// the settings cache empty is the case that hung: it needs a connection too
		s.settings.Forget()
		var wg sync.WaitGroup
		for i, c := range clients {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cctx, done := context.WithTimeout(ctx, 20*time.Second)
				defer done()
				for n := range 3 {
					if _, err := c.MessagesSendMessage(cctx, &tg.MessagesSendMessageRequest{Peer: peerCh(group),
						Message: fmt.Sprintf("r%d c%d n%d", round, i, n), RandomID: int64(1000*round + 100*i + n + 1)}); err != nil {
						t.Errorf("round %d client %d send %d: %v", round, i, n, err)
						return
					}
				}
				if _, _, err := channelHistory(cctx, c, group); err != nil {
					t.Errorf("round %d client %d history: %v", round, i, err)
				}
				if _, err := c.MessagesGetDialogs(cctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 20}); err != nil {
					t.Errorf("round %d client %d dialogs: %v", round, i, err)
				}
			}()
		}
		wg.Wait()
		if t.Failed() {
			return
		}
		// a stuck job only logs, so look for the push
		waitChannelText(t, pushes[1], fmt.Sprintf("r%d c0 n2", round))
	}

	cctx, done := context.WithTimeout(ctx, 20*time.Second)
	defer done()
	if _, err := clients[0].ChannelsUpdateUsername(cctx, &tg.ChannelsUpdateUsernameRequest{Channel: inCh(group), Username: "poolgroup"}); err != nil {
		t.Fatalf("set username: %v", err)
	}
	if _, err := clients[0].ChannelsGetGroupsForDiscussion(cctx); err != nil {
		t.Fatalf("discussion candidates: %v", err)
	}

	c := account.Client{IP: "203.0.113.20", UserAgent: "pool"}
	f, err := s.accounts.StartFlow(cctx, "phone", "", c)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.accounts.SetIntent(cctx, f.Token, "register"); err != nil {
		t.Fatal(err)
	}
	s.settings.Forget()
	if _, err := s.accounts.GivePhone(cctx, f.Token, "+15550188", c); err != nil {
		t.Fatalf("phone: %v", err)
	}
	s.settings.Forget()
	if _, err := s.accounts.Register(cctx, f.Token, account.Registration{FirstName: "Pool", Username: "pool_reg", Password: "correct horse 6"}, c); err != nil {
		t.Fatalf("register: %v", err)
	}
}

// A settings read waiting for a connection must not hold up anyone else, and
// the failure of one must not stick.
func TestSettingsReadWithPoolBusy(t *testing.T) {
	st := dbWith(t, "pool_max_conns=1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := st.Pool.Exec(ctx, `INSERT INTO instance_settings(key, value) VALUES ('sessionDays', '9') ON CONFLICT (key) DO UPDATE SET value=excluded.value`); err != nil {
		t.Fatal(err)
	}
	set := settings.New(st.Pool)
	if got := set.Get(ctx).SessionDays; got != 9 {
		t.Fatalf("session days %d", got)
	}

	conn, err := st.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	set.Forget()
	waiting, stop := context.WithCancel(ctx)
	read := make(chan struct{})
	go func() {
		set.Get(waiting)
		close(read)
	}()
	time.Sleep(100 * time.Millisecond)
	forgot := make(chan struct{})
	go func() {
		set.Forget()
		close(forgot)
	}()
	select {
	case <-forgot:
	case <-time.After(5 * time.Second):
		t.Error("a read waiting for a connection holds the settings lock")
	}
	stop()
	<-read
	conn.Release()
	if got := set.Get(ctx).SessionDays; got != 9 {
		t.Fatalf("the failed read stuck: session days %d", got)
	}
}
