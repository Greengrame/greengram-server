package edge

import (
	"context"
	"errors"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto"
)

// not in td's mt package
const (
	destroyAuthKeyID   = 0xd1435160
	destroyAuthKeyOkID = 0xf660e1d4
)

// bad_msg_notification codes
const (
	badMsgIDTooLow    = 16
	badMsgIDTooHigh   = 17
	badMsgIDBits      = 18
	badMsgIDDuplicate = 19 // container id reused
	badSeqNotOdd      = 34
	badSeqNotEven     = 35
	badContainer      = 64
)

// process handles one client message, top level or out of a container.
func (s *Server) process(ctx context.Context, sess *session, msgID int64, seqNo int32, body []byte, inContainer bool) error {
	b := &bin.Buffer{Buf: body}
	typeID, err := b.PeekID()
	if err != nil {
		return err
	}
	if code := s.checkMsgID(msgID, seqNo, typeID); code != 0 {
		return s.badMsg(ctx, sess, msgID, seqNo, code)
	}

	sess.mu.Lock()
	fresh := sess.seen.add(msgID)
	sess.mu.Unlock()
	if !fresh {
		return s.duplicate(ctx, sess, msgID)
	}

	switch typeID {
	case proto.MessageContainerTypeID:
		if inContainer {
			return s.badMsg(ctx, sess, msgID, seqNo, badContainer)
		}
		var mc proto.MessageContainer
		if err := mc.Decode(b); err != nil {
			return s.badMsg(ctx, sess, msgID, seqNo, badContainer)
		}
		for _, m := range mc.Messages {
			if err := s.process(ctx, sess, m.ID, int32(m.SeqNo), m.Body, true); err != nil {
				return err
			}
		}
		return nil
	case proto.GZIPTypeID:
		var gz proto.GZIP
		if err := gz.Decode(b); err != nil {
			return err
		}
		// same msg_id, so skip the checks and go straight to the body
		return s.processBody(ctx, sess, msgID, gz.Data)
	}
	return s.processBody(ctx, sess, msgID, body)
}

