package edge

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
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

func putKey(t *testing.T, s *Server, expires time.Time) (crypto.AuthKey, int64) {
	t.Helper()
	var k crypto.Key
	rand.Read(k[:])
	ak := k.WithID()
	if err := s.keys.PutAuthKey(context.Background(), ak, 0, expires, nil); err != nil {
		t.Fatal(err)
	}
	return ak, store.KeyID(ak.ID)
}

func frame(t *testing.T, s *Server, ak crypto.AuthKey, body []byte) *bin.Buffer {
	t.Helper()
	var out bin.Buffer
	if err := crypto.NewClientCipher(rand.Reader).Encrypt(ak, crypto.EncryptedMessageData{
		Salt: s.salts.current(store.KeyID(ak.ID), s.clock.Now()), SessionID: 42, MessageID: s.msgIDs.New(proto.MessageFromClient), SeqNo: 1,
		MessageDataLen: int32(len(body)), MessageDataWithPadding: body,
	}, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

// A temp key past its expires_in gets -404 (the apps make a new one) and is
// gone from the store.
func TestExpiredTempKey(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	ak, id := putKey(t, s, s.clock.Now().Add(-time.Second))
	tc := &captureConn{}
	err := s.handleEncrypted(ctx, &conn{tc: tc, sessions: map[*session]struct{}{}}, frame(t, s, ak, encode(t, &mt.PingRequest{PingID: 1})))
	if !errors.Is(err, errClose) {
		t.Fatalf("got %v, want the connection closed", err)
	}
	tc.mu.Lock()
	sent := tc.sent
	tc.mu.Unlock()
	if len(sent) != 1 || int32(binary.LittleEndian.Uint32(sent[0])) != -404 {
		t.Fatalf("sent %x, want -404", sent)
	}
	if _, ok, _ := s.keys.AuthKey(ctx, id); ok {
		t.Fatal("expired key still stored")
	}

	live, _ := putKey(t, s, s.clock.Now().Add(time.Hour))
	if err := s.handleEncrypted(ctx, &conn{tc: &captureConn{}, sessions: map[*session]struct{}{}}, frame(t, s, live, encode(t, &mt.PingRequest{PingID: 1}))); err != nil {
		t.Fatalf("live temp key refused: %v", err)
	}
}

// Sweep prunes expired keys from the store, but not one a session still
// holds (its next message gets -404 instead).
func TestSweepPrunesExpiredKeys(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	past := s.clock.Now().Add(-time.Minute)
	_, gone := putKey(t, s, past)
	_, held := putKey(t, s, past)
	_, perm := putKey(t, s, time.Time{})
	rec, _, _ := s.authKey(ctx, held)
	s.sessions.get(rec, held, 1, s.clock.Now())

	s.Sweep(time.Hour)
	for id, want := range map[int64]bool{gone: false, held: true, perm: true} {
		if _, ok, _ := s.keys.AuthKey(ctx, id); ok != want {
			t.Errorf("key %d stored=%v, want %v", id, ok, want)
		}
	}
}

// Past the per-key budget a call is answered FLOOD_WAIT_X without running,
// and a re-send of it gets the same answer.
func TestRPCFloodWait(t *testing.T) {
	s := newTestServer(t)
	s.floodRates.rpcKey = rate{burst: 2, perSec: 0.5}
	var runs atomic.Int32
	s.Handle(tlprofile.SemanticMethodHelpGetNearestDC, func(context.Context, bin.Object) (any, error) {
		runs.Add(1)
		return &tg.NearestDC{Country: "XX", ThisDC: 2, NearestDC: 2}, nil
	})
	sess, _ := s.sessions.get(store.AuthKey{}, 1, 1, s.clock.Now())
	ctx := context.Background()
	body := encode(t, &tg.InvokeWithLayerRequest{Layer: 231, Query: &tg.HelpGetNearestDCRequest{}})
	var ids []int64
	for range 3 {
		id := s.msgIDs.New(proto.MessageFromClient)
		ids = append(ids, id)
		if err := s.process(ctx, sess, id, 1, body, false); err != nil {
			t.Fatal(err)
		}
	}
	result := func(id int64) []byte {
		var r []byte
		waitFor(t, func() bool {
			sess.mu.Lock()
			defer sess.mu.Unlock()
			r = sess.calls[id].result
			return r != nil
		})
		return r
	}
	for _, id := range ids[:2] {
		result(id)
	}
	var e mt.RPCError
	b := &bin.Buffer{Buf: result(ids[2])[12:]} // rpc_result#f35c6d01 req_msg_id
	if err := e.Decode(b); err != nil {
		t.Fatal(err)
	}
	// two seconds for the next token, less whatever the test took
	if e.ErrorCode != 420 || e.ErrorMessage != "FLOOD_WAIT_2" && e.ErrorMessage != "FLOOD_WAIT_1" {
		t.Fatalf("third call: %d %s", e.ErrorCode, e.ErrorMessage)
	}
	if n := runs.Load(); n != 2 {
		t.Fatalf("handler ran %d times", n)
	}
}

func TestFloodIP(t *testing.T) {
	a, _ := floodIP(&net.TCPAddr{IP: net.ParseIP("2001:db8:1:2:aaaa::1"), Port: 1})
	b, _ := floodIP(&net.TCPAddr{IP: net.ParseIP("2001:db8:1:2:ffff::9"), Port: 2})
	c, _ := floodIP(&net.TCPAddr{IP: net.ParseIP("2001:db8:1:3::1"), Port: 1})
	if a != b || a == c {
		t.Fatalf("/64 buckets: %v %v %v", a, b, c)
	}
	if v4, ok := floodIP(&net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 1}); !ok || v4 != netip.MustParseAddr("203.0.113.7") {
		t.Fatalf("v4: %v %v", v4, ok)
	}
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "::1", "fd00::1"} {
		if _, ok := floodIP(&net.TCPAddr{IP: net.ParseIP(ip)}); ok {
			t.Errorf("%s counted; it's a proxy in front of us", ip)
		}
	}
	if _, ok := floodIP(nil); ok {
		t.Error("nil address counted")
	}

	var f flood
	r := rate{burst: 2, perSec: 1}
	now := time.Unix(1_800_000_000, 0)
	k := floodKey{kind: floodHandshakeIP, ip: a}
	if f.take(k, r, now) != 0 || f.take(k, r, now) != 0 || f.take(k, r, now) == 0 {
		t.Fatal("burst of 2 not enforced")
	}
	if f.take(k, r, now.Add(time.Second)) != 0 {
		t.Fatal("no refill after a second")
	}
	f.sweep(now.Add(time.Minute))
	if len(f.m) != 0 {
		t.Fatal("refilled bucket kept")
	}
}

