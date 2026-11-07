package media

import (
	"encoding/json"
	"testing"
)

func TestPadLottie(t *testing.T) {
	src := `{"tgs":1,"v":"5.5.2","fr":60,"ip":0,"op":180,"w":512,"h":512,"assets":[{"id":"img","p":"x"}],"layers":[{"ty":4,"ind":3,"parent":6},{"ty":3,"ind":6}]}`
	out, err := padLottie([]byte(src), 0.5, 0.6)
	if err != nil {
		t.Fatal(err)
	}
	var anim struct {
		W, H, Op float64
		Tgs      int
		Assets   []struct {
			ID     string
			Layers []map[string]any
		}
		Layers []struct {
			Ty    int
			RefID string
			Ip    float64
			Op    float64
			Ks    struct {
				P, S struct{ K []float64 }
			}
		}
	}
	if err := json.Unmarshal(out, &anim); err != nil {
		t.Fatal(err)
	}
	if anim.W != 512 || anim.Op != 180 || anim.Tgs != 1 || len(anim.Assets) != 2 || len(anim.Assets[1].Layers) != 2 {
		t.Fatalf("frame or assets lost: %s", out)
	}
	l := anim.Layers
	if len(l) != 1 || l[0].Ty != 0 || l[0].RefID != anim.Assets[1].ID || l[0].Op != 180 {
		t.Fatalf("layer %+v", l)
	}
	if l[0].Ks.S.K[0] != 50 || l[0].Ks.P.K[0] != 256 || l[0].Ks.P.K[1] != 512*0.6 {
		t.Fatalf("placed %+v", l[0].Ks)
	}
	if _, err := padLottie([]byte(`{"w":0,"h":0,"ip":0,"op":1,"layers":[]}`), 0.5, 0.5); err == nil {
		t.Fatal("padded an empty frame")
	}
}
