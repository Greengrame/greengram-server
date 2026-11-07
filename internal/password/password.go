// Package password hashes and checks account passwords: argon2id in PHC
// form, the same parameters Bun.password used on the web server, so imported
// hashes verify as they are.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	memory  = 19456 // KiB
	time    = 2
	threads = 1
	keyLen  = 32
	saltLen = 32 // what Bun uses
)

var b64 = base64.RawStdEncoding

func Hash(pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(pw), salt, time, memory, threads, keyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", memory, time, threads, b64.EncodeToString(salt), b64.EncodeToString(sum)), nil
}

// Verify checks pw against a PHC string, honouring the parameters stored in
// it. A malformed hash is a plain "no".
func Verify(pw, phc string) bool {
	parts := strings.Split(phc, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || m > 1<<20 || t == 0 || t > 16 || p == 0 {
		return false
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > 128 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// placeholder is verified against when the account doesn't exist, so a
// wrong username and a wrong password take the same time.
var placeholder = func() string {
	h, err := Hash("invalid-placeholder-password")
	if err != nil {
		panic(err)
	}
	return h
}()

// Check is Verify with the timing guard for missing accounts.
func Check(pw, phc string) bool {
	if phc == "" {
		Verify(pw, placeholder)
		return false
	}
	return Verify(pw, phc)
}

var ErrLength = errors.New("Use a password between 10 and 256 characters.")

func Validate(pw string) error {
	if len(pw) < 10 || len(pw) > 256 {
		return ErrLength
	}
	return nil
}
