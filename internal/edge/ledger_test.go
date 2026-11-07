package edge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/store"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{RSA: k, Secret: make([]byte, 32), Keys: NewMemKeys(), ExchangeDC: 2})
}

func encode(t *testing.T, o bin.Encoder) []byte {
	t.Helper()
	var b bin.Buffer
	if err := o.Encode(&b); err != nil {
		t.Fatal(err)
	}
	return b.Buf
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A request re-sent with the same msg_id runs once; the copy is answered
// from the ledger. With no connection attached the answer waits in unacked,
// which is what gets re-sent after a reconnect.
func TestDuplicateRunsOnce(t *testing.T) {
	s := newTestServer(t)
	var runs atomic.Int32
	s.Handle(tlprofile.SemanticMethodHelpGetNearestDC, func(context.Context, bin.Object) (any, error) {
		runs.Add(1)
		return &tg.NearestDC{Country: "XX", ThisDC: 2, NearestDC: 2}, nil
	})

	sess, _ := s.sessions.get(store.AuthKey{}, 1, 1, s.clock.Now())
	body := encode(t, &tg.InvokeWithLayerRequest{Layer: 231, Query: &tg.HelpGetNearestDCRequest{}})
	msgID := s.msgIDs.New(proto.MessageFromClient)
	ctx := context.Background()

	if err := s.process(ctx, sess, msgID, 1, body, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.calls[msgID] != nil && sess.calls[msgID].result != nil
	})
	if err := s.process(ctx, sess, msgID, 1, body, false); err != nil {
		t.Fatal(err)
	}
	if n := runs.Load(); n != 1 {
		t.Fatalf("handler ran %d times", n)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.unacked) != 2 {
		t.Fatalf("expected the result twice in unacked, got %d", len(sess.unacked))
	}
	if sess.profile != 231 {
		t.Fatalf("session layer %d, want 231 from invokeWithLayer", sess.profile)
	}
}

func TestMsgIDRules(t *testing.T) {
	s := newTestServer(t)
	now := s.clock.Now()
	good := int64(proto.NewMessageID(now, proto.MessageFromClient))
	cases := []struct {
		name  string
		id    int64
		seq   int32
		typID uint32
		want  int
	}{
		{"ok", good, 1, tg.HelpGetConfigRequestTypeID, 0},
		{"server parity", good + 1, 1, tg.HelpGetConfigRequestTypeID, badMsgIDBits},
		{"too old", int64(proto.NewMessageID(now.Add(-10*time.Minute), proto.MessageFromClient)), 1, tg.HelpGetConfigRequestTypeID, badMsgIDTooLow},
		{"from the future", int64(proto.NewMessageID(now.Add(time.Minute), proto.MessageFromClient)), 1, tg.HelpGetConfigRequestTypeID, badMsgIDTooHigh},
		{"request with even seqno", good, 2, tg.HelpGetConfigRequestTypeID, badSeqNotOdd},
		{"container with odd seqno", good, 3, proto.MessageContainerTypeID, badSeqNotEven},
	}
	for _, c := range cases {
		if got := s.checkMsgID(c.id, c.seq, c.typID); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}

func TestSalts(t *testing.T) {
	s := salts{secret: []byte("0123456789abcdef0123456789abcdef")}
	now := time.Unix(1_800_000_000, 0)
	cur := s.current(7, now)
	if !s.valid(7, cur, now) || !s.valid(7, cur, now.Add(saltPeriod)) {
		t.Fatal("current salt must stay valid into the next period")
	}
	if s.valid(7, cur, now.Add(2*saltPeriod)) {
		t.Fatal("salt still valid two periods later")
	}
	if s.valid(8, cur, now) {
		t.Fatal("salt leaked to another key")
	}
	if f := s.future(7, now, 3); len(f) != 3 || f[0].Salt != cur {
		t.Fatalf("future salts %+v", f)
	}

	k := store.AuthKey{HandshakeSalt: 99, CreatedAt: now}
	if !handshakeSalt(k, 99, now.Add(time.Minute)) {
		t.Fatal("fresh key rejected its handshake salt")
	}
	if handshakeSalt(k, 99, now.Add(3*saltPeriod)) {
		t.Fatal("handshake salt still valid hours later")
	}
}

func TestOurDC(t *testing.T) {
	s := &Server{dc: 10002}
	for dc, want := range map[int]bool{2: true, 10002: true, -2: true, -10002: true, 3: false, 10003: false, 12: false} {
		if s.ourDC(dc) != want {
			t.Errorf("dc %d: want %v", dc, want)
		}
	}
}

// Answers are only needed while the client may still re-send the request
// (300s of msg_id validity); a busy session must not keep every result it
// ever sent until it hits callsKept.
func TestSweepTrimsOldCalls(t *testing.T) {
	s := newTestServer(t)
	now := s.clock.Now()
	sess, _ := s.sessions.get(store.AuthKey{}, 1, 1, now)
	sess.mu.Lock()
	sess.calls[1] = &call{at: now.Add(-2 * callRetain), result: make([]byte, 1<<20), done: make(chan struct{})}
	sess.calls[2] = &call{at: now, result: []byte{1}, done: make(chan struct{})}
	sess.calls[3] = &call{at: now.Add(-2 * callRetain), done: make(chan struct{})} // still running
	sess.mu.Unlock()

	s.Sweep(time.Hour)
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.dead {
		t.Fatal("active session swept")
	}
	if _, ok := sess.calls[1]; ok || sess.calls[2] == nil || sess.calls[3] == nil {
		t.Fatalf("calls after sweep: %v", sess.calls)
	}
}

type captureConn struct {
	mu   sync.Mutex
	sent [][]byte
}

func (c *captureConn) Send(_ context.Context, b *bin.Buffer) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, b.Copy())
	return nil
}
func (c *captureConn) Recv(context.Context, *bin.Buffer) error { return nil }
func (c *captureConn) Close() error                            { return nil }

