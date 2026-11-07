package edge

import (
	"context"
	"crypto/aes"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/gotd/ige"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
)

const handlerTimeout = time.Minute

// Call describes the request a handler is running for.
type Call struct {
	AuthKeyID int64
	PermKeyID int64 // the account key: AuthKeyID itself, or the perm key a temp key is bound to
	SessionID int64
	MsgID     int64
	Layer     int
	Client    ClientInfo
	Remote    net.Addr
	// TestBackend is the key's exchange: 10000+dc or not, nil when unknown
	TestBackend *bool

	sess      *session
	noUpdates bool
}

// Session is the client session the call came in on, for pushing updates.
func (c *Call) Session() Session { return Session{c.sess} }

// WantsUpdates is false for invokeWithoutUpdates: helper connections
// (media, a bot's one-off call) that must not get the update stream.
func (c *Call) WantsUpdates() bool { return !c.noUpdates }

type callKey struct{}

func CallFrom(ctx context.Context) *Call {
	c, _ := ctx.Value(callKey{}).(*Call)
	return c
}

// call runs one RPC once. A re-sent msg_id gets the stored result (or
// nothing while it is still running) instead of a second execution.
func (s *Server) call(ctx context.Context, sess *session, msgID int64, body []byte) {
	sess.mu.Lock()
	if _, ok := sess.calls[msgID]; ok {
		sess.mu.Unlock()
		return
	}
	now := s.clock.Now()
	sess.dropOldCalls(now)
	cl := &call{at: now, done: make(chan struct{})}
	sess.calls[msgID] = cl
	c := sess.conn
	sess.mu.Unlock()

	// the refusal goes in the ledger like any answer, so a re-send of the
	// same msg_id doesn't get another go
	if wait := s.rpcFlood(sess.keyID, c, now); wait > 0 {
		s.finish(ctx, sess, cl, floodWait(msgID, wait))
		return
	}
	var remote net.Addr
	if c != nil {
		remote = c.client
	}
	// detached from the connection: a reply that misses its socket waits in
	// unacked for the reconnect
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handlerTimeout)
	body = append([]byte(nil), body...)
	go func() {
		defer cancel()
		s.finish(ctx, sess, cl, s.invoke(ctx, sess, msgID, body, remote))
	}()
}

func (s *Server) finish(ctx context.Context, sess *session, cl *call, result []byte) {
	sess.mu.Lock()
	cl.result = result
	cl.at = s.clock.Now()
	sess.mu.Unlock()
	close(cl.done)
	if err := s.sendRaw(ctx, sess, result, true, true); err != nil {
		slog.Debug("edge: send rpc_result", "err", err)
	}
}

