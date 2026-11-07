package edge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/iamxvbaba/td/transport"

	"github.com/Greengrame/greengram-server/internal/clientip"
)

const sniffTimeout = 15 * time.Second

// Serve accepts MTProto over TCP: abridged, intermediate and padded
// intermediate, plain or obfuscated, all on one port.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		go s.serveRaw(ctx, c)
	}
}

// WebSocket returns the handler for /apiws (Telegram Web A) and starts
// feeding its connections into the same pipeline as TCP.
func (s *Server) WebSocket(ctx context.Context, addr net.Addr) http.Handler {
	ln, h := transport.WebsocketListener(addr)
	go s.Serve(ctx, ln)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// td's listener hands over the socket without the request, and behind
		// Traefik the socket only knows the proxy. The peer address is unique
		// while the handler runs, so it leads back to the headers.
		s.wsClients.Store(r.RemoteAddr, clientip.From(r))
		defer s.wsClients.Delete(r.RemoteAddr)
		h.ServeHTTP(w, r)
	})
}

// forwardedAddr is a client address read from proxy headers; there's no port.
type forwardedAddr string

func (a forwardedAddr) Network() string { return "tcp" }
func (a forwardedAddr) String() string  { return net.JoinHostPort(string(a), "0") }

func (s *Server) serveRaw(ctx context.Context, c net.Conn) {
	conn, err := sniff(c)
	if err != nil {
		// port scanners and half-open sockets land here all day
		slog.Debug("edge: drop connection", "remote", c.RemoteAddr(), "err", err)
		c.Close()
		return
	}
	remote, client := c.RemoteAddr(), c.RemoteAddr()
	if ip, ok := s.wsClients.Load(remote.String()); ok {
		client = forwardedAddr(ip.(string))
	}
	s.serveConn(ctx, conn, remote, client)
}

func sniff(c net.Conn) (transport.Conn, error) {
	c.SetReadDeadline(time.Now().Add(sniffTimeout))
	defer c.SetReadDeadline(time.Time{})

	var head [4]byte
	if _, err := io.ReadFull(c, head[:1]); err != nil {
		return nil, err
	}
	n := 1
	if head[0] != 0xef {
		if _, err := io.ReadFull(c, head[1:]); err != nil {
			return nil, err
		}
		n = 4
	}
	replay := &replayConn{Conn: c, r: io.MultiReader(bytes.NewReader(head[:n]), c)}
	tag := binary.LittleEndian.Uint32(head[:])
	plain := head[0] == 0xef || tag == 0xeeeeeeee || tag == 0xdddddddd

	var raw net.Conn = replay
	if !plain {
		obf, err := transport.ObfuscatedListener(&once{c: replay}).Accept()
		if err != nil {
			return nil, err
		}
		raw = obf
	}
	return transport.Listen(&once{c: raw}).Accept()
}

type replayConn struct {
	net.Conn
	r io.Reader
}

func (c *replayConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// once is a listener that hands out a single, already accepted conn, so td's
// listener wrappers can run per connection instead of on the accept loop.
type once struct {
	c    net.Conn
	used bool
}

func (o *once) Accept() (net.Conn, error) {
	if o.used {
		return nil, net.ErrClosed
	}
	o.used = true
	return o.c, nil
}

func (o *once) Close() error   { return nil }
func (o *once) Addr() net.Addr { return o.c.LocalAddr() }
