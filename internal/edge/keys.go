package edge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/iamxvbaba/td/crypto"

	"github.com/Greengrame/greengram-server/internal/store"
)

// KeyStore persists auth keys; *store.Store implements it, MemKeys is for tests.
type KeyStore interface {
	AuthKey(ctx context.Context, id int64) (store.AuthKey, bool, error)
	PutAuthKey(ctx context.Context, k crypto.AuthKey, handshakeSalt int64, expiresAt time.Time, testBackend *bool) error
	BindTempAuthKey(ctx context.Context, temp, perm int64, expiresAt time.Time) error
	SetAuthKeyLayer(ctx context.Context, id int64, layer int) error
	MarkAuthKeyUsed(ctx context.Context, id int64) error
	DeleteAuthKey(ctx context.Context, id int64) error
	PruneAuthKeys(ctx context.Context, now, unusedBefore time.Time, keep []int64) (int64, error)
}

type MemKeys struct {
	mu   sync.Mutex
	keys map[int64]store.AuthKey
}

func NewMemKeys() *MemKeys { return &MemKeys{keys: map[int64]store.AuthKey{}} }

func (m *MemKeys) AuthKey(_ context.Context, id int64) (store.AuthKey, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.keys[id]
	return k, ok, nil
}

func (m *MemKeys) PutAuthKey(_ context.Context, k crypto.AuthKey, salt int64, expiresAt time.Time, testBackend *bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.keys[store.KeyID(k.ID)]; !ok {
		m.keys[store.KeyID(k.ID)] = store.AuthKey{Key: k, HandshakeSalt: salt, CreatedAt: time.Now(), ExpiresAt: expiresAt, TestBackend: testBackend}
	}
	return nil
}

func (m *MemKeys) BindTempAuthKey(_ context.Context, temp, perm int64, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.keys[temp]
	k.PermID, k.Used = perm, true
	if k.ExpiresAt.IsZero() || expiresAt.Before(k.ExpiresAt) {
		k.ExpiresAt = expiresAt
	}
	m.keys[temp] = k
	if p, ok := m.keys[perm]; ok {
		p.Used = true
		m.keys[perm] = p
	}
	return nil
}

func (m *MemKeys) MarkAuthKeyUsed(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k, ok := m.keys[id]; ok {
		k.Used = true
		m.keys[id] = k
	}
	return nil
}

// PruneAuthKeys here can't see sign-ins (they live in Postgres), only
// expiry and use.
func (m *MemKeys) PruneAuthKeys(_ context.Context, now, unusedBefore time.Time, keep []int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, k := range m.keys {
		expired := !k.ExpiresAt.IsZero() && !k.ExpiresAt.After(now)
		if (expired || !k.Used && k.CreatedAt.Before(unusedBefore)) && !slices.Contains(keep, id) {
			delete(m.keys, id)
			n++
		}
	}
	return n, nil
}

func (m *MemKeys) SetAuthKeyLayer(_ context.Context, id int64, layer int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.keys[id]
	k.Layer = layer
	m.keys[id] = k
	return nil
}

func (m *MemKeys) DeleteAuthKey(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.keys, id)
	return nil
}

// LoadRSAKey reads dir/server.pem or makes one. Clients pin the public half
// (tdesktop compiles it in), so it must never change once a build ships.
// The gateway's server.pem works as is.
func LoadRSAKey(dir string) (*rsa.PrivateKey, error) {
	path := filepath.Join(dir, "server.pem")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return createRSAKey(dir, path)
	}
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("server.pem: no PEM block")
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func createRSAKey(dir, path string) (*rsa.PrivateKey, error) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	priv := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	if err := os.WriteFile(path, priv, 0o600); err != nil {
		return nil, err
	}
	pub := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&k.PublicKey)})
	if err := os.WriteFile(filepath.Join(dir, "public.pem"), pub, 0o644); err != nil {
		return nil, err
	}
	return k, nil
}

// LoadSecret reads dir/salt.key (32 random bytes) or makes one. Server salts
// are derived from it, so they survive restarts without a table.
func LoadSecret(dir string) ([]byte, error) {
	path := filepath.Join(dir, "salt.key")
	b, err := os.ReadFile(path)
	if err == nil && len(b) == 32 {
		return b, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	b = make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return b, os.WriteFile(path, b, 0o600)
}

func (m *MemKeys) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.keys)
}