// invoke admits, dispatches and encodes; the return value is a whole
// rpc_result message.
func (s *Server) invoke(ctx context.Context, sess *session, msgID int64, body []byte, remote net.Addr) []byte {
	sess.mu.Lock()
	profile := sess.profile
	client := sess.client
	bound := sess.key.PermID // a bind may land while we run
	used := sess.key.Used
	testBackend := sess.key.TestBackend
	sess.mu.Unlock()
	if !used {
		s.markUsed(ctx, sess.keyID)
	}

	opts := tlprofile.AdmissionOptions{ExpandGZIP: gunzip}
	b := &bin.Buffer{Buf: body}
	var (
		adm tlprofile.Admission
		err error
	)
	// Until a client shows invokeWithLayer we assume the newest layer; an
	// invokeWithLayer anywhere in the wrapper chain overrides it.
	assumed := profile
	if assumed == 0 {
		assumed = tlprofile.ProfileCanonical
	}
	adm, err = s.rpc.AdmitDefaultWithOptions(assumed, b, opts)
	if err != nil {
		return rpcError(msgID, admissionError(err, body))
	}
	if p, ok := adm.ProfileEvidence(); ok && p != profile {
		profile = p
		sess.mu.Lock()
		sess.profile = p
		sess.mu.Unlock()
		s.updateKey(sess.keyID, func(k *keyRecord) { k.Layer = int(p) })
		if err := s.keys.SetAuthKeyLayer(ctx, sess.keyID, int(p)); err != nil {
			slog.Warn("edge: save layer", "err", err)
		}
	}

	if slog.Default().Enabled(ctx, slog.LevelDebug) {
		_, name, _ := tlprofile.SemanticName(adm.Call().Method())
		start := time.Now()
		defer func() {
			slog.Debug("edge: rpc", "method", name, "layer", adm.Call().Profile(), "key", sess.keyID, "temp", bound != 0,
				"ms", time.Since(start).Milliseconds())
		}()
	}

	perm := bound
	if perm == 0 {
		perm = sess.keyID
	}
	// Web A's file connections share the auth key and never say
	// invokeWithoutUpdates. Fed the stream, they deliver a message twice
	// and can beat the main session's send reply, which leaves the
	// client's own copy pending.
	_, method, _ := tlprofile.SemanticName(adm.Call().Method())
	ctx = context.WithValue(ctx, callKey{}, &Call{
		AuthKeyID:   sess.keyID,
		PermKeyID:   perm,
		SessionID:   sess.id,
		MsgID:       msgID,
		Layer:       int(adm.Call().Profile()),
		Client:      client,
		Remote:      remote,
		TestBackend: testBackend,
		sess:        sess,
		noUpdates:   strings.HasPrefix(method, "upload."),
	})
	res, err := s.rpc.Dispatch(ctx, adm)
	if err != nil {
		var rpcErr *tgerr.Error
		switch {
		case errors.As(err, &rpcErr):
			return rpcError(msgID, &mt.RPCError{ErrorCode: rpcErr.Code, ErrorMessage: rpcErr.Message})
		case errors.Is(err, tlprofile.ErrHandlerNotRegistered):
			_, name, _ := tlprofile.SemanticName(adm.Call().Method())
			slog.Info("edge: method not implemented", "method", name)
			return rpcError(msgID, &mt.RPCError{ErrorCode: 400, ErrorMessage: "METHOD_NOT_IMPLEMENTED"})
		default:
			_, name, _ := tlprofile.SemanticName(adm.Call().Method())
			slog.Error("edge: handler failed", "method", name, "err", err)
			return rpcError(msgID, &mt.RPCError{ErrorCode: 500, ErrorMessage: "INTERNAL"})
		}
	}
	var out bin.Buffer
	out.PutID(mt.RPCResultTypeID)
	out.PutLong(msgID)
	if err := res.Encode(&out); err != nil {
		_, name, _ := tlprofile.SemanticName(adm.Call().Method())
		slog.Error("edge: encode result", "method", name, "layer", profile, "err", err)
		return rpcError(msgID, &mt.RPCError{ErrorCode: 500, ErrorMessage: "INTERNAL"})
	}
	return out.Buf
}

func admissionError(err error, body []byte) *mt.RPCError {
	// an old Android method we know and answer with a refusal (adaptAndroid)
	var rpcErr *tgerr.Error
	if errors.As(err, &rpcErr) {
		return &mt.RPCError{ErrorCode: rpcErr.Code, ErrorMessage: rpcErr.Message}
	}
	switch {
	case errors.Is(err, tlprofile.ErrProfileRequired):
		// official clients answer this by re-sending invokeWithLayer(initConnection(...))
		return &mt.RPCError{ErrorCode: 400, ErrorMessage: "CONNECTION_NOT_INITED"}
	case errors.Is(err, tlprofile.ErrUnknownRPCMethod):
		id, _ := (&bin.Buffer{Buf: body}).PeekID()
		slog.Info("edge: unknown method", "id", id)
		return &mt.RPCError{ErrorCode: 400, ErrorMessage: "INPUT_METHOD_INVALID"}
	case errors.Is(err, tlprofile.ErrProfileConflict):
		return &mt.RPCError{ErrorCode: 400, ErrorMessage: "CONNECTION_LAYER_INVALID"}
	}
	slog.Debug("edge: bad request", "err", err)
	return &mt.RPCError{ErrorCode: 400, ErrorMessage: "INPUT_REQUEST_INVALID"}
}

