package secret

import (
	"encoding/json"
	"os"
	"testing"
)

// testdata/web.json was made by greengram-web's own crypto code.
type fixture struct {
	Key, Phone, Context, Lookup, Encrypted string
}

func load(t *testing.T) fixture {
	t.Helper()
	b, err := os.ReadFile("testdata/web.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestMatchesWeb(t *testing.T) {
	f := load(t)
	k := FromBytes([]byte(f.Key))
	if got := k.PhoneLookup(f.Phone); got != f.Lookup {
		t.Fatalf("lookup %s, web made %s", got, f.Lookup)
	}
	if got := k.PhoneLookup("+1 (555) 0100"); got != f.Lookup {
		t.Fatal("lookup should ignore formatting")
	}
	got, err := k.DecryptPhone(f.Encrypted, f.Context)
	if err != nil || got != f.Phone {
		t.Fatalf("decrypt web value: %q %v", got, err)
	}
	if _, err := k.DecryptPhone(f.Encrypted, "user:43"); err == nil {
		t.Fatal("decrypted under the wrong context")
	}
	enc, err := k.EncryptPhone(f.Phone, "user:7")
	if err != nil {
		t.Fatal(err)
	}
	if back, err := k.DecryptPhone(enc, "user:7"); err != nil || back != f.Phone {
		t.Fatalf("round trip: %q %v", back, err)
	}
}