func (s *Server) processBody(ctx context.Context, sess *session, msgID int64, body []byte) error {
	b := &bin.Buffer{Buf: body}
	typeID, err := b.PeekID()
	if err != nil {
		return err
	}
	now := s.clock.Now()
	switch typeID {
	case mt.MsgsAckTypeID:
		var ack mt.MsgsAck
		if err := ack.Decode(b); err != nil {
			return err
		}
		sess.mu.Lock()
		for _, id := range ack.MsgIDs {
			delete(sess.unacked, id)
		}
		sess.mu.Unlock()
		return nil

	case mt.PingRequestTypeID:
		var p mt.PingRequest
		if err := p.Decode(b); err != nil {
			return err
		}
		return s.sendService(ctx, sess, &mt.Pong{MsgID: msgID, PingID: p.PingID}, true)

	case mt.PingDelayDisconnectRequestTypeID:
		var p mt.PingDelayDisconnectRequest
		if err := p.Decode(b); err != nil {
			return err
		}
		sess.mu.Lock()
		c := sess.conn
		sess.mu.Unlock()
		if c != nil && p.DisconnectDelay > 0 {
			c.delayDisconnect(time.Duration(p.DisconnectDelay) * time.Second)
		}
		return s.sendService(ctx, sess, &mt.Pong{MsgID: msgID, PingID: p.PingID}, true)

	case mt.GetFutureSaltsRequestTypeID:
		var r mt.GetFutureSaltsRequest
		if err := r.Decode(b); err != nil {
			return err
		}
		return s.sendService(ctx, sess, &mt.FutureSalts{
			ReqMsgID: msgID,
			Now:      int(now.Unix()),
			Salts:    s.salts.future(sess.keyID, now, r.Num),
		}, true)

	case mt.DestroySessionRequestTypeID:
		var r mt.DestroySessionRequest
		if err := r.Decode(b); err != nil {
			return err
		}
		// a client may only kill its other sessions on the same key
		if r.SessionID != sess.id && s.sessions.destroy(sess.keyID, r.SessionID) {
			return s.sendService(ctx, sess, &mt.DestroySessionOk{SessionID: r.SessionID}, true)
		}
		return s.sendService(ctx, sess, &mt.DestroySessionNone{SessionID: r.SessionID}, true)

	case mt.MsgsStateReqTypeID:
		var r mt.MsgsStateReq
		if err := r.Decode(b); err != nil {
			return err
		}
		info := make([]byte, len(r.MsgIDs))
		sess.mu.Lock()
		for i, id := range r.MsgIDs {
			info[i] = s.msgState(sess, id)
		}
		sess.mu.Unlock()
		return s.sendService(ctx, sess, &mt.MsgsStateInfo{ReqMsgID: msgID, Info: info}, false)

	case mt.MsgResendReqTypeID:
		var r mt.MsgResendReq
		if err := r.Decode(b); err != nil {
			return err
		}
		sess.mu.Lock()
		var resend [][]byte
		for _, id := range r.MsgIDs {
			if body, ok := sess.unacked[id]; ok {
				delete(sess.unacked, id)
				resend = append(resend, body)
			}
		}
		sess.mu.Unlock()
		for _, body := range resend {
			if err := s.sendRaw(ctx, sess, body, true, true); err != nil {
				return err
			}
		}
		return nil

	case mt.RPCDropAnswerRequestTypeID:
		var r mt.RPCDropAnswerRequest
		if err := r.Decode(b); err != nil {
			return err
		}
		// we don't cancel running handlers; the client just ignores the result
		return s.sendResult(ctx, sess, msgID, &mt.RPCAnswerUnknown{})

	case mt.HTTPWaitRequestTypeID, mt.BadMsgNotificationTypeID, mt.BadServerSaltTypeID:
		return nil

	case destroyAuthKeyID:
		return s.destroyAuthKey(ctx, sess)
	}

	sess.mu.Lock()
	sess.acks = append(sess.acks, msgID)
	sess.mu.Unlock()
	s.call(ctx, sess, msgID, body)
	return nil
}

// msgs_state_info byte: 1 nothing known, 3 never got it (too low), 4 got it,
// +8 answered and the answer was acked or is being re-sent.
func (s *Server) msgState(sess *session, id int64) byte {
	if !sess.seen.has(id) {
		return 1
	}
	if c, ok := sess.calls[id]; ok && c.result != nil {
		return 4 | 8
	}
	return 4
}

// checkMsgID applies the msg_id/seqno rules from the MTProto spec.
func (s *Server) checkMsgID(msgID int64, seqNo int32, typeID uint32) int {
	if msgID%4 != 0 {
		return badMsgIDBits
	}
	now := s.clock.Now()
	t := proto.MessageID(msgID).Time()
	if t.Before(now.Add(-300 * time.Second)) {
		return badMsgIDTooLow
	}
	if t.After(now.Add(30 * time.Second)) {
		return badMsgIDTooHigh
	}
	content := seqNo%2 == 1
	switch typeID {
	case proto.MessageContainerTypeID, mt.MsgsAckTypeID:
		if content {
			return badSeqNotEven
		}
	case mt.PingRequestTypeID, mt.PingDelayDisconnectRequestTypeID, mt.MsgsStateReqTypeID,
		mt.MsgResendReqTypeID, mt.HTTPWaitRequestTypeID, mt.GetFutureSaltsRequestTypeID:
		// clients disagree about these; accept either parity
	default:
		if !content {
			return badSeqNotOdd
		}
	}
	return 0
}

func (s *Server) badMsg(ctx context.Context, sess *session, msgID int64, seqNo int32, code int) error {
	return s.sendService(ctx, sess, &mt.BadMsgNotification{BadMsgID: msgID, BadMsgSeqno: int(seqNo), ErrorCode: code}, false)
}