func rpcError(msgID int64, e *mt.RPCError) []byte {
	var out bin.Buffer
	out.PutID(mt.RPCResultTypeID)
	out.PutLong(msgID)
	e.Encode(&out)
	return out.Buf
}

// consumeWrappers picks up initConnection and invokeWithoutUpdates, and
// holds invokeAfterMsg(s) back until the calls they name are done: calls run
// concurrently, and the apps send a chat's messages chained that way.
func (s *Server) consumeWrappers(ctx context.Context, adm tlprofile.Admission, next tlprofile.Next) error {
	c := CallFrom(ctx)
	for i := range adm.WrapperCount() {
		w, _ := adm.Wrapper(i)
		if c != nil && w.Semantic() == tlprofile.SemanticMethodInvokeWithoutUpdates {
			c.noUpdates = true
		}
		if c != nil && (w.Semantic() == tlprofile.SemanticMethodInvokeAfterMsg || w.Semantic() == tlprofile.SemanticMethodInvokeAfterMsgs) {
			if err := c.waitAfter(ctx, w); err != nil {
				return err
			}
		}
		if w.Semantic() != tlprofile.SemanticMethodInitConnection || c == nil {
			continue
		}
		info := ClientInfo{
			APIID:         wrapperInt(w, "api_id"),
			DeviceModel:   wrapperString(w, "device_model"),
			SystemVersion: wrapperString(w, "system_version"),
			AppVersion:    wrapperString(w, "app_version"),
			LangCode:      wrapperString(w, "lang_code"),
		}
		c.Client = info
		c.sess.mu.Lock()
		c.sess.client = info
		c.sess.mu.Unlock()
	}
	return next(ctx)
}

func wrapperString(w tlprofile.Wrapper, name string) string {
	v, _, _, _ := w.Value(name)
	s, _ := v.(string)
	return s
}

func wrapperInt(w tlprofile.Wrapper, name string) int {
	v, _, _, _ := w.Value(name)
	n, _ := v.(int)
	return n
}

// waitAfter blocks until the calls an invokeAfterMsg(s) names have
// finished. Only earlier msg_ids count, so two calls can't wait on each
// other; ids we never saw (or already forgot) don't hold anything up.
func (c *Call) waitAfter(ctx context.Context, w tlprofile.Wrapper) error {
	var ids []int64
	v, _, _, _ := w.Value("msg_id")
	if id, ok := v.(int64); ok {
		ids = append(ids, id)
	}
	v, _, _, _ = w.Value("msg_ids")
	if list, ok := v.([]int64); ok {
		ids = append(ids, list...)
	}
	for _, id := range ids {
		if id >= c.MsgID {
			continue
		}
		c.sess.mu.Lock()
		prev := c.sess.calls[id]
		c.sess.mu.Unlock()
		if prev == nil {
			continue
		}
		select {
		case <-prev.done:
		case <-ctx.Done():
			return tgerr.New(400, "MSG_WAIT_TIMEOUT")
		}
	}
	return nil
}

func (s *Server) registerInternal() {
	s.Handle(tlprofile.SemanticMethodAuthBindTempAuthKey, func(ctx context.Context, req bin.Object) (any, error) {
		return s.bindTempAuthKey(ctx, req.(*tg.AuthBindTempAuthKeyRequest))
	})
}

