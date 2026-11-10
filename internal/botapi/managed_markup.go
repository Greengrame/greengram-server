package botapi

import (
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/bots"
)

type apiManagedBotButton struct {
	ID       int    `json:"request_id"`
	Name     string `json:"suggested_name,omitempty"`
	Username string `json:"suggested_username,omitempty"`
}

func managedButtonType(button *apiManagedBotButton) tg.ButtonTypeClass {
	return &tg.ButtonTypeRequestPeer{ButtonID: button.ID, MaxQuantity: 1, PeerType: &tg.RequestPeerTypeCreateBot{BotManaged: true, SuggestedName: button.Name, SuggestedUsername: button.Username}}
}

func validateManagedMarkup(markup apiMarkup) error {
	var row []apiButton
	var button apiButton
	var seen = map[int]bool{}
	var count int
	for _, row = range markup.Keyboard {
		for _, button = range row {
			count++
			if count > 100 {
				return bad("too many keyboard buttons")
			}
			if button.Managed == nil {
				continue
			}
			if button.Managed.ID < -2147483648 || button.Managed.ID > 2147483647 || seen[button.Managed.ID] {
				return bad("invalid or duplicate managed bot request ID")
			}
			seen[button.Managed.ID] = true
			if utf8.RuneCountInString(button.Managed.Name) > 64 {
				return bad("suggested bot name is too long")
			}
			if button.Managed.Username != "" && !strings.HasSuffix(strings.ToLower(button.Managed.Username), "bot") && !bots.IsSecondaryUsername(button.Managed.Username) {
				return bad("invalid suggested bot username")
			}
		}
	}
	return nil
}