// After StopPushes (sign-out on the key) a push carrying the epoch read
// before it fails, and pushes still waiting for an ack are dropped while
// rpc results stay.
func TestStopPushes(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	perm, temp := int64(10), int64(11)
	onTemp, _ := s.sessions.get(store.AuthKey{PermID: perm}, temp, 1, s.clock.Now())
	other, _ := s.sessions.get(store.AuthKey{}, 12, 1, s.clock.Now())
	h, ho := Session{onTemp}, Session{other}
	epoch, otherEpoch := h.Epoch(), ho.Epoch()
	upd := &tg.Updates{Updates: []tg.UpdateClass{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}}
	if err := s.Push(ctx, h, epoch, upd); err != nil {
		t.Fatal(err)
	}
	if err := s.sendResult(ctx, onTemp, 4, &mt.RPCAnswerUnknown{}); err != nil {
		t.Fatal(err)
	}

	s.StopPushes(perm)
	if err := s.Push(ctx, h, epoch, upd); !errors.Is(err, ErrSessionGone) {
		t.Fatalf("push with the old epoch: %v", err)
	}
	onTemp.mu.Lock()
	var pushes, results int
	for id := range onTemp.unacked {
		if proto.MessageID(id).Type() == proto.MessageFromServer {
			pushes++
		} else {
			results++
		}
	}
	onTemp.mu.Unlock()
	if pushes != 0 || results != 1 {
		t.Fatalf("unacked after StopPushes: %d pushes, %d results", pushes, results)
	}
	if err := s.Push(ctx, h, h.Epoch(), upd); err != nil {
		t.Fatalf("push with the new epoch: %v", err)
	}
	if err := s.Push(ctx, ho, otherEpoch, upd); err != nil {
		t.Fatalf("another account's key was stopped: %v", err)
	}
}

// A session idle past the sweep with unacked messages stays and gets them on
// reconnect, along with a session gap for what it missed: it takes no pushes
// while parked, and the hub drops its handle until it's back.
func TestSweepKeepsUnacked(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	ak, id := putKey(t, s, time.Time{})
	rec, _, _ := s.authKey(ctx, id)
	sess, _ := s.sessions.get(rec, id, 42, s.clock.Now().Add(-time.Hour))
	sess.announced = true
	h := Session{sess}
	upd := &tg.Updates{Updates: []tg.UpdateClass{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}}
	if err := s.Push(ctx, h, h.Epoch(), upd); err != nil {
		t.Fatal(err)
	}

	s.Sweep(time.Minute)
	sess.mu.Lock()
	dead, n := sess.dead, len(sess.unacked)
	sess.mu.Unlock()
	if dead || n != 1 {
		t.Fatalf("after sweep: dead=%v unacked=%d", dead, n)
	}
	if err := s.Push(ctx, h, h.Epoch(), upd); !errors.Is(err, ErrSessionGone) {
		t.Fatalf("push to a parked session: %v", err)
	}

	tc := &captureConn{}
	if err := s.handleEncrypted(ctx, &conn{tc: tc, sessions: map[*session]struct{}{}}, frame(t, s, ak, encode(t, &mt.PingRequest{PingID: 1}))); err != nil {
		t.Fatal(err)
	}
	client := crypto.NewClientCipher(rand.Reader)
	var pushes, gaps int
	tc.mu.Lock()
	for _, raw := range tc.sent {
		data, err := client.DecryptFromBuffer(ak, &bin.Buffer{Buf: raw})
		if err != nil {
			t.Fatal(err)
		}
		switch id, _ := (&bin.Buffer{Buf: data.Data()}).PeekID(); id {
		case tg.UpdatesTypeID:
			pushes++
		case mt.NewSessionCreatedTypeID:
			gaps++
		}
	}
	tc.mu.Unlock()
	if pushes != 1 || gaps != 1 {
		t.Fatalf("on reconnect: %d pushes, %d session gaps", pushes, gaps)
	}
	if err := s.Push(ctx, h, h.Epoch(), upd); err != nil {
		t.Fatalf("push after coming back: %v", err)
	}
}

