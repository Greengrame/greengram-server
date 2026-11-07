package dh

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto"
)

// pipe is one end of an in-memory transport.Conn pair.
type pipe struct {
	in, out chan []byte
	done    chan struct{}
	once    *sync.Once
}

func pipes() (pipe, pipe) {
	a, b, done := make(chan []byte, 4), make(chan []byte, 4), make(chan struct{})
	once := &sync.Once{}
	return pipe{in: a, out: b, done: done, once: once}, pipe{in: b, out: a, done: done, once: once}
}

func (p pipe) Send(_ context.Context, b *bin.Buffer) error {
	select {
	case p.out <- b.Copy():
		return nil
	case <-p.done:
		return errors.New("closed")
	}
}

func (p pipe) Recv(ctx context.Context, b *bin.Buffer) error {
	select {
	case m := <-p.in:
		b.ResetTo(m)
		return nil
	case <-p.done:
		return errors.New("closed")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p pipe) Close() error { p.once.Do(func() { close(p.done) }); return nil }

type tamper struct {
	serverNonce bool     // in req_DH_params
	p           bool     // req_DH_params names other factors
	temp        bool     // p_q_inner_data_temp_dc
	expiresIn   int      // for temp
	gB          *big.Int // instead of g^b
}

var testKey = sync.OnceValue(func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
})

