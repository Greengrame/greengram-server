package api

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/messages"
)

// hub knows which client sessions belong to which user, so an update for a
// user reaches every app and tab they have open. A session joins on its
// first signed-in call (unless it asked for no updates).
type hub struct {
	srv *edge.Server

	mu    sync.Mutex
	m     map[int64]map[edge.Session]member // user -> session
	owner map[edge.Session]owner
}

type member struct {
	hash  string // sign-in session
	epoch uint64 // edge push epoch read before the sign-in was looked up
}

type owner struct {
	user  int64
	epoch uint64
}

func newHub(srv *edge.Server) *hub {
	return &hub{srv: srv, m: map[int64]map[edge.Session]member{}, owner: map[edge.Session]owner{}}
}

// join puts s in user's stream. A session belongs to one user: the same
// auth key may have been signed in as someone else a moment ago, and that
// sign-in can still be live elsewhere, so the hash check in push alone
// would keep feeding it the old account's messages. A call that looked the
// user up before a sign-out or sign-in on the key carries an older epoch
// and is ignored.
func (h *hub) join(user int64, s edge.Session, sessionHash string, epoch uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if prev, ok := h.owner[s]; ok {
		if prev.epoch > epoch {
			return
		}
		if prev.user != user {
			h.dropLocked(prev.user, s)
		}
	}
	h.owner[s] = owner{user, epoch}
	set := h.m[user]
	if set == nil {
		set = map[edge.Session]member{}
		h.m[user] = set
	}
	set[s] = member{sessionHash, epoch}
}

// leaveKey takes every session of an account key out of the streams, when
// the key signs out or in as someone else; the next signed-in call joins
// again. Rare enough to scan for.
func (h *hub) leaveKey(perm int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s, o := range h.owner {
		if s.PermKeyID() == perm {
			h.dropLocked(o.user, s)
		}
	}
}

func (h *hub) dropLocked(user int64, s edge.Session) {
	delete(h.m[user], s)
	if len(h.m[user]) == 0 {
		delete(h.m, user)
	}
	if h.owner[s].user == user {
		delete(h.owner, s)
	}
}

// online is whether user has any session to push to, so fan-out can skip
// building updates for people who aren't there.
func (h *hub) online(user int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.m[user]) > 0
}

// push sends obj to the user's sessions except `except`. Sessions whose
// sign-in ended (logout, revoked elsewhere) or that the edge forgot are
// dropped on the way. The hash check alone races a logout on that key;
// the epoch closes it (see edge.StopPushes).
func (a *API) push(ctx context.Context, user int64, obj *tg.Updates, except edge.Session) {
	h := a.hub
	h.mu.Lock()
	targets := make(map[edge.Session]member, len(h.m[user]))
	for s, m := range h.m[user] {
		targets[s] = m
	}
	h.mu.Unlock()
	if len(targets) == 0 {
		return
	}
	live, err := a.accounts.SessionHashes(ctx, user)
	if err != nil {
		slog.Warn("api: push sessions", "err", err)
		return
	}
	var drop []edge.Session
	for s, m := range targets {
		if !live[m.hash] {
			drop = append(drop, s)
			continue
		}
		if s == except {
			continue
		}
		if err := h.srv.Push(ctx, s, m.epoch, obj); errors.Is(err, edge.ErrSessionGone) {
			drop = append(drop, s)
		} else if err != nil {
			slog.Debug("api: push", "user", user, "err", err)
		} else {
			slog.Debug("api: pushed", "user", user, "key", s.PermKeyID())
		}
	}
	if len(drop) > 0 {
		h.mu.Lock()
		for _, s := range drop {
			// unless it joined again meanwhile
			if h.m[user][s] == targets[s] {
				h.dropLocked(user, s)
			}
		}
		h.mu.Unlock()
	}
}

// outbox runs one user's pushes one after another, in the order they were
// queued. A goroutine per push let a later message overtake an earlier
// one, and the client then saw a pts gap and went for getDifference.
type outbox struct {
	mu   sync.Mutex
	q    map[int64][]func(context.Context)
	idle map[int64]chan struct{} // closed when the user's queue empties
}

// wait returns once everything queued for user so far has gone out, or
// after max.
func (o *outbox) wait(ctx context.Context, user int64, max time.Duration) {
	o.mu.Lock()
	if _, busy := o.q[user]; !busy {
		o.mu.Unlock()
		return
	}
	if o.idle == nil {
		o.idle = map[int64]chan struct{}{}
	}
	ch := o.idle[user]
	if ch == nil {
		ch = make(chan struct{})
		o.idle[user] = ch
	}
	o.mu.Unlock()
	t := time.NewTimer(max)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	case <-ctx.Done():
	}
}

