package store

import (
	"context"
	"encoding/binary"
	"errors"
	"time"

	"github.com/iamxvbaba/td/crypto"
	"github.com/jackc/pgx/v5"
)

// KeyID turns the wire auth_key_id into the bigint we store.
func KeyID(id [8]byte) int64 { return int64(binary.LittleEndian.Uint64(id[:])) }

type AuthKey struct {
	Key    crypto.AuthKey
	PermID int64 // 0 unless this is a bound temp key
	Layer  int
	// from the DH exchange; valid for a while after CreatedAt
	HandshakeSalt int64
	CreatedAt     time.Time
	ExpiresAt     time.Time // temp keys; zero for permanent ones
	Used          bool      // carried a call or a bind; unused keys get pruned
	// TestBackend: the exchange named 10000+dc, as Telegram's test backend
	// does (our Android build). nil for keys made before it was kept.
	TestBackend *bool
}

func (s *Store) AuthKey(ctx context.Context, id int64) (AuthKey, bool, error) {
	var (
		raw     []byte
		permID  *int64
		expires *time.Time
		k       AuthKey
	)
	err := s.Pool.QueryRow(ctx, "SELECT key, perm_id, layer, handshake_salt, created_at, expires_at, used, test_backend FROM auth_keys WHERE id=$1", id).
		Scan(&raw, &permID, &k.Layer, &k.HandshakeSalt, &k.CreatedAt, &expires, &k.Used, &k.TestBackend)
	if errors.Is(err, pgx.ErrNoRows) {
		return k, false, nil
	}
	if err != nil {
		return k, false, err
	}
	copy(k.Key.Value[:], raw)
	k.Key = k.Key.Value.WithID()
	if permID != nil {
		k.PermID = *permID
	}
	if expires != nil {
		k.ExpiresAt = *expires
	}
	return k, true, nil
}

// PutAuthKey stores a key from a finished exchange; expiresAt is zero
// unless the client asked for a temp key, testBackend nil when the
// exchange didn't name a DC.
func (s *Store) PutAuthKey(ctx context.Context, k crypto.AuthKey, salt int64, expiresAt time.Time, testBackend *bool) error {
	var exp *time.Time
	if !expiresAt.IsZero() {
		exp = &expiresAt
	}
	_, err := s.Pool.Exec(ctx, "INSERT INTO auth_keys(id, key, handshake_salt, expires_at, test_backend) VALUES($1, $2, $3, $4, $5) ON CONFLICT (id) DO NOTHING",
		KeyID(k.ID), k.Value[:], salt, exp, testBackend)
	return err
}

// BindTempAuthKey never moves expiry past what the exchange agreed on.
func (s *Store) BindTempAuthKey(ctx context.Context, temp, perm int64, expiresAt time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE auth_keys SET
		perm_id = CASE WHEN id=$1 THEN $2 ELSE perm_id END,
		expires_at = CASE WHEN id=$1 THEN LEAST(coalesce(expires_at, $3), $3) ELSE expires_at END,
		used = true
		WHERE id IN ($1, $2)`, temp, perm, expiresAt)
	return err
}

func (s *Store) SetAuthKeyLayer(ctx context.Context, id int64, layer int) error {
	_, err := s.Pool.Exec(ctx, "UPDATE auth_keys SET layer=$2 WHERE id=$1 AND layer<>$2", id, layer)
	return err
}

func (s *Store) MarkAuthKeyUsed(ctx context.Context, id int64) error {
	_, err := s.Pool.Exec(ctx, "UPDATE auth_keys SET used=true WHERE id=$1 AND NOT used", id)
	return err
}

// PruneAuthKeys deletes expired temp keys and keys from exchanges that
// never carried a call (signed-in keys always did), except those in keep.
func (s *Store) PruneAuthKeys(ctx context.Context, now, unusedBefore time.Time, keep []int64) (int64, error) {
	if keep == nil {
		keep = []int64{}
	}
	tag, err := s.Pool.Exec(ctx, `DELETE FROM auth_keys WHERE id <> ALL($3) AND
		(expires_at <= $1 OR (NOT used AND session_hash IS NULL AND created_at < $2))`, now, unusedBefore, keep)
	return tag.RowsAffected(), err
}

func (s *Store) DeleteAuthKey(ctx context.Context, id int64) error {
	_, err := s.Pool.Exec(ctx, "DELETE FROM auth_keys WHERE id=$1", id)
	return err
}
