package edge

import (
	"fmt"
	"math"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/iamxvbaba/td/mt"
)

// Flood limits are token buckets per auth key and per client address. A
// call over the limit gets FLOOD_WAIT_X like on Telegram; a handshake over
// it gets the transport error -429. The numbers only stop floods: an app
// starting up or pulling media in parallel stays well under them.

type rate struct {
	burst  float64
	perSec float64
}

type floodRates struct {
	rpcKey, rpcIP, handshakeIP rate
}

var defaultFloodRates = floodRates{
	rpcKey:      rate{burst: 500, perSec: 100},
	rpcIP:       rate{burst: 2000, perSec: 300},
	handshakeIP: rate{burst: 60, perSec: 1},
}

type floodKind uint8

const (
	floodRPCKey floodKind = iota
	floodRPCIP
	floodHandshakeIP
)

type floodKey struct {
	kind floodKind
	key  int64
	ip   netip.Addr
}

type tokens struct {
	n    float64
	at   time.Time
	full time.Time // when it is back to burst; then it can go
}

type flood struct {
	mu sync.Mutex
	m  map[floodKey]*tokens
}

// take spends a token: 0 if there was one, else how long until there is.
func (f *flood) take(k floodKey, r rate, now time.Time) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[floodKey]*tokens{}
	}
	t := f.m[k]
	if t == nil {
		t = &tokens{n: r.burst, at: now}
		f.m[k] = t
	}
	t.n = min(r.burst, t.n+now.Sub(t.at).Seconds()*r.perSec)
	t.at = now
	if t.n < 1 {
		return time.Duration((1 - t.n) / r.perSec * float64(time.Second))
	}
	t.n--
	t.full = now.Add(time.Duration((r.burst - t.n) / r.perSec * float64(time.Second)))
	return 0
}

// sweep forgets buckets that refilled; a new one starts full anyway.
func (f *flood) sweep(now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, t := range f.m {
		if !now.Before(t.full) {
			delete(f.m, k)
		}
	}
}

// floodIP is the address a client counts under. IPv6 goes by /64: that is
// what one host gets, so rotating inside it is free. A private or loopback
// peer is a proxy in front of us (Traefik for /apiws) hiding the client, so
// it isn't counted.
func floodIP(a net.Addr) (netip.Addr, bool) {
	var ip netip.Addr
	switch v := a.(type) {
	case nil:
		return ip, false
	case *net.TCPAddr:
		ip = v.AddrPort().Addr()
	default:
		ap, err := netip.ParseAddrPort(a.String())
		if err != nil {
			return ip, false
		}
		ip = ap.Addr()
	}
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() {
		return netip.Addr{}, false
	}
	if ip.Is6() {
		p, _ := ip.Prefix(64)
		ip = p.Addr()
	}
	return ip, true
}

// rpcFlood is how long the client has to wait before its next call.
func (s *Server) rpcFlood(keyID int64, c *conn, now time.Time) time.Duration {
	wait := s.flood.take(floodKey{kind: floodRPCKey, key: keyID}, s.floodRates.rpcKey, now)
	if c != nil && c.floodOK && wait == 0 {
		wait = s.flood.take(floodKey{kind: floodRPCIP, ip: c.floodIP}, s.floodRates.rpcIP, now)
	}
	return wait
}

func (s *Server) handshakeFlood(c *conn, now time.Time) bool {
	return c.floodOK && s.flood.take(floodKey{kind: floodHandshakeIP, ip: c.floodIP}, s.floodRates.handshakeIP, now) > 0
}

func floodWait(msgID int64, wait time.Duration) []byte {
	secs := max(1, int(math.Ceil(wait.Seconds())))
	return rpcError(msgID, &mt.RPCError{ErrorCode: 420, ErrorMessage: fmt.Sprintf("FLOOD_WAIT_%d", secs)})
}