// bindTempAuthKey ties the PFS key this request came in on to the device's
// permanent key. The proof is a bind_auth_key_inner encrypted with the
// permanent key (MTProto 1.0 KDF); without checking it anyone could claim
// another device's permanent key and the account signed in on it.
func (s *Server) bindTempAuthKey(ctx context.Context, req *tg.AuthBindTempAuthKeyRequest) (bool, error) {
	c := CallFrom(ctx)
	invalid := tgerr.New(400, "ENCRYPTED_MESSAGE_INVALID")
	perm, ok, err := s.authKey(ctx, req.PermAuthKeyID)
	if err != nil {
		return false, err
	}
	if !ok || perm.PermID != 0 || req.PermAuthKeyID == c.AuthKeyID {
		return false, tgerr.New(400, "PERM_AUTH_KEY_INVALID")
	}
	var msg crypto.EncryptedMessage
	if err := msg.Decode(&bin.Buffer{Buf: req.EncryptedMessage}); err != nil ||
		msg.AuthKeyID != perm.Key.ID || len(msg.EncryptedData)%aes.BlockSize != 0 {
		return false, invalid
	}
	key, iv := crypto.KeysV1(perm.Key.Value, msg.MsgKey)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return false, invalid
	}
	plain := make([]byte, len(msg.EncryptedData))
	ige.DecryptBlocks(block, iv[:], plain, msg.EncryptedData)

	// random(16) msg_id(8) seq_no(4) length(4) bind_auth_key_inner(40)
	const header, body = 32, 40
	if len(plain) < header+body || binary.LittleEndian.Uint32(plain[28:32]) != body ||
		crypto.MessageKeyV1(plain[:header+body]) != msg.MsgKey ||
		int64(binary.LittleEndian.Uint64(plain[16:24])) != c.MsgID {
		return false, invalid
	}
	var inner crypto.BindAuthKeyInner
	if err := inner.Decode(&bin.Buffer{Buf: plain[header : header+body]}); err != nil {
		return false, invalid
	}
	if inner.Nonce != req.Nonce || inner.PermAuthKeyID != req.PermAuthKeyID || inner.ExpiresAt != req.ExpiresAt ||
		inner.TempAuthKeyID != c.AuthKeyID || inner.TempSessionID != c.SessionID {
		return false, invalid
	}
	expires := time.Unix(int64(req.ExpiresAt), 0)
	if err := s.keys.BindTempAuthKey(ctx, c.AuthKeyID, req.PermAuthKeyID, expires); err != nil {
		return false, err
	}
	// the exchange's expires_in wins if it ends sooner
	s.updateKey(c.AuthKeyID, func(k *keyRecord) {
		k.PermID, k.Used = req.PermAuthKeyID, true
		if k.ExpiresAt.IsZero() || expires.Before(k.ExpiresAt) {
			k.ExpiresAt = expires
		}
	})
	s.updateKey(req.PermAuthKeyID, func(k *keyRecord) { k.Used = true })
	// every session on the temp key, not just this one: they all carry a
	// copy of the key record
	s.sessions.forKey(c.AuthKeyID, func(sess *session) { sess.key.PermID, sess.key.Used = req.PermAuthKeyID, true })
	return true, nil
}

// markUsed keeps the key out of the prune for handshakes nobody used.
func (s *Server) markUsed(ctx context.Context, keyID int64) {
	if err := s.keys.MarkAuthKeyUsed(ctx, keyID); err != nil {
		slog.Warn("edge: mark key used", "err", err)
		return
	}
	s.updateKey(keyID, func(k *keyRecord) { k.Used = true })
	s.sessions.forKey(keyID, func(sess *session) { sess.key.Used = true })
}

// destroyAuthKey forgets the key and, for an account key, what it signed in
// to: the row would otherwise outlive any way to sign out of it. A temp key
// is only a stand-in for its perm key, so its sign-in stays.
func (s *Server) destroyAuthKey(ctx context.Context, sess *session) error {
	sess.mu.Lock()
	isPerm := sess.key.PermID == 0
	sess.mu.Unlock()
	if isPerm && s.OnKeyDestroyed != nil {
		if err := s.OnKeyDestroyed(ctx, sess.keyID); err != nil {
			return err
		}
	}
	if err := s.keys.DeleteAuthKey(ctx, sess.keyID); err != nil {
		return err
	}
	s.forgetKey(sess.keyID)
	s.sessions.dropKey(sess.keyID)
	if isPerm {
		// bound temp keys cascade away in the store
		for _, temp := range s.sessions.boundTo(sess.keyID) {
			s.forgetKey(temp)
			s.sessions.dropKey(temp)
		}
	}
	var ok bin.Buffer
	ok.PutID(destroyAuthKeyOkID)
	if err := s.sendRaw(ctx, sess, ok.Buf, false, false); err != nil {
		return err
	}
	return errClose
}