// handshake drives the server through a key exchange as a client would,
// with tm's changes, and returns what the server made of it.
func handshake(t *testing.T, tm tamper) (Result, error) {
	t.Helper()
	// td's DecomposePQ takes up to a second, several under -race
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cli, srv := pipes()
	key := exchange.PrivateKey{RSA: testKey()}

	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := NewServer(srv, key, rand.Reader, clock.System, func(int) bool { return true }).Run(ctx)
		srv.Close()
		done <- outcome{res, err}
	}()

	b := new(bin.Buffer)
	send := func(o bin.Encoder) {
		var body bin.Buffer
		if err := o.Encode(&body); err != nil {
			t.Fatal(err)
		}
		msg := proto.UnencryptedMessage{MessageID: int64(proto.NewMessageID(time.Now(), proto.MessageFromClient)), MessageData: body.Buf}
		b.Reset()
		if err := msg.Encode(b); err != nil {
			t.Fatal(err)
		}
		cli.Send(ctx, b)
	}
	recv := func() (*bin.Buffer, bool) {
		if err := cli.Recv(ctx, b); err != nil {
			return nil, false
		}
		var msg proto.UnencryptedMessage
		if err := msg.Decode(b); err != nil {
			t.Fatal(err)
		}
		return &bin.Buffer{Buf: msg.MessageData}, true
	}
	result := func() (Result, error) {
		o := <-done
		return o.res, o.err
	}

	nonce, _ := crypto.RandInt128(rand.Reader)
	send(&mt.ReqPqMultiRequest{Nonce: nonce})
	rb, ok := recv()
	if !ok {
		return result()
	}
	var res mt.ResPQ
	if err := res.Decode(rb); err != nil {
		t.Fatal(err)
	}
	p, q, err := crypto.DecomposePQ(new(big.Int).SetBytes(res.Pq), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	newNonce, _ := crypto.RandInt256(rand.Reader)
	var inner bin.Encoder = &mt.PQInnerDataDC{Pq: res.Pq, P: p.Bytes(), Q: q.Bytes(), Nonce: nonce, ServerNonce: res.ServerNonce, NewNonce: newNonce, DC: 2}
	if tm.temp {
		inner = &mt.PQInnerDataTempDC{Pq: res.Pq, P: p.Bytes(), Q: q.Bytes(), Nonce: nonce, ServerNonce: res.ServerNonce, NewNonce: newNonce, DC: 2, ExpiresIn: tm.expiresIn}
	}
	var ib bin.Buffer
	if err := inner.Encode(&ib); err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.RSAPad(ib.Buf, &testKey().PublicKey, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	req := &mt.ReqDHParamsRequest{Nonce: nonce, ServerNonce: res.ServerNonce, P: p.Bytes(), Q: q.Bytes(), PublicKeyFingerprint: key.Fingerprint(), EncryptedData: enc}
	if tm.serverNonce {
		req.ServerNonce[0] ^= 1
	}
	if tm.p {
		req.P = big.NewInt(3).Bytes()
	}
	send(req)
	rb, ok = recv()
	if !ok {
		return result()
	}
	var dhOK mt.ServerDHParamsOk
	if err := dhOK.Decode(rb); err != nil {
		t.Fatal(err)
	}
	aesKey, iv := crypto.TempAESKeys(newNonce.BigInt(), res.ServerNonce.BigInt())
	plain, err := crypto.DecryptExchangeAnswer(dhOK.EncryptedAnswer, aesKey, iv)
	if err != nil {
		t.Fatal(err)
	}
	var sdh mt.ServerDHInnerData
	if err := sdh.Decode(&bin.Buffer{Buf: plain}); err != nil {
		t.Fatal(err)
	}
	prime := new(big.Int).SetBytes(sdh.DhPrime)
	bParam, _ := rand.Int(rand.Reader, prime)
	gB := new(big.Int).Exp(big.NewInt(int64(sdh.G)), bParam, prime)
	if tm.gB != nil {
		gB = tm.gB
	}
	var cb bin.Buffer
	if err := (&mt.ClientDHInnerData{Nonce: nonce, ServerNonce: res.ServerNonce, GB: gB.Bytes()}).Encode(&cb); err != nil {
		t.Fatal(err)
	}
	cenc, err := crypto.EncryptExchangeAnswer(rand.Reader, cb.Buf, aesKey, iv)
	if err != nil {
		t.Fatal(err)
	}
	send(&mt.SetClientDHParamsRequest{Nonce: nonce, ServerNonce: res.ServerNonce, EncryptedData: cenc})
	recv()
	return result()
}

func TestHandshake(t *testing.T) {
	if _, err := handshake(t, tamper{}); err != nil {
		t.Fatalf("plain exchange: %v", err)
	}
	res, err := handshake(t, tamper{temp: true, expiresIn: 3600})
	if err != nil || res.ExpiresIn != 3600 {
		t.Fatalf("temp exchange: expires_in %d, err %v", res.ExpiresIn, err)
	}
}

func TestHandshakeRejects(t *testing.T) {
	prime, _ := rng{}.DhPrime()
	nearTop := new(big.Int).Sub(prime, big.NewInt(5))
	cases := map[string]tamper{
		"server_nonce":          {serverNonce: true},
		"factors":               {p: true},
		"temp without lifetime": {temp: true},
		"g_b = 1":               {gB: big.NewInt(1)},
		"g_b below 2^{2048-64}": {gB: big.NewInt(1 << 40)},
		"g_b near dh_prime":     {gB: nearTop},
		"g_b = dh_prime - 1":    {gB: new(big.Int).Sub(prime, big.NewInt(1))},
	}
	for name, tm := range cases {
		if _, err := handshake(t, tm); err == nil {
			t.Errorf("%s: exchange went through", name)
		} else {
			t.Logf("%s: %v", name, err)
		}
	}
}

func TestPQIsFreshAndFactors(t *testing.T) {
	r := rng{rand: rand.Reader}
	pq1, p, q, err := r.PQ()
	if err != nil {
		t.Fatal(err)
	}
	if p.Cmp(q) >= 0 || new(big.Int).Mul(p, q).Cmp(pq1) != 0 || !p.ProbablyPrime(20) || !q.ProbablyPrime(20) || pq1.BitLen() > 63 {
		t.Fatalf("pq %v = %v * %v", pq1, p, q)
	}
	pq2, _, _, _ := r.PQ()
	if pq1.Cmp(pq2) == 0 {
		t.Fatal("pq repeats")
	}
}
