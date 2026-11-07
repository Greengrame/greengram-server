package edge_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/iamxvbaba/td/session"
	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/telegram/dcs"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/transport"

	"github.com/Greengrame/greengram-server/internal/api"
	"github.com/Greengrame/greengram-server/internal/edge"
)

const dc = 2

type testServer struct {
	key  *rsa.PrivateKey
	keys *edge.MemKeys
	tcp  int
	ws   string
}

func start(t *testing.T) *testServer {
	t.Helper()
	return startAs(t, dc)
}

// startAs serves help.getConfig as DC this; td only takes a config whose
// this_dc is the DC it dialled
func startAs(t *testing.T, this int) *testServer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 32)
	rand.Read(secret)
	keys := edge.NewMemKeys()
	// configured like prod (test-backend Android), dialled by a plain DC 2 client
	srv := edge.New(edge.Config{RSA: k, Secret: secret, Keys: keys, ExchangeDC: 10000 + dc})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	api.RegisterHelp(srv, api.Instance{DC: this, Host: "127.0.0.1", Port: port, BaseURL: "http://greengram.test", TestMode: true})
	go srv.Serve(ctx, ln)

	hl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/apiws", srv.WebSocket(ctx, hl.Addr()))
	hs := &http.Server{Handler: mux}
	go hs.Serve(hl)
	t.Cleanup(func() { hs.Close() })

	return &testServer{key: k, keys: keys, tcp: port, ws: fmt.Sprintf("ws://%s/apiws", hl.Addr())}
}

func (ts *testServer) client(resolver dcs.Resolver, storage telegram.SessionStorage) *telegram.Client {
	return ts.clientOn(dc, resolver, storage)
}

// clientOn names id in the key exchange: 10000+dc is how a client on
// Telegram's test backend (our Android build) dials.
func (ts *testServer) clientOn(id int, resolver dcs.Resolver, storage telegram.SessionStorage) *telegram.Client {
	return telegram.NewClient(1, "test", telegram.Options{
		PublicKeys: []telegram.PublicKey{{RSA: &ts.key.PublicKey}},
		DC:         id,
		DCList: dcs.List{
			Options: []tg.DCOption{{ID: id, IPAddress: "127.0.0.1", Port: ts.tcp}},
			Domains: map[int]string{id: ts.ws},
		},
		Resolver:       resolver,
		NoUpdates:      true,
		SessionStorage: storage,
		Logger:         clientLog(),
	})
}

func getConfig(t *testing.T, c *telegram.Client) *tg.Config {
	t.Helper()
	// td's client factors pq slowly under -race on a small box (seen ~10s)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var cfg *tg.Config
	err := c.Run(ctx, func(ctx context.Context) error {
		var err error
		cfg, err = c.API().HelpGetConfig(ctx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestGetConfig(t *testing.T) {
	ts := start(t)
	cases := map[string]dcs.Resolver{
		"intermediate":            dcs.Plain(dcs.PlainOptions{Protocol: transport.Intermediate}),
		"padded":                  dcs.Plain(dcs.PlainOptions{Protocol: transport.PaddedIntermediate}),
		"abridged obfuscated":     dcs.Plain(dcs.PlainOptions{Protocol: transport.Abridged, Obfuscated: true}),
		"websocket (Web A)":       dcs.Websocket(dcs.WebsocketOptions{}),
		"intermediate obfuscated": dcs.Plain(dcs.PlainOptions{Protocol: transport.Intermediate, Obfuscated: true}),
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := getConfig(t, ts.client(r, &session.StorageMemory{}))
			if cfg.ThisDC != dc || len(cfg.DCOptions) != 1 || cfg.DCOptions[0].Port != ts.tcp {
				t.Fatalf("unexpected config: this_dc=%d options=%+v", cfg.ThisDC, cfg.DCOptions)
			}
			if cfg.MeURLPrefix != "http://greengram.test/u/" {
				t.Fatalf("me_url_prefix = %q", cfg.MeURLPrefix)
			}
			// the instance takes test-backend clients, but this one isn't:
			// Web A would put "[T]" in its title
			if cfg.TestMode {
				t.Fatal("test_mode for a client that named dc 2")
			}
		})
	}
	t.Run("test backend (Android)", func(t *testing.T) {
		ts := startAs(t, 10000+dc)
		cfg := getConfig(t, ts.clientOn(10000+dc, dcs.Plain(dcs.PlainOptions{Protocol: transport.Intermediate}), &session.StorageMemory{}))
		if !cfg.TestMode {
			t.Fatal("no test_mode for a client that named 10000+dc")
		}
	})
}

// A second run with the same session storage must reuse the auth key, not
// redo the exchange.
func TestKeyReuse(t *testing.T) {
	ts := start(t)
	storage := &session.StorageMemory{}
	r := dcs.Plain(dcs.PlainOptions{Protocol: transport.Intermediate})
	getConfig(t, ts.client(r, storage))
	getConfig(t, ts.client(r, storage))
	if n := ts.keys.Len(); n != 1 {
		t.Fatalf("expected 1 auth key after reconnect, got %d", n)
	}
}
