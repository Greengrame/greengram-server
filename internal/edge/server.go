// Package edge is the MTProto side of the server: transports, the key
// exchange, sessions, msg_id rules, salts, acks and the RPC hand-off.
//
// Protocol rules follow https://core.telegram.org/mtproto and were checked
// against gramsrv's mtprotoedge; the code is our own and much smaller.
package edge

import (
	"context"
	"crypto/rsa"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/store"
)

type Config struct {
	RSA    *rsa.PrivateKey
	Secret []byte // salt derivation, see LoadSecret
	Keys   KeyStore
	// DC the clients put in p_q_inner_data. Our Android build runs on the
	// test backend, so it sends 10000+dc.
	ExchangeDC int
	Clock      clock.Clock // nil = system
}

// keyRecord is what we cache per auth key.
type keyRecord = store.AuthKey

type Server struct {
	// OnKeyDestroyed ends what an account key signed in to, before
	// destroy_auth_key removes the key. An error keeps the key.
	OnKeyDestroyed func(ctx context.Context, keyID int64) error

	wsClients sync.Map // peer address of an open /apiws request -> client IP

	key      exchange.PrivateKey
	cipher   crypto.Cipher
	keys     KeyStore
	salts    salts
	dc       int
	clock    clock.Clock
	msgIDs   *proto.MessageIDGen
	rpc      *tlprofile.Dispatcher
	sessions sessions

	flood      flood
	floodRates floodRates

	keyMu    sync.Mutex
	keyCache map[int64]store.AuthKey
	pruned   time.Time // last PruneAuthKeys, Sweep's goroutine only
}

func New(cfg Config) *Server {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.System
	}
	s := &Server{
		key:      exchange.PrivateKey{RSA: cfg.RSA},
		cipher:   crypto.NewServerCipher(crypto.DefaultRand()),
		keys:     cfg.Keys,
		salts:    salts{secret: cfg.Secret},
		dc:       cfg.ExchangeDC,
		clock:    clk,
		msgIDs:   proto.NewMessageIDGen(clk.Now),
		rpc:      tlprofile.NewDispatcher(),
		sessions: sessions{m: map[int64]map[int64]*session{}},
		keyCache: map[int64]store.AuthKey{},

		floodRates: defaultFloodRates,
	}
	s.rpc.OnWrappers(s.consumeWrappers)
	s.rpc.OnUnknownMethod(s.adaptAndroid)
	s.registerInternal()
	return s
}

// PublicKey is what clients pin.
func (s *Server) PublicKey() exchange.PublicKey { return s.key.Public() }

// Handle registers an RPC method. h gets the canonical (layer 232) request
// and returns the canonical result; the dispatcher re-encodes it for the
// client's layer.
func (s *Server) Handle(method tlprofile.SemanticID, h tlprofile.Handler) {
	if err := s.rpc.Register(method, h); err != nil {
		panic(err)
	}
}

func (s *Server) authKey(ctx context.Context, id int64) (store.AuthKey, bool, error) {
	s.keyMu.Lock()
	k, ok := s.keyCache[id]
	s.keyMu.Unlock()
	if ok {
		return k, true, nil
	}
	k, ok, err := s.keys.AuthKey(ctx, id)
	if err != nil || !ok {
		return k, ok, err
	}
	s.keyMu.Lock()
	s.keyCache[id] = k
	s.keyMu.Unlock()
	return k, true, nil
}

func (s *Server) updateKey(id int64, f func(*store.AuthKey)) {
	s.keyMu.Lock()
	if k, ok := s.keyCache[id]; ok {
		f(&k)
		s.keyCache[id] = k
	}
	s.keyMu.Unlock()
}

func (s *Server) forgetKey(id int64) {
	s.keyMu.Lock()
	delete(s.keyCache, id)
	s.keyMu.Unlock()
}

// expireKey drops a temp key whose time is up. The client gets -404 and
// makes a new one.
func (s *Server) expireKey(ctx context.Context, id int64) {
	s.forgetKey(id)
	s.sessions.dropKey(id)
	if err := s.keys.DeleteAuthKey(ctx, id); err != nil {
		slog.Warn("edge: delete expired key", "err", err)
	}
}

const (
	pruneEvery = 10 * time.Minute
	// a handshake nobody followed up on; real apps call within seconds
	unusedKeyTTL = 24 * time.Hour
)

// Sweep drops idle sessions, old answers and cached keys nobody is using;
// run it from a ticker. Apps make fresh temp keys all the time, so a cache
// of every key seen would only grow. Every pruneEvery it also deletes
// expired and never used keys from the store.
func (s *Server) Sweep(idle time.Duration) {
	now := s.clock.Now()
	n := s.sessions.sweep(now.Add(-idle), now)
	if n > 0 {
		slog.Debug("edge: dropped idle sessions", "count", n)
	}
	s.flood.sweep(now)
	inUse := s.sessions.keys()
	s.keyMu.Lock()
	for id := range s.keyCache {
		if !inUse[id] {
			delete(s.keyCache, id)
		}
	}
	s.keyMu.Unlock()

	if now.Sub(s.pruned) < pruneEvery {
		return
	}
	s.pruned = now
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	gone, err := s.keys.PruneAuthKeys(ctx, now, now.Add(-unusedKeyTTL), slices.Collect(maps.Keys(inUse)))
	if err != nil {
		slog.Warn("edge: prune auth keys", "err", err)
	} else if gone > 0 {
		slog.Debug("edge: pruned auth keys", "count", gone)
	}
}
