package edge

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tlprofile"
)

// Session is a handle on one client session (auth key + session_id). It
// stays comparable so callers can keep sets of them.
type Session struct{ s *session }

func (s Session) Valid() bool { return s.s != nil }

// PermKeyID is the account key the session's auth key stands for.
func (s Session) PermKeyID() int64 {
	if s.s == nil {
		return 0
	}
	s.s.mu.Lock()
	defer s.s.mu.Unlock()
	if s.s.key.PermID != 0 {
		return s.s.key.PermID
	}
	return s.s.keyID
}

// Epoch is the session's push epoch. Read it before looking up who the
// session is signed in as, and hand it to Push: a StopPushes in between
// makes that Push fail instead of reaching the wrong account.
func (s Session) Epoch() uint64 {
	if s.s == nil {
		return 0
	}
	s.s.mu.Lock()
	defer s.s.mu.Unlock()
	return s.s.epoch
}

// ErrSessionGone means the session was destroyed, swept or parked, or its
// key stopped pushes since epoch was read; drop the handle. A session that
// comes back joins the push stream again on its next signed-in call.
var ErrSessionGone = errors.New("edge: session gone")

// Push sends a server-initiated object (an Updates) to the session, encoded
// for the layer that session speaks. With no connection attached it waits
// in unacked and goes out on reconnect, like any other content message.
func (s *Server) Push(ctx context.Context, to Session, epoch uint64, obj bin.Object) error {
	if to.s == nil {
		return ErrSessionGone
	}
	to.s.mu.Lock()
	gone, profile := to.s.dead || to.s.parked || to.s.epoch != epoch, to.s.profile
	to.s.mu.Unlock()
	if gone {
		return ErrSessionGone
	}
	if profile == 0 {
		profile = tlprofile.ProfileCanonical
	}
	var b bin.Buffer
	if err := tlprofile.EncodeObject(profile, obj, &b); err != nil {
		return err
	}
	// checked again where it's queued: that's the point StopPushes orders
	// against
	to.s.mu.Lock()
	if to.s.dead || to.s.parked || to.s.epoch != epoch {
		to.s.mu.Unlock()
		return ErrSessionGone
	}
	q := s.queueLocked(to.s, b.Buf, true, true, proto.MessageFromServer)
	to.s.mu.Unlock()
	return s.write(ctx, to.s, q)
}

// StopPushes ends the update stream of every session on an account key and
// the temp keys bound to it, when it signs out or in as someone else.
// Pushes already queued but not acked are dropped too, so a reconnect
// doesn't deliver them. Once it returns no Push with an older epoch gets
// through.
func (s *Server) StopPushes(perm int64) {
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()
	for keyID, byID := range s.sessions.m {
		for _, sess := range byID {
			sess.mu.Lock()
			if keyID == perm || sess.key.PermID == perm {
				sess.epoch++
				for id := range sess.unacked {
					if proto.MessageID(id).Type() == proto.MessageFromServer {
						delete(sess.unacked, id)
					}
				}
			}
			sess.mu.Unlock()
		}
	}
}
