package clientip

import (
	"net/http"
	"testing"
)

func TestFrom(t *testing.T) {
	for _, c := range []struct {
		name, peer, xff, real, want string
	}{
		{"direct client", "203.0.113.9:1234", "", "", "203.0.113.9"},
		{"direct client can't forge", "203.0.113.9:1234", "1.1.1.1", "2.2.2.2", "203.0.113.9"},
		{"behind traefik", "172.18.0.5:1234", "198.51.100.7", "198.51.100.7", "198.51.100.7"},
		{"spoofed left of the proxy's entry", "172.18.0.5:1234", "1.1.1.1, 198.51.100.7", "", "198.51.100.7"},
		{"two proxies", "172.18.0.5:1234", "198.51.100.7, 10.0.0.2", "", "198.51.100.7"},
		{"real ip only", "172.18.0.5:1234", "", "198.51.100.7", "198.51.100.7"},
		{"garbage header", "172.18.0.5:1234", "<script>", "nope", "172.18.0.5"},
		{"nothing forwarded", "127.0.0.1:1234", "", "", "127.0.0.1"},
		{"ipv6", "[fd00::1]:1234", "2001:db8::5", "", "2001:db8::5"},
		{"mapped", "172.18.0.5:1234", "::ffff:198.51.100.7", "", "198.51.100.7"},
	} {
		r := &http.Request{RemoteAddr: c.peer, Header: http.Header{}}
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if c.real != "" {
			r.Header.Set("X-Real-IP", c.real)
		}
		if got := From(r); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
