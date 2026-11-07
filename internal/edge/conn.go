package edge

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/proto/codec"
	"github.com/iamxvbaba/td/transport"

	"github.com/Greengrame/greengram-server/internal/edge/dh"
	"github.com/Greengrame/greengram-server/internal/store"
)

const (
	idleTimeout  = 5 * time.Minute // clients ping every ~60s
	writeTimeout = 15 * time.Second
)

type conn struct {
	tc      transport.Conn
	remote  net.Addr
	client  net.Addr   // who is behind remote, see Server.WebSocket
	floodIP netip.Addr // see floodIP
	floodOK bool

	wmu sync.Mutex

	mu       sync.Mutex
	sessions map[*session]struct{}
	pingKill *time.Timer // ping_delay_disconnect
}

func (c *conn) write(ctx context.Context, b *bin.Buffer) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return c.tc.Send(ctx, b)
}

func (c *conn) protoError(ctx context.Context, code int32) error {
	var b bin.Buffer
	b.PutInt32(-code)
	return c.write(ctx, &b)
}

func (c *conn) delayDisconnect(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pingKill != nil {
		c.pingKill.Stop()
	}
	c.pingKill = time.AfterFunc(d, func() { c.tc.Close() })
}

func (s *Server) serveConn(ctx context.Context, tc transport.Conn, remote, client net.Addr) {
	c := &conn{tc: tc, remote: remote, client: client, sessions: map[*session]struct{}{}}
	c.floodIP, c.floodOK = floodIP(remote)
	defer func() {
		tc.Close()
		c.mu.Lock()
		if c.pingKill != nil {
			c.pingKill.Stop()
		}
		list := c.sessions
		c.sessions = nil
		c.mu.Unlock()
		for sess := range list {
			sess.mu.Lock()
			if sess.conn == c {
				sess.conn = nil
			}
			sess.mu.Unlock()
		}
	}()

	b := new(bin.Buffer)
	for {
		b.Reset()
		rctx, cancel := context.WithTimeout(ctx, idleTimeout)
		err := tc.Recv(rctx, b)
		cancel()
		if err != nil {
			return
		}
		var id [8]byte
		if err := b.PeekN(id[:], 8); err != nil {
			return
		}
		if id == ([8]byte{}) {
			if plainAck(b) {
				continue
			}
			frame, err := s.exchange(ctx, c, b)
			if err != nil {
				slog.Debug("edge: key exchange failed", "remote", remote, "err", err)
				return
			}
			if frame == nil {
				continue
			}
			b.ResetTo(frame)
		}
		if err := s.handleEncrypted(ctx, c, b); err != nil {
			if !errors.Is(err, errClose) {
				slog.Debug("edge: closing connection", "remote", remote, "err", err)
			}
			return
		}
	}
}

var errClose = errors.New("close connection")

// exchange runs the DH handshake starting from the frame already read. It
// returns a frame to handle as encrypted when the client switched to an
// existing key mid-way.
func (s *Server) exchange(ctx context.Context, c *conn, first *bin.Buffer) ([]byte, error) {
	if s.handshakeFlood(c, s.clock.Now()) {
		c.protoError(ctx, codec.CodeTransportFlood)
		return nil, errors.New("handshake flood")
	}
	pc := &primedConn{Conn: c.tc, first: first.Copy()}
	res, err := dh.NewServer(pc, s.key, s.cipher.Rand(), s.clock, s.ourDC).Run(ctx)
	var enc *exchange.UnexpectedEncryptedError
	if errors.As(err, &enc) {
		return enc.Frame, nil
	}
	var se *exchange.ServerExchangeError
	if errors.As(err, &se) {
		c.protoError(ctx, se.Code)
	}
	if err != nil {
		return nil, err
	}
	var expires time.Time
	if res.ExpiresIn > 0 {
		expires = s.clock.Now().Add(time.Duration(res.ExpiresIn) * time.Second)
	}
	if err := s.keys.PutAuthKey(ctx, res.Key, res.ServerSalt, expires, testBackend(res.DC)); err != nil {
		return nil, err
	}
	slog.Debug("edge: new auth key", "id", store.KeyID(res.Key.ID), "remote", c.remote, "temp", res.ExpiresIn > 0)
	return nil, nil
}

// testBackend says whether an exchange came from a client on Telegram's
// test backend, which names 10000+dc (negative for media keys).
func testBackend(dc int) *bool {
	if dc == 0 {
		return nil
	}
	if dc < 0 {
		dc = -dc
	}
	t := dc >= 10000
	return &t
}