// add queues job for user; a drainer runs while the user has any queued.
func (o *outbox) add(user int64, job func(context.Context)) {
	o.mu.Lock()
	if o.q == nil {
		o.q = map[int64][]func(context.Context){}
	}
	_, busy := o.q[user]
	o.q[user] = append(o.q[user], job)
	o.mu.Unlock()
	if !busy {
		go o.drain(user)
	}
}

func (o *outbox) drain(user int64) {
	for {
		o.mu.Lock()
		list := o.q[user]
		if len(list) == 0 {
			delete(o.q, user)
			if ch := o.idle[user]; ch != nil {
				close(ch)
				delete(o.idle, user)
			}
			o.mu.Unlock()
			return
		}
		job := list[0]
		o.q[user] = list[1:]
		o.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		job(ctx)
		cancel()
	}
}

// deliver pushes journaled events to their users, each with the user and
// chat objects its viewer needs. Detached: the caller's reply shouldn't
// wait on other people's sockets.
func (a *API) deliver(events []messages.Event, except edge.Session) {
	for _, e := range events {
		if !a.hub.online(e.UserID) {
			continue
		}
		// the recipient's update shares media objects with the sender's
		// reply (one photo, one poll), and td's Encode writes flags as it
		// goes: the job gets its own copy, taken before the reply goes out
		var raw bin.Buffer
		if err := e.Update.Encode(&raw); err != nil {
			slog.Warn("api: deliver encode", "err", err)
			continue
		}
		e.Update = nil
		a.out.add(e.UserID, func(ctx context.Context) {
			var err error
			if e.Update, err = tg.DecodeUpdate(&raw); err != nil {
				slog.Warn("api: deliver decode", "err", err)
				return
			}
			users, err := a.usersFor(ctx, e.UserID, updateUsers(e.Update))
			if err != nil {
				slog.Warn("api: deliver users", "err", err)
				return
			}
			var chans []int64
			if m := updateMessage(e.Update); m != nil {
				_, chans = mentioned(m)
			}
			if id := updateChannelID(e.Update); id != 0 {
				chans = append(chans, id)
			}
			chats, err := a.chatsFor(ctx, e.UserID, chans)
			if err != nil {
				slog.Warn("api: deliver chats", "err", err)
				return
			}
			a.push(ctx, e.UserID, &tg.Updates{Updates: []tg.UpdateClass{e.Update}, Users: users, Chats: chats, Date: int(time.Now().Unix())}, except)
		})
	}
}

// updateUsers lists who an update mentions.
func updateUsers(u tg.UpdateClass) []int64 {
	var msg tg.MessageClass
	switch v := u.(type) {
	case *tg.UpdateNewMessage:
		msg = v.Message
	case *tg.UpdateEditMessage:
		msg = v.Message
	case *tg.UpdateUserTyping:
		return []int64{serverID(v.UserID)}
	case *tg.UpdateMessageReactions:
		return reactionUsers(v)
	case *tg.UpdatePinnedMessages:
		if p, ok := v.Peer.(*tg.PeerUser); ok {
			return []int64{serverID(p.UserID)}
		}
		return nil
	case *tg.UpdateDraftMessage:
		if p, ok := v.Peer.(*tg.PeerUser); ok {
			return []int64{serverID(p.UserID)}
		}
		return nil
	case *tg.UpdateNotifySettings:
		if n, ok := v.Peer.(*tg.NotifyPeer); ok {
			if p, ok := n.Peer.(*tg.PeerUser); ok {
				return []int64{serverID(p.UserID)}
			}
		}
		return nil
	default:
		return nil
	}
	var out []int64
	add := func(p tg.PeerClass) {
		if pu, ok := p.(*tg.PeerUser); ok {
			out = append(out, serverID(pu.UserID))
		}
	}
	if s, ok := msg.(*tg.MessageService); ok {
		add(s.PeerID)
		if s.FromID != nil {
			add(s.FromID)
		}
		return out
	}
	m, ok := msg.(*tg.Message)
	if !ok {
		return nil
	}
	add(m.PeerID)
	if m.FromID != nil {
		add(m.FromID)
	}
	if fwd, ok := m.GetFwdFrom(); ok && fwd.FromID != nil {
		add(fwd.FromID)
	}
	return out
}
