package edge

import (
	"sort"
	"sync"
	"time"

	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/store"
)

// A session is (auth key, session_id). It outlives the TCP connection: when
// the client reconnects with the same session_id it gets the same seqno,
// duplicate filter, call ledger and every unacked message re-sent.

const (
	seenWindow  = 2048             // client msg_ids remembered for duplicate checks
	callsKept   = 4096             // finished calls kept for replay, per session
	callRetain  = 10 * time.Minute // and for how long
	unackedKept = 1024             // server messages waiting for msgs_ack
	// Apps open a handful of sessions per key (Android ~6, a Web A tab
	// each); past this the least recently used one goes.
	sessionsPerKey = 64
)

type session struct {
	key   store.AuthKey
	keyID int64
	id    int64

	mu        sync.Mutex
	conn      *conn
	profile   tlprofile.Profile
	client    ClientInfo
	seq       int32  // content-related messages we sent
	announced bool   // new_session_created sent
	dead      bool   // removed from the server; pushes are refused
	parked    bool   // idle past the sweep, kept for its unacked; pushes are refused
	epoch     uint64 // bumped by StopPushes; a push for an older one is refused
	seen      seenIDs
	calls     map[int64]*call // by req_msg_id
	unacked   map[int64][]byte
	acks      []int64 // client content messages to ack
	active    time.Time
}

type call struct {
	at     time.Time
	result []byte        // full rpc_result, nil while running
	done   chan struct{} // closed once result is set
}

// ClientInfo comes from initConnection.
type ClientInfo struct {
	APIID         int
	DeviceModel   string
	SystemVersion string
	AppVersion    string
	LangCode      string
}

func (s *session) nextSeq(content bool) int32 {
	if !content {
		return s.seq * 2
	}
	s.seq++
	return s.seq*2 - 1
}

// pending returns unacked messages oldest first.
func (s *session) pending() [][]byte {
	ids := make([]int64, 0, len(s.unacked))
	for id := range s.unacked {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([][]byte, len(ids))
	for i, id := range ids {
		out[i] = s.unacked[id]
		delete(s.unacked, id)
	}
	return out
}

func (s *session) dropOldCalls(now time.Time) {
	if len(s.calls) < callsKept {
		return
	}
	s.trimCalls(now)
}

// trimCalls forgets answers the client can no longer ask for again: a
// re-sent msg_id older than 300s is refused before the ledger is checked.
func (s *session) trimCalls(now time.Time) {
	for id, c := range s.calls {
		if c.result != nil && now.Sub(c.at) > callRetain {
			delete(s.calls, id)
		}
	}
}

type seenIDs struct {
	set  map[int64]struct{}
	ring []int64
	next int
}

// add reports false for a msg_id we already had.
func (s *seenIDs) add(id int64) bool {
	if s.set == nil {
		// grown as used: most sessions never see seenWindow messages
		s.set = map[int64]struct{}{}
	}
	if _, dup := s.set[id]; dup {
		return false
	}
	if len(s.ring) < seenWindow {
		s.ring = append(s.ring, id)
	} else {
		delete(s.set, s.ring[s.next])
		s.ring[s.next] = id
		s.next = (s.next + 1) % seenWindow
	}
	s.set[id] = struct{}{}
	return true
}

func (s *seenIDs) has(id int64) bool {
	_, ok := s.set[id]
	return ok
}

type sessions struct {
	mu sync.Mutex
	m  map[int64]map[int64]*session // auth key -> session_id -> session
}

func (ss *sessions) get(k store.AuthKey, keyID, id int64, now time.Time) (*session, bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	byID := ss.m[keyID]
	if s, ok := byID[id]; ok {
		return s, false
	}
	if byID == nil {
		byID = map[int64]*session{}
		ss.m[keyID] = byID
	}
	if len(byID) >= sessionsPerKey {
		evictLRU(byID)
	}
	s := &session{
		key:     k,
		keyID:   keyID,
		id:      id,
		profile: tlprofile.Profile(k.Layer),
		calls:   map[int64]*call{},
		unacked: map[int64][]byte{},
		active:  now,
	}
	if _, ok := tlprofile.ResolveProfile(k.Layer); !ok {
		s.profile = 0
	}
	byID[id] = s
	return s, true
}

func evictLRU(byID map[int64]*session) {
	var (
		oldID int64
		old   *session
		at    time.Time
	)
	for id, s := range byID {
		s.mu.Lock()
		active := s.active
		s.mu.Unlock()
		if old == nil || active.Before(at) {
			oldID, old, at = id, s, active
		}
	}
	old.kill()
	delete(byID, oldID)
}

func (ss *sessions) destroy(keyID, id int64) bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	s, ok := ss.m[keyID][id]
	if ok {
		s.kill()
		ss.remove(keyID, id)
	}
	return ok
}

// remove is delete for the two-level map; ss.mu held.
func (ss *sessions) remove(keyID, id int64) {
	delete(ss.m[keyID], id)
	if len(ss.m[keyID]) == 0 {
		delete(ss.m, keyID)
	}
}

func (ss *sessions) dropKey(keyID int64) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	for _, s := range ss.m[keyID] {
		s.kill()
	}
	delete(ss.m, keyID)
}

// keys lists the auth keys that have sessions.
func (ss *sessions) keys() map[int64]bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	out := make(map[int64]bool, len(ss.m))
	for id := range ss.m {
		out[id] = true
	}
	return out
}

// boundTo lists the temp keys bound to perm that have sessions.
func (ss *sessions) boundTo(perm int64) []int64 {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	var out []int64
	for keyID, byID := range ss.m {
		for _, s := range byID {
			s.mu.Lock()
			bound := s.key.PermID == perm
			s.mu.Unlock()
			if bound {
				out = append(out, keyID)
				break
			}
		}
	}
	return out
}

// forKey runs f on each session of the key, with that session locked.
func (ss *sessions) forKey(keyID int64, f func(*session)) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	for _, s := range ss.m[keyID] {
		s.mu.Lock()
		f(s)
		s.mu.Unlock()
	}
}

// sweep drops sessions nobody came back to. One still holding unacked
// messages stays, as on Telegram, until the client returns or sessionsPerKey
// evicts it. It only stops taking pushes: Web A leaves a new session behind on
// every reconnect, and those would otherwise fill up forever. A temp key that
// ran out can't come back, so neither can its sessions.
func (ss *sessions) sweep(before, now time.Time) int {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	n := 0
	for keyID, byID := range ss.m {
		for id, s := range byID {
			s.mu.Lock()
			idle := s.conn == nil && s.active.Before(before)
			expired := !s.key.ExpiresAt.IsZero() && !s.key.ExpiresAt.After(now)
			drop := idle && (len(s.unacked) == 0 || expired)
			if !drop {
				s.trimCalls(now)
			}
			if idle && !drop {
				s.parked = true
				// what it missed while parked comes from getDifference
				s.announced = false
			}
			s.mu.Unlock()
			if drop {
				s.kill()
				ss.remove(keyID, id)
				n++
			}
		}
	}
	return n
}

// kill also lets go of the session's state: the push hub keeps handles
// until its next push to that user finds them gone.
func (s *session) kill() {
	s.mu.Lock()
	s.dead = true
	s.seen = seenIDs{}
	clear(s.calls)
	clear(s.unacked)
	s.mu.Unlock()
}