// primedConn replays the first exchange frame and skips the plain msgs_ack
// some clients send during the handshake.
type primedConn struct {
	transport.Conn
	first []byte
}

func (p *primedConn) Recv(ctx context.Context, b *bin.Buffer) error {
	if p.first != nil {
		b.ResetTo(p.first)
		p.first = nil
		return nil
	}
	for {
		if err := p.Conn.Recv(ctx, b); err != nil {
			return err
		}
		if !plainAck(b) {
			return nil
		}
	}
}

func plainAck(b *bin.Buffer) bool {
	raw := b.Raw()
	return len(raw) >= 24 && binary.LittleEndian.Uint64(raw[:8]) == 0 &&
		binary.LittleEndian.Uint32(raw[20:24]) == mt.MsgsAckTypeID
}

func (s *Server) handleEncrypted(ctx context.Context, c *conn, b *bin.Buffer) error {
	var m crypto.EncryptedMessage
	if err := m.DecodeWithoutCopy(b); err != nil {
		return err
	}
	keyID := store.KeyID(m.AuthKeyID)
	k, ok, err := s.authKey(ctx, keyID)
	if err != nil {
		return err
	}
	now := s.clock.Now()
	if ok && !k.ExpiresAt.IsZero() && !now.Before(k.ExpiresAt) {
		s.expireKey(ctx, keyID)
		ok = false
	}
	if !ok {
		// the client drops the key and redoes the exchange
		c.protoError(ctx, codec.CodeAuthKeyNotFound)
		return errClose
	}
	data, err := s.cipher.Decrypt(k.Key, &m)
	if err != nil {
		return err // bad msg_key: the spec says drop the connection
	}
	sess, _ := s.sessions.get(k, keyID, data.SessionID, now)
	s.attach(ctx, sess, c)

	if !s.salts.valid(keyID, data.Salt, now) && !handshakeSalt(k, data.Salt, now) {
		return s.sendService(ctx, sess, &mt.BadServerSalt{
			BadMsgID:      data.MessageID,
			BadMsgSeqno:   int(data.SeqNo),
			ErrorCode:     48,
			NewServerSalt: s.salts.current(keyID, now),
		}, false)
	}
	// announced on the first message with a good salt, not the first
	// message: the bad_server_salt round trip comes before it
	sess.mu.Lock()
	announce := !sess.announced
	sess.announced = true
	sess.mu.Unlock()
	if announce {
		if err := s.sendService(ctx, sess, &mt.NewSessionCreated{
			FirstMsgID: firstMsgID(data.MessageID, data.Data()),
			UniqueID:   int64(binary.LittleEndian.Uint64(m.MsgKey[:8])),
			ServerSalt: s.salts.current(keyID, now),
		}, true); err != nil {
			return err
		}
	}
	if err := s.process(ctx, sess, data.MessageID, data.SeqNo, data.Data(), false); err != nil {
		return err
	}
	return s.flushAcks(ctx, sess)
}

// firstMsgID is the lowest msg_id in a frame. Clients re-send every request
// below new_session_created's first_msg_id as lost, and a container's own
// id is above those of the messages in it.
func firstMsgID(id int64, body []byte) int64 {
	var mc proto.MessageContainer
	if t, err := (&bin.Buffer{Buf: body}).PeekID(); err != nil || t != proto.MessageContainerTypeID ||
		mc.Decode(&bin.Buffer{Buf: body}) != nil {
		return id
	}
	for _, m := range mc.Messages {
		id = min(id, m.ID)
	}
	return id
}

// attach points the session's output at c and re-sends what the client
// hasn't acked yet; that's how replies survive a reconnect.
func (s *Server) attach(ctx context.Context, sess *session, c *conn) {
	sess.mu.Lock()
	sess.active = s.clock.Now()
	sess.parked = false
	if sess.conn == c {
		sess.mu.Unlock()
		return
	}
	sess.conn = c
	resend := sess.pending()
	sess.mu.Unlock()

	c.mu.Lock()
	if c.sessions != nil {
		c.sessions[sess] = struct{}{}
	}
	c.mu.Unlock()
	for _, body := range resend {
		s.sendRaw(ctx, sess, body, true, true)
	}
}

// ourDC accepts the DC in p_q_inner_data with or without the test-backend
// offset (our Android build says 10002, everyone else 2) and the negative
// media-only form.
func (s *Server) ourDC(dc int) bool {
	if dc < 0 {
		dc = -dc
	}
	return dc%10000 == s.dc%10000
}