// duplicate answers a re-sent request from the ledger instead of running it
// twice; a container with a reused id is a client bug.
func (s *Server) duplicate(ctx context.Context, sess *session, msgID int64) error {
	sess.mu.Lock()
	c, ok := sess.calls[msgID]
	var result []byte
	if ok {
		result = c.result
	}
	sess.mu.Unlock()
	if result != nil {
		return s.sendRaw(ctx, sess, result, true, true)
	}
	// still running, or a service message we already handled: ack it again
	sess.mu.Lock()
	sess.acks = append(sess.acks, msgID)
	sess.mu.Unlock()
	return nil
}

func (s *Server) flushAcks(ctx context.Context, sess *session) error {
	sess.mu.Lock()
	ids := sess.acks
	sess.acks = nil
	sess.mu.Unlock()
	if len(ids) == 0 {
		return nil
	}
	return s.sendService(ctx, sess, &mt.MsgsAck{MsgIDs: ids}, false)
}

func (s *Server) sendService(ctx context.Context, sess *session, msg bin.Encoder, content bool) error {
	var b bin.Buffer
	if err := msg.Encode(&b); err != nil {
		return err
	}
	return s.sendRaw(ctx, sess, b.Buf, content, false)
}

func (s *Server) sendResult(ctx context.Context, sess *session, reqMsgID int64, result bin.Encoder) error {
	var b bin.Buffer
	b.PutID(mt.RPCResultTypeID)
	b.PutLong(reqMsgID)
	if err := result.Encode(&b); err != nil {
		return err
	}
	return s.sendRaw(ctx, sess, b.Buf, true, true)
}

// sendRaw encrypts body for sess and writes it to the session's connection.
// keep holds it until msgs_ack so a reconnect can re-send it. With no
// connection attached the message just waits in unacked.
func (s *Server) sendRaw(ctx context.Context, sess *session, body []byte, content, keep bool) error {
	return s.sendTyped(ctx, sess, body, content, keep, proto.MessageServerResponse)
}

// sendTyped is sendRaw with the msg_id type: responses vs server-initiated
// messages (updates) differ in the low bits.
func (s *Server) sendTyped(ctx context.Context, sess *session, body []byte, content, keep bool, typ proto.MessageType) error {
	sess.mu.Lock()
	q := s.queueLocked(sess, body, content, keep, typ)
	sess.mu.Unlock()
	return s.write(ctx, sess, q)
}

type queued struct {
	c    *conn
	key  crypto.AuthKey
	data crypto.EncryptedMessageData
}

// queueLocked gives body its msg_id and seqno and keeps it for re-sending;
// sess.mu held.
func (s *Server) queueLocked(sess *session, body []byte, content, keep bool, typ proto.MessageType) queued {
	id := s.msgIDs.New(typ)
	seq := sess.nextSeq(content)
	if keep && !sess.dead {
		if len(sess.unacked) >= unackedKept {
			// a client that never acks; forget the oldest, and have its next
			// message announce a session gap so it resyncs what went
			oldest := int64(0)
			for k := range sess.unacked {
				if oldest == 0 || k < oldest {
					oldest = k
				}
			}
			delete(sess.unacked, oldest)
			sess.announced = false
		}
		sess.unacked[id] = body
	}
	return queued{c: sess.conn, key: sess.key.Key, data: crypto.EncryptedMessageData{
		SessionID:              sess.id,
		MessageID:              id,
		SeqNo:                  seq,
		MessageDataLen:         int32(len(body)),
		MessageDataWithPadding: body,
	}}
}

// write sends a queued message if the session has a connection; without
// one it just waits in unacked.
func (s *Server) write(ctx context.Context, sess *session, q queued) error {
	if q.c == nil {
		return nil
	}
	q.data.Salt = s.salts.current(sess.keyID, s.clock.Now())
	var out bin.Buffer
	if err := s.cipher.Encrypt(q.key, q.data, &out); err != nil {
		return err
	}
	return q.c.write(ctx, &out)
}

func gunzip(wire []byte, limit int) ([]byte, func(), error) {
	var gz proto.GZIP
	if err := gz.Decode(&bin.Buffer{Buf: wire}); err != nil {
		return nil, nil, err
	}
	data := gz.Data
	if len(data) > limit {
		return nil, nil, errors.New("gzip_packed too large")
	}
	return data, nil, nil
}
