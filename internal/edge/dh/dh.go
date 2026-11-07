// Package dh is the server side of the MTProto key exchange.
package dh

import (
	"io"

	"github.com/gotd/log"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/transport"
)

type ServerExchange struct {
	unencryptedWriter
	rand     io.Reader
	log      log.Helper
	rng      rng
	key      exchange.PrivateKey
	acceptDC func(int) bool
}

// Result is a finished exchange. ExpiresIn is set (seconds) when the client
// asked for a temp key with p_q_inner_data_temp.
type Result struct {
	exchange.ServerExchangeResult
	ExpiresIn int
	// DC is what p_q_inner_data named, 0 for the old form without one:
	// 10000+dc from a client on the test backend, negative for media keys
	DC int
}

// NewServer runs over conn with key; acceptDC says which DC numbers in the
// client's p_q_inner_data are this server.
func NewServer(conn transport.Conn, key exchange.PrivateKey, rand io.Reader, clk clock.Clock, acceptDC func(int) bool) ServerExchange {
	return ServerExchange{
		unencryptedWriter: unencryptedWriter{
			clock:   clk,
			conn:    conn,
			timeout: exchange.DefaultTimeout,
			input:   proto.MessageFromClient,
			output:  proto.MessageServerResponse,
		},
		rand:     rand,
		log:      log.For(nil),
		rng:      rng{rand: rand},
		key:      key,
		acceptDC: acceptDC,
	}
}
