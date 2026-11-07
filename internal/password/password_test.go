package password

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// testdata/web.json holds hashes made by Bun.password on the web server.
func TestVerifiesBunHashes(t *testing.T) {
	b, err := os.ReadFile("testdata/web.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct{ Password, Argon, ArgonDefault string }
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{f.Argon, f.ArgonDefault} {
		if !Verify(f.Password, h) {
			t.Fatalf("Bun hash did not verify: %s", h)
		}
		if Verify(f.Password+"x", h) {
			t.Fatal("wrong password verified")
		}
	}
}

func TestHash(t *testing.T) {
	h, err := Hash("hunter2hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") || !Verify("hunter2hunter2", h) || Verify("nope", h) {
		t.Fatalf("bad hash %s", h)
	}
	if Check("anything", "") || Verify("x", "$argon2id$garbage") {
		t.Fatal("missing or broken hash verified")
	}
}