// Nobody can come back on a temp key that ran out, unacked or not.
func TestSweepDropsExpiredTempKeySessions(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	_, id := putKey(t, s, s.clock.Now().Add(-time.Second))
	rec, _, _ := s.authKey(ctx, id)
	sess, _ := s.sessions.get(rec, id, 1, s.clock.Now().Add(-time.Hour))
	sess.unacked[1] = []byte{1}
	s.Sweep(time.Minute)
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if !sess.dead {
		t.Fatal("session of an expired key kept")
	}
}

// Past the queue's bound the oldest goes, and the client has to hear about
// it: the next message from it announces a session gap.
func TestQueueTrimAnnouncesGap(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	sess, _ := s.sessions.get(store.AuthKey{}, 1, 1, s.clock.Now())
	sess.announced = true
	h := Session{sess}
	upd := &tg.Updates{Updates: []tg.UpdateClass{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}}
	for range unackedKept {
		if err := s.Push(ctx, h, h.Epoch(), upd); err != nil {
			t.Fatal(err)
		}
	}
	sess.mu.Lock()
	full := sess.announced
	sess.mu.Unlock()
	if !full {
		t.Fatal("gap announced before anything was lost")
	}
	if err := s.Push(ctx, h, h.Epoch(), upd); err != nil {
		t.Fatal(err)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.announced || len(sess.unacked) != unackedKept {
		t.Fatalf("after the trim: announced=%v unacked=%d", sess.announced, len(sess.unacked))
	}
}

func destroyBody() []byte {
	var b bin.Buffer
	b.PutID(destroyAuthKeyID)
	return b.Buf
}

// destroy_auth_key on an account key ends its sign-in and takes the bound
// temp keys along; on a temp key the sign-in stays.
func TestDestroyAuthKey(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	var ended []int64
	s.OnKeyDestroyed = func(_ context.Context, id int64) error {
		ended = append(ended, id)
		return nil
	}
	destroy := func(ak crypto.AuthKey) error {
		return s.handleEncrypted(ctx, &conn{tc: &captureConn{}, sessions: map[*session]struct{}{}}, frame(t, s, ak, destroyBody()))
	}

	permKey, perm := putKey(t, s, time.Time{})
	tempKey, temp := putKey(t, s, s.clock.Now().Add(time.Hour))
	if err := s.keys.BindTempAuthKey(ctx, temp, perm, s.clock.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := destroy(tempKey); !errors.Is(err, errClose) {
		t.Fatalf("temp key: %v", err)
	}
	if len(ended) != 0 {
		t.Fatalf("destroying a temp key ended sign-in %v", ended)
	}
	if _, ok, _ := s.keys.AuthKey(ctx, temp); ok {
		t.Fatal("temp key still stored")
	}

	otherKey, other := putKey(t, s, s.clock.Now().Add(time.Hour))
	if err := s.keys.BindTempAuthKey(ctx, other, perm, s.clock.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.handleEncrypted(ctx, &conn{tc: &captureConn{}, sessions: map[*session]struct{}{}}, frame(t, s, otherKey, encode(t, &mt.PingRequest{PingID: 1}))); err != nil {
		t.Fatal(err)
	}
	if err := destroy(permKey); !errors.Is(err, errClose) {
		t.Fatalf("perm key: %v", err)
	}
	if len(ended) != 1 || ended[0] != perm {
		t.Fatalf("ended %v, want %d", ended, perm)
	}
	if inUse := s.sessions.keys(); inUse[perm] || inUse[other] {
		t.Fatalf("sessions left: %v", inUse)
	}

	// a failure keeps the key, so the client can ask again
	again, id := putKey(t, s, time.Time{})
	s.OnKeyDestroyed = func(context.Context, int64) error { return errors.New("db down") }
	if err := destroy(again); err == nil || errors.Is(err, errClose) {
		t.Fatalf("failed destroy: %v", err)
	}
	if _, ok, _ := s.keys.AuthKey(ctx, id); !ok {
		t.Fatal("key gone though its sign-in wasn't ended")
	}
}
