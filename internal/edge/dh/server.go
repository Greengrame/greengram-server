// Copied from github.com/iamxvbaba/td/exchange (MIT, Copyright (c) 2020
// Aleksandr Razumov), changed by Greengram: the DC check in p_q_inner_data
// takes a predicate, so one server accepts both 2 and 10002 (our Android
// build runs on the test backend); nonces, pq, the key fingerprint and g_b
// are checked as the spec asks; temp keys report expires_in. See
// third_party/td.

package dh

import (
	"context"
	"math/big"

	"github.com/go-faster/errors"
	"github.com/gotd/log"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto/codec"
)

func serverError(code int32, err error) error {
	return &exchange.ServerExchangeError{Code: code, Err: err}
}

var errNonce = errors.New("nonce or server_nonce mismatch")

func sameInt(b []byte, x *big.Int) bool { return new(big.Int).SetBytes(b).Cmp(x) == 0 }

// req_pq#60469778 or req_pq_multi#be7e8ef1
type reqPQ struct {
	Type  uint32
	Nonce bin.Int128
}

func (r *reqPQ) Decode(b *bin.Buffer) error {
	var (
		legacy mt.ReqPqRequest
		multi  mt.ReqPqMultiRequest
	)
	id, err := b.PeekID()
	if err != nil {
		return err
	}
	r.Type = id
	switch id {
	case legacy.TypeID():
		if err := legacy.Decode(b); err != nil {
			return err
		}
		r.Nonce = legacy.Nonce
		return nil
	case multi.TypeID():
		if err := multi.Decode(b); err != nil {
			return err
		}
		r.Nonce = multi.Nonce
		return nil
	default:
		return bin.NewUnexpectedID(id)
	}
}

type reqOrDH struct {
	Type uint32
	DH   mt.ReqDHParamsRequest
	Req  reqPQ
}

func (r *reqOrDH) Decode(b *bin.Buffer) error {
	id, err := b.PeekID()
	if err != nil {
		return err
	}
	r.Type = id
	switch id {
	case r.DH.TypeID():
		return r.DH.Decode(b)
	default:
		return r.Req.Decode(b)
	}
}

