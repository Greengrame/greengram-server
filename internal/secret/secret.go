// Package secret is the instance secret: 32 bytes that key phone lookups
// and encrypt phones at rest. Same formats as greengram-web's
// secret-storage.js, so an imported database decrypts with the same file.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type Key struct{ b []byte }

// Load reads path or creates it (mode 0600). Copy the web server's
// instance-secret.key here before importing its database.
func Load(path string) (*Key, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		b = make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return nil, err
		}
		return &Key{b}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, errors.New("instance secret must be exactly 32 bytes")
	}
	return &Key{b}, nil
}

func FromBytes(b []byte) *Key { return &Key{append([]byte(nil), b...)} }

// Lookup is a keyed hash for finding a row by a value we store encrypted.
func (k *Key) Lookup(value string) string {
	m := hmac.New(sha256.New, k.b)
	m.Write([]byte("lookup:v1:" + value))
	return hex.EncodeToString(m.Sum(nil))
}

// PhoneLookup matches phoneLookup() in confidential-fields.js.
func (k *Key) PhoneLookup(phone string) string {
	var digits strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	return k.Lookup("phone:+" + digits.String())
}

const encPrefix = "ggenc:v1:"

// EncryptPhone gives "ggenc:v1:" + base64(nonce | tag | ciphertext), with
// context as the GCM additional data ("user:<id>", "auth-flow:<id>").
func (k *Key) EncryptPhone(value, context string) (string, error) {
	if value == "" {
		return "", nil
	}
	gcm, err := k.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(value), []byte(context))
	ct, tag := sealed[:len(sealed)-16], sealed[len(sealed)-16:]
	out := append(append(append([]byte{}, nonce...), tag...), ct...)
	return encPrefix + base64.StdEncoding.EncodeToString(out), nil
}

func (k *Key) DecryptPhone(value, context string) (string, error) {
	if !strings.HasPrefix(value, encPrefix) {
		return value, nil // never encrypted (very old rows)
	}
	raw, err := base64.StdEncoding.DecodeString(value[len(encPrefix):])
	if err != nil || len(raw) < 28 {
		return "", errors.New("encrypted phone is invalid")
	}
	gcm, err := k.gcm()
	if err != nil {
		return "", err
	}
	nonce, tag, ct := raw[:12], raw[12:28], raw[28:]
	plain, err := gcm.Open(nil, nonce, append(append([]byte{}, ct...), tag...), []byte(context))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (k *Key) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(k.b)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
