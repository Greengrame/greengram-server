package edge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"time"

	"github.com/iamxvbaba/td/mt"

	"github.com/Greengrame/greengram-server/internal/store"
)

// One salt per auth key per hour. The previous hour's salt is still accepted
// so a client that just got future_salts doesn't bounce off the edge.
const saltPeriod = time.Hour

type salts struct{ secret []byte }

func (s salts) at(keyID int64, window int64) int64 {
	m := hmac.New(sha256.New, s.secret)
	var b [16]byte
	binary.LittleEndian.PutUint64(b[:8], uint64(keyID))
	binary.LittleEndian.PutUint64(b[8:], uint64(window))
	m.Write(b[:])
	return int64(binary.LittleEndian.Uint64(m.Sum(nil)))
}

func window(now time.Time) int64 { return now.Unix() / int64(saltPeriod/time.Second) }

func (s salts) current(keyID int64, now time.Time) int64 { return s.at(keyID, window(now)) }

func (s salts) valid(keyID, salt int64, now time.Time) bool {
	w := window(now)
	return salt == s.at(keyID, w) || salt == s.at(keyID, w-1)
}

func (s salts) future(keyID int64, now time.Time, n int) []mt.FutureSalt {
	n = min(max(n, 1), 64)
	w := window(now)
	out := make([]mt.FutureSalt, 0, n)
	for i := range int64(n) {
		start := (w + i) * int64(saltPeriod/time.Second)
		out = append(out, mt.FutureSalt{
			ValidSince: int(start),
			ValidUntil: int(start + int64(2*saltPeriod/time.Second)),
			Salt:       s.at(keyID, w+i),
		})
	}
	return out
}

// handshakeSalt accepts the salt agreed in the key exchange for the key's
// first two periods. Telegram does the same; Android's tgnet answers a
// bad_server_salt on auth.bindTempAuthKey by redoing the PFS handshake,
// which then gets the same answer, forever.
func handshakeSalt(k store.AuthKey, salt int64, now time.Time) bool {
	return k.HandshakeSalt != 0 && salt == k.HandshakeSalt && now.Sub(k.CreatedAt) < 2*saltPeriod
}