// new_session_created names the first message the server got. tgnet and
// tdlib re-send every request below first_msg_id as lost, so when a session
// opens with a container it must be the lowest inner id, not the
// container's (which is always higher): that re-ran the container's calls.
func TestNewSessionFirstMsgIDInContainer(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	var k crypto.Key
	rand.Read(k[:])
	ak := k.WithID()
	s.keys.PutAuthKey(ctx, ak, 0, time.Time{}, nil)
	keyID := store.KeyID(ak.ID)

	ping := func(id int64) []byte { return encode(t, &mt.PingRequest{PingID: id}) }
	first := s.msgIDs.New(proto.MessageFromClient)
	second := s.msgIDs.New(proto.MessageFromClient)
	cont := s.msgIDs.New(proto.MessageFromClient)
	p1, p2 := ping(1), ping(2)
	body := encode(t, &proto.MessageContainer{Messages: []proto.Message{
		{ID: first, SeqNo: 1, Bytes: len(p1), Body: p1}, {ID: second, SeqNo: 3, Bytes: len(p2), Body: p2},
	}})
	var frame bin.Buffer
	client := crypto.NewClientCipher(rand.Reader)
	if err := client.Encrypt(ak, crypto.EncryptedMessageData{
		Salt: s.salts.current(keyID, s.clock.Now()), SessionID: 42, MessageID: cont, SeqNo: 4,
		MessageDataLen: int32(len(body)), MessageDataWithPadding: body,
	}, &frame); err != nil {
		t.Fatal(err)
	}
	tc := &captureConn{}
	if err := s.handleEncrypted(ctx, &conn{tc: tc, sessions: map[*session]struct{}{}}, &frame); err != nil {
		t.Fatal(err)
	}
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if len(tc.sent) == 0 {
		t.Fatal("nothing sent")
	}
	data, err := client.DecryptFromBuffer(ak, &bin.Buffer{Buf: tc.sent[0]})
	if err != nil {
		t.Fatal(err)
	}
	var created mt.NewSessionCreated
	if err := created.Decode(&bin.Buffer{Buf: data.Data()}); err != nil {
		t.Fatal(err)
	}
	if created.FirstMsgID != first {
		t.Fatalf("first_msg_id %x, want the first inner message %x (container %x)", created.FirstMsgID, first, cont)
	}
}

// The key cache holds keys in use, not every key seen since start: apps
// make fresh temp keys all the time.
func TestSweepForgetsIdleKeys(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	var k crypto.Key
	rand.Read(k[:])
	ak := k.WithID()
	s.keys.PutAuthKey(ctx, ak, 0, time.Time{}, nil)
	id := store.KeyID(ak.ID)
	rec, _, _ := s.authKey(ctx, id)
	s.sessions.get(rec, id, 1, s.clock.Now().Add(-time.Hour))

	s.Sweep(time.Minute)
	s.keyMu.Lock()
	n := len(s.keyCache)
	s.keyMu.Unlock()
	if n != 0 {
		t.Fatalf("%d keys still cached after their sessions went", n)
	}
	if _, ok, _ := s.authKey(ctx, id); !ok {
		t.Fatal("key gone from the store")
	}
}

// One auth key can't grow sessions without bound: a client spraying fresh
// session_ids on one connection would otherwise eat the server's memory.
func TestSessionsPerKeyCapped(t *testing.T) {
	s := newTestServer(t)
	now := s.clock.Now()
	first, _ := s.sessions.get(store.AuthKey{}, 1, 1, now)
	first.seen.add(1)
	for i := range int64(sessionsPerKey + 50) {
		s.sessions.get(store.AuthKey{}, 1, i+2, now.Add(time.Duration(i+1)*time.Millisecond))
	}
	other, _ := s.sessions.get(store.AuthKey{}, 2, 1, now)
	n := 0
	s.sessions.forKey(1, func(*session) { n++ })
	if n != sessionsPerKey {
		t.Fatalf("key has %d sessions, want %d", n, sessionsPerKey)
	}
	first.mu.Lock()
	dead, kept := first.dead, len(first.seen.set)
	first.mu.Unlock()
	if !dead {
		t.Fatal("least recently used session survived")
	}
	// the push hub may hold the handle a while longer
	if kept != 0 {
		t.Fatal("dead session keeps its state")
	}
	if other.dead {
		t.Fatal("another key's session was evicted")
	}
}

