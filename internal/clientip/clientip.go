// Package clientip finds the real client address of an HTTP request.
package clientip

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// A private or loopback peer is our own proxy: Traefik reaches the container
// over the docker network, and nothing public can have such an address.
func trusted(a netip.Addr) bool {
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast()
}

func parse(v string) (netip.Addr, bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(v))
	if err != nil {
		return a, false
	}
	return a.Unmap(), true
}

// From is the client behind r. Forwarded headers count only when the peer is
// trusted, and X-Forwarded-For is read from the right: a client can put
// anything on the left of it, only the entries our proxies added are true.
func From(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, ok := parse(host)
	if !ok || !trusted(peer) {
		return host
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, ok := parse(hops[i])
		if !ok {
			break
		}
		if !trusted(hop) {
			return hop.String()
		}
	}
	if real, ok := parse(r.Header.Get("X-Real-IP")); ok && !trusted(real) {
		return real.String()
	}
	return host
}