// Run runs server-side flow.
// If b parameter is not nil, it will be used as first read message.
// Otherwise, it will be read from connection.
func (s ServerExchange) Run(ctx context.Context) (Result, error) {
	wrapKeyNotFound := func(err error) error {
		return serverError(codec.CodeAuthKeyNotFound, err)
	}

	// 1. Client sends query to server
	// req_pq#60469778 nonce:int128 = ResPQ; // legacy
	// req_pq_multi#be7e8ef1 nonce:int128 = ResPQ;
	var req reqPQ
	b := new(bin.Buffer)
	if err := s.readUnencrypted(ctx, b, &req); err != nil {
		return Result{}, err
	}
	s.log.Debug(ctx, "Received client ReqPqMultiRequest")

	serverNonce, err := crypto.RandInt128(s.rand)
	if err != nil {
		return Result{}, errors.Wrap(err, "generate server nonce")
	}

	// 2. Server sends response of the form
	//
	// resPQ#05162463 nonce:int128 server_nonce:int128 pq:string server_public_key_fingerprints:Vector long = ResPQ;
	pq, p, q, err := s.rng.PQ()
	if err != nil {
		return Result{}, errors.Wrap(err, "generate pq")
	}

SendResPQ:
	s.log.Debug(ctx, "Sending ResPQ", log.String("pq", pq.String()))
	if err := s.writeUnencrypted(ctx, b, &mt.ResPQ{
		Pq:          pq.Bytes(),
		Nonce:       req.Nonce,
		ServerNonce: serverNonce,
		ServerPublicKeyFingerprints: []int64{
			s.key.Fingerprint(),
		},
	}); err != nil {
		return Result{}, err
	}

	// 4. Client sends query to server
	//
	// req_DH_params#d712e4be nonce:int128 server_nonce:int128 p:string
	//  q:string public_key_fingerprint:long encrypted_data:string = Server_DH_Params
	var dhParams reqOrDH
	if err := s.readUnencrypted(ctx, b, &dhParams); err != nil {
		return Result{}, err
	}
	switch dhParams.Type {
	case mt.ReqPqRequestTypeID, mt.ReqPqMultiRequestTypeID:
		// Client can send fake req_pq on start. Ignore it.
		//
		// Next one should be not fake.
		s.log.Debug(ctx, "Received ReqPQ again")
		req = dhParams.Req
		goto SendResPQ
	default:
		s.log.Debug(ctx, "Received client ReqDHParamsRequest")
	}
	// all of this before the RSA decryption, which is the expensive part
	if r := dhParams.DH; r.Nonce != req.Nonce || r.ServerNonce != serverNonce {
		return Result{}, errNonce
	} else if r.PublicKeyFingerprint != s.key.Fingerprint() {
		return Result{}, wrapKeyNotFound(errors.New("unknown public key fingerprint"))
	} else if !sameInt(r.P, p) || !sameInt(r.Q, q) {
		return Result{}, errors.New("wrong p or q")
	}

	var (
		innerData mt.PQInnerData
		expiresIn int
		dc        int
	)
	{
		r, err := crypto.DecodeRSAPad(dhParams.DH.EncryptedData, s.key.RSA)
		if err != nil {
			return Result{}, wrapKeyNotFound(err)
		}
		b.ResetTo(r)

		d, err := mt.DecodePQInnerData(b)
		if err != nil {
			return Result{}, err
		}

		switch innerDataDC := d.(type) {
		case *mt.PQInnerDataDC:
			// Legacy/permanent flow.
			if !s.acceptDC(innerDataDC.DC) {
				err := errors.Errorf("wrong DC ID %d", innerDataDC.DC)
				return Result{}, serverError(codec.CodeWrongDC, err)
			}
			dc = innerDataDC.DC
		case *mt.PQInnerDataTempDC:
			// PFS flow: same DC validation, plus the key's lifetime
			if !s.acceptDC(innerDataDC.DC) {
				err := errors.Errorf("wrong DC ID %d", innerDataDC.DC)
				return Result{}, serverError(codec.CodeWrongDC, err)
			}
			if innerDataDC.ExpiresIn <= 0 {
				return Result{}, errors.Errorf("temp key expires_in %d", innerDataDC.ExpiresIn)
			}
			expiresIn = innerDataDC.ExpiresIn
			dc = innerDataDC.DC
		}
		if d.GetNonce() != req.Nonce || d.GetServerNonce() != serverNonce {
			return Result{}, errNonce
		}
		if !sameInt(d.GetPq(), pq) || !sameInt(d.GetP(), p) || !sameInt(d.GetQ(), q) {
			return Result{}, errors.New("p_q_inner_data: wrong pq")
		}

		innerData = mt.PQInnerData{
			Pq:          d.GetPq(),
			P:           d.GetP(),
			Q:           d.GetQ(),
			Nonce:       d.GetNonce(),
			ServerNonce: d.GetServerNonce(),
			NewNonce:    d.GetNewNonce(),
		}
	}

	dhPrime, err := s.rng.DhPrime()
	if err != nil {
		return Result{}, errors.Wrap(err, "generate dh_prime")
	}

	g := 3
	a, ga, err := s.rng.GA(g, dhPrime)
	if err != nil {
		return Result{}, errors.Wrap(err, "generate g_a")
	}

	data := mt.ServerDHInnerData{
		Nonce:       req.Nonce,
		ServerNonce: serverNonce,
		G:           g,
		GA:          ga.Bytes(),
		DhPrime:     dhPrime.Bytes(),
		ServerTime:  int(s.clock.Now().Unix()),
	}

	b.Reset()
	if err := data.Encode(b); err != nil {
		return Result{}, err
	}

	key, iv := crypto.TempAESKeys(innerData.NewNonce.BigInt(), serverNonce.BigInt())
	answer, err := crypto.EncryptExchangeAnswer(s.rand, b.Raw(), key, iv)
	if err != nil {
		return Result{}, err
	}

	s.log.Debug(ctx, "Sending ServerDHParamsOk", log.Int("g", g))
	// 5. Server responds with Server_DH_Params.
	if err := s.writeUnencrypted(ctx, b, &mt.ServerDHParamsOk{
		Nonce:           req.Nonce,
		ServerNonce:     serverNonce,
		EncryptedAnswer: answer,
	}); err != nil {
		return Result{}, err
	}

	var clientDhParams mt.SetClientDHParamsRequest
	if err := s.readUnencrypted(ctx, b, &clientDhParams); err != nil {
		return Result{}, err
	}
	s.log.Debug(ctx, "Received client SetClientDHParamsRequest")
	if clientDhParams.Nonce != req.Nonce || clientDhParams.ServerNonce != serverNonce {
		return Result{}, errNonce
	}

	decrypted, err := crypto.DecryptExchangeAnswer(clientDhParams.EncryptedData, key, iv)
	if err != nil {
		err = errors.Wrap(err, "decrypt exchange answer")
		return Result{}, wrapKeyNotFound(err)
	}
	b.ResetTo(decrypted)

	var clientInnerData mt.ClientDHInnerData
	if err := clientInnerData.Decode(b); err != nil {
		return Result{}, wrapKeyNotFound(err)
	}

	if clientInnerData.Nonce != req.Nonce || clientInnerData.ServerNonce != serverNonce {
		return Result{}, errNonce
	}
	gB := big.NewInt(0).SetBytes(clientInnerData.GB)
	// 1 < g_b < p-1 and 2^{2048-64} < g_b < p - 2^{2048-64}
	if err := crypto.CheckDHParams(dhPrime, big.NewInt(int64(g)), ga, gB); err != nil {
		return Result{}, err
	}
	var authKey crypto.Key
	if !crypto.FillBytes(big.NewInt(0).Exp(gB, a, dhPrime), authKey[:]) {
		err := errors.New("auth_key is too big")
		return Result{}, wrapKeyNotFound(err)
	}

	// DH key exchange complete
	// 8. Server responds in one of three ways:
	// dh_gen_ok#3bcbf734 nonce:int128 server_nonce:int128
	// 	new_nonce_hash1:int128 = Set_client_DH_params_answer;
	s.log.Debug(ctx, "Sending DhGenOk")
	if err := s.writeUnencrypted(ctx, b, &mt.DhGenOk{
		Nonce:         req.Nonce,
		ServerNonce:   serverNonce,
		NewNonceHash1: crypto.NonceHash1(innerData.NewNonce, authKey),
	}); err != nil {
		return Result{}, err
	}

	serverSalt := crypto.ServerSalt(innerData.NewNonce, serverNonce)
	return Result{
		ServerExchangeResult: exchange.ServerExchangeResult{
			Key:        authKey.WithID(),
			ServerSalt: serverSalt,
		},
		ExpiresIn: expiresIn,
		DC:        dc,
	}, nil
}