// Binding a temp key moves every session on it to the account key, not just
// the one the bind came in on; calls already running on that session read
// the perm id safely.
func TestBindReachesEverySession(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	newKey := func() crypto.AuthKey {
		var k crypto.Key
		rand.Read(k[:])
		ak := k.WithID()
		if err := s.keys.PutAuthKey(ctx, ak, 0, time.Time{}, nil); err != nil {
			t.Fatal(err)
		}
		return ak
	}
	perm, temp := newKey(), newKey()
	permID, tempID := store.KeyID(perm.ID), store.KeyID(temp.ID)
	rec, _, err := s.authKey(ctx, tempID)
	if err != nil {
		t.Fatal(err)
	}

	var seen sync.Map // session id -> PermKeyID
	s.Handle(tlprofile.SemanticMethodHelpGetNearestDC, func(ctx context.Context, _ bin.Object) (any, error) {
		c := CallFrom(ctx)
		seen.Store(c.SessionID, c.PermKeyID)
		return &tg.NearestDC{Country: "XX", ThisDC: 2, NearestDC: 2}, nil
	})
	a, _ := s.sessions.get(rec, tempID, 1, s.clock.Now())
	b, _ := s.sessions.get(rec, tempID, 2, s.clock.Now())

	bindID := s.msgIDs.New(proto.MessageFromClient)
	inner := &crypto.BindAuthKeyInner{Nonce: 7, TempAuthKeyID: tempID, PermAuthKeyID: permID, TempSessionID: 1, ExpiresAt: int(time.Now().Add(time.Hour).Unix())}
	enc, err := crypto.EncryptBindMessage(rand.Reader, perm, bindID, inner)
	if err != nil {
		t.Fatal(err)
	}
	bind := encode(t, &tg.InvokeWithLayerRequest{Layer: 231, Query: &tg.AuthBindTempAuthKeyRequest{
		PermAuthKeyID: permID, Nonce: 7, ExpiresAt: inner.ExpiresAt, EncryptedMessage: enc,
	}})
	if err := s.process(ctx, a, bindID, 1, bind, false); err != nil {
		t.Fatal(err)
	}
	// a call racing the bind on the same session
	if err := s.process(ctx, a, s.msgIDs.New(proto.MessageFromClient), 3, encode(t, &tg.HelpGetNearestDCRequest{}), false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.calls[bindID].result != nil
	})
	if err := s.process(ctx, b, s.msgIDs.New(proto.MessageFromClient), 1, encode(t, &tg.InvokeWithLayerRequest{Layer: 231, Query: &tg.HelpGetNearestDCRequest{}}), false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, ok := seen.Load(int64(2)); return ok })
	if got, _ := seen.Load(int64(2)); got != permID {
		t.Fatalf("other session on the temp key calls as %v, want perm key %d", got, permID)
	}
}

// invokeAfterMsg must not start before the call it names has finished:
// Android and Desktop send a chat's messages that way to keep their order.
func TestInvokeAfterMsgWaits(t *testing.T) {
	s := newTestServer(t)
	release := make(chan struct{})
	var firstDone, secondRan, early atomic.Bool
	s.Handle(tlprofile.SemanticMethodHelpGetNearestDC, func(context.Context, bin.Object) (any, error) {
		<-release
		firstDone.Store(true)
		return &tg.NearestDC{Country: "XX", ThisDC: 2, NearestDC: 2}, nil
	})
	s.Handle(tlprofile.SemanticMethodHelpGetCDNConfig, func(context.Context, bin.Object) (any, error) {
		if !firstDone.Load() {
			early.Store(true)
		}
		secondRan.Store(true)
		return &tg.CDNConfig{}, nil
	})

	sess, _ := s.sessions.get(store.AuthKey{}, 1, 1, s.clock.Now())
	ctx := context.Background()
	first := s.msgIDs.New(proto.MessageFromClient)
	if err := s.process(ctx, sess, first, 1, encode(t, &tg.InvokeWithLayerRequest{Layer: 231, Query: &tg.HelpGetNearestDCRequest{}}), false); err != nil {
		t.Fatal(err)
	}
	second := s.msgIDs.New(proto.MessageFromClient)
	body := encode(t, &tg.InvokeAfterMsgRequest{MsgID: first, Query: &tg.HelpGetCDNConfigRequest{}})
	if err := s.process(ctx, sess, second, 3, body, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	waitFor(t, secondRan.Load)
	if early.Load() {
		t.Fatal("invokeAfterMsg ran before the call it waits for")
	}
}
