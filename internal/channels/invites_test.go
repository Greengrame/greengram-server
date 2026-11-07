package channels

import (
	"testing"

	"github.com/Greengrame/greengram-server/internal/ids"
)

// admin_id is a client id: an account sitting on a reserved id is shifted
func TestInviteAdminID(t *testing.T) {
	for _, id := range []int64{42, 777000} {
		if got := (Invite{Hash: "x", Creator: id}).TL("https://gg.test").AdminID; got != ids.Client(id) {
			t.Errorf("creator %d: admin_id %d, want %d", id, got, ids.Client(id))
		}
	}
}
