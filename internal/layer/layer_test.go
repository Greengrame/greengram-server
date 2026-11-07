package layer

import "testing"

func TestResolve(t *testing.T) {
	for n := Oldest; n <= Newest; n++ {
		if p, ok := Resolve(n); !ok || int(p) != n {
			t.Fatalf("layer %d: got %d %v", n, p, ok)
		}
	}
	if p, ok := Resolve(Newest + 5); !ok || int(p) != Newest {
		t.Fatalf("newer layer: got %d %v", p, ok)
	}
	if _, ok := Resolve(Oldest - 1); ok {
		t.Fatal("layer below the oldest profile was accepted")
	}
}
