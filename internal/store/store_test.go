package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/crypto"
)

func TestLoadMigrations(t *testing.T) {
	list, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 || list[0].version != 1 {
		t.Fatalf("expected migrations starting at 1, got %+v", list)
	}
}

// Needs a real Postgres: GREENGRAM_TEST_DATABASE_URL=postgres://... (CI sets it).
// Every run gets its own schema, dropped at the end.
func TestMigrate(t *testing.T) {
	url := os.Getenv("GREENGRAM_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("GREENGRAM_TEST_DATABASE_URL not set in CI")
		}
		t.Skip("GREENGRAM_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	if _, err := admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")

	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	s, err := Open(ctx, url+sep+"search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 2; i++ { // second run must be a no-op
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	list, _ := loadMigrations()
	var n int
	if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(list) {
		t.Fatalf("schema_migrations has %d rows, want %d", n, len(list))
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO instance_settings(key, value) VALUES('name', '"Greengram"')`); err != nil {
		t.Fatal(err)
	}
	testAuthKeys(t, ctx, s)
}

func testAuthKeys(t *testing.T, ctx context.Context, s *Store) {
	var perm, temp crypto.Key
	perm[0], temp[0] = 1, 2
	pk, tk := perm.WithID(), temp.WithID()
	tempUntil := time.Now().Add(time.Hour).Truncate(time.Second)
	for _, k := range []crypto.AuthKey{pk, tk, pk} { // second put of pk is a no-op
		var exp time.Time
		if k == tk {
			exp = tempUntil
		}
		if err := s.PutAuthKey(ctx, k, 42, exp, nil); err != nil {
			t.Fatal(err)
		}
	}
	// a bind can't push expiry past what the exchange agreed on
	if err := s.BindTempAuthKey(ctx, KeyID(tk.ID), KeyID(pk.ID), tempUntil.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAuthKeyLayer(ctx, KeyID(tk.ID), 231); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.AuthKey(ctx, KeyID(tk.ID))
	if err != nil || !ok {
		t.Fatalf("temp key: ok=%v err=%v", ok, err)
	}
	if got.Key != tk || got.PermID != KeyID(pk.ID) || got.Layer != 231 || got.HandshakeSalt != 42 || got.CreatedAt.IsZero() ||
		!got.ExpiresAt.Equal(tempUntil) || !got.Used {
		t.Fatalf("temp key round trip: %+v", got)
	}
	if p, _, _ := s.AuthKey(ctx, KeyID(pk.ID)); !p.Used || !p.ExpiresAt.IsZero() {
		t.Fatalf("bind must mark the perm key used and leave it permanent: %+v", p)
	}
	testPruneAuthKeys(t, ctx, s)
	// deleting the perm key takes its bound temp keys with it
	if err := s.DeleteAuthKey(ctx, KeyID(pk.ID)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.AuthKey(ctx, KeyID(tk.ID)); ok {
		t.Fatal("bound temp key survived its perm key")
	}
}

func testPruneAuthKeys(t *testing.T, ctx context.Context, s *Store) {
	now := time.Now()
	put := func(b byte, exp time.Time) int64 {
		var k crypto.Key
		k[0], k[1] = b, 0xee
		ak := k.WithID()
		if err := s.PutAuthKey(ctx, ak, 0, exp, nil); err != nil {
			t.Fatal(err)
		}
		return KeyID(ak.ID)
	}
	expired, live, unused, used, kept := put(1, now.Add(-time.Minute)), put(2, now.Add(time.Hour)), put(3, time.Time{}), put(4, time.Time{}), put(5, now.Add(-time.Minute))
	for _, id := range []int64{used, live} {
		if err := s.MarkAuthKeyUsed(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	// unusedBefore ahead of now, so the fresh unused key counts as stale
	n, err := s.PruneAuthKeys(ctx, now, now.Add(time.Hour), []int64{kept})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("pruned %d keys, want the expired and the unused one", n)
	}
	for id, want := range map[int64]bool{expired: false, live: true, unused: false, used: true, kept: true} {
		if _, ok, _ := s.AuthKey(ctx, id); ok != want {
			t.Errorf("key %d present=%v, want %v", id, ok, want)
		}
	}
}
