package api

import (
	"testing"

	"github.com/Greengrame/greengram-server/internal/account"
)

func TestAudienceCan(t *testing.T) {
	rule := func(vis string, edit func(*account.Privacy)) map[string]account.Privacy {
		p := account.DefaultPrivacy("lastSeen")
		p.Visibility = vis
		if edit != nil {
			edit(&p)
		}
		return map[string]account.Privacy{"lastSeen": p}
	}
	au := &audience{
		viewer:     1,
		theyHaveMe: map[int64]bool{3: true, 7: true},
		blockedMe:  map[int64]bool{6: true},
		chats:      map[int64]bool{500: true},
		rules: map[int64]map[string]account.Privacy{
			2: rule("everybody", nil),
			3: rule("contacts", nil),
			4: rule("contacts", nil),
			5: rule("nobody", func(p *account.Privacy) { p.AllowChatIDs = []string{"500"} }),
			6: rule("everybody", nil),
			7: rule("everybody", func(p *account.Privacy) { p.BlockUserIDs = []string{"1"} }),
			8: rule("everybody", func(p *account.Privacy) { p.BlockChatIDs = []string{"500"} }),
		},
	}
	for owner, want := range map[int64]bool{1: true, 2: true, 3: true, 4: false, 5: true, 6: false, 7: false, 8: false, 9: false} {
		if got := au.can(owner, "lastSeen"); got != want {
			t.Errorf("owner %d: can=%v, want %v", owner, got, want)
		}
	}
}
