// Copied from github.com/iamxvbaba/td/exchange (MIT, Copyright (c) 2020
// Aleksandr Razumov), changed by Greengram: pq is random per handshake.
// See third_party/td.

package dh

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"math/big"

	"github.com/go-faster/errors"

	"github.com/iamxvbaba/td/crypto"
)

type rng struct {
	rand io.Reader
}

func (s rng) bigFromHex(hexString string) (p *big.Int, err error) {
	data, err := hex.DecodeString(hexString)
	if err != nil {
		return nil, errors.Wrap(err, "decode hex string")
	}

	return big.NewInt(0).SetBytes(data), nil
}

// PQ makes a fresh pq = p*q, p < q, both 31-bit primes like Telegram's. A
// fixed one lets a client skip the factoring, the only work the handshake
// asks of it before the server spends an RSA decryption.
func (s rng) PQ() (pq, p, q *big.Int, err error) {
	for {
		if p, err = rand.Prime(s.rand, 31); err != nil {
			return nil, nil, nil, err
		}
		if q, err = rand.Prime(s.rand, 31); err != nil {
			return nil, nil, nil, err
		}
		switch p.Cmp(q) {
		case 0:
			continue
		case 1:
			p, q = q, p
		}
		return new(big.Int).Mul(p, q), p, q, nil
	}
}

// GA returns a and g_a params.
func (s rng) GA(g int, dhPrime *big.Int) (a, ga *big.Int, err error) {
	if err := crypto.CheckGP(g, dhPrime); err != nil {
		return nil, nil, err
	}

	gBig := big.NewInt(int64(g))
	one := big.NewInt(1)
	dhPrimeMinusOne := big.NewInt(0).Sub(dhPrime, one)

	safetyRangeMin := big.NewInt(0).Exp(big.NewInt(2), big.NewInt(crypto.RSAKeyBits-64), nil)
	safetyRangeMax := big.NewInt(0).Sub(dhPrime, safetyRangeMin)

	randMax := big.NewInt(0).SetBit(big.NewInt(0), crypto.RSAKeyBits, 1)
	for {
		a, err = rand.Int(s.rand, randMax)
		if err != nil {
			return
		}

		ga = big.NewInt(0).Exp(gBig, a, dhPrime)
		if crypto.InRange(ga, one, dhPrimeMinusOne) && crypto.InRange(ga, safetyRangeMin, safetyRangeMax) {
			return
		}
	}
}

// DhPrime returns Telegram's dh_prime.
func (s rng) DhPrime() (p *big.Int, err error) {
	return s.bigFromHex("C71CAEB9C6B1C9048E6C522F70F13F73980D40238E3E21C14934D037563D930F" +
		"48198A0AA7C14058229493D22530F4DBFA336F6E0AC925139543AED44CCE7C37" +
		"20FD51F69458705AC68CD4FE6B6B13ABDC9746512969328454F18FAF8C595F64" +
		"2477FE96BB2A941D5BCD1D4AC8CC49880708FA9B378E3C4F3A9060BEE67CF9A4" +
		"A4A695811051907E162753B56B0F6B410DBA74D8A84B2A14B3144E0EF1284754" +
		"FD17ED950D5965B4B9DD46582DB1178D169C6BC465B0D6FF9CA3928FEF5B9AE4" +
		"E418FC15E83EBEA0F87FA9FF5EED70050DED2849F47BF959D956850CE929851F" +
		"0D8115F635B105EE2E4E15D04B2454BF6F4FADF034B10403119CD8E3B92FCC5B")
}
