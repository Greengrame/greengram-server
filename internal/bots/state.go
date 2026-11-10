package bots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/iamxvbaba/td/tg"
	"github.com/jackc/pgx/v5"
)

// flexBool is a switch in bots.state: greengram-web stored true or 0.
type flexBool bool

func (f *flexBool) UnmarshalJSON(raw []byte) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case bool:
		*f = flexBool(x)
	case float64:
		*f = x != 0
	}
	return nil
}

// State is what BotFather's mini app keeps in bots.state, in greengram-web's
// shape. Only settings something acts on go here.
type State struct {
	Settings struct {
		Topics flexBool  `json:"topics"`
		Groups *flexBool `json:"grps"`
		// nil is on: migration 0340 wrote false for the bots that predate it
		Privacy       *flexBool `json:"priv"`
		BotToBot      flexBool  `json:"loop"`
		Inline        flexBool  `json:"inline"`
		Placeholder   string    `json:"inph"`
		Feedback      string    `json:"infdb"`
		SameOrigin    flexBool  `json:"sor"`
		Splash        Splash    `json:"splashscreen"`
		Access        flexBool  `json:"access"`
		AccessUsers   []int64   `json:"access_users"`
		PrivacyURL    string    `json:"privacy_policy_url"`
		GroupRights   []string  `json:"group_admin_rights"`
		ChannelRights []string  `json:"broadcast_admin_rights"`
	} `json:"settings"`
	WelcomePhoto int64 `json:"welcomePhotoId"`
	MainApp      struct {
		Mode string `json:"mode"`
	} `json:"mainApp"`
}

// a bad old value must not take the bot down with it
func parseState(raw []byte) State {
	var st State
	if json.Unmarshal(raw, &st) != nil {
		return State{}
	}
	return st
}

// AllowGroups is on unless the owner turned it off.
func (st State) AllowGroups() bool {
	return st.Settings.Groups == nil || bool(*st.Settings.Groups)
}

// GroupPrivacy is on unless the owner turned it off: the bot only gets group
// messages meant for it. The rule itself is in Postgres (bot_gets_group_message).
func (st State) GroupPrivacy() bool {
	return st.Settings.Privacy == nil || bool(*st.Settings.Privacy)
}

// InlinePlaceholder is what the composer shows after @bot. Web A takes an empty
// one to mean no inline mode, so a bot with the switch on always has something.
func (st State) InlinePlaceholder() string {
	if st.Settings.Placeholder != "" {
		return st.Settings.Placeholder
	}
	return "Search..."
}

// FeedbackEvery is the inline feedback rate: 1 in n sent results reaches the bot
// as chosen_inline_result. 0 is never. The page's values are the original's.
func (st State) FeedbackEvery() int {
	switch st.Settings.Feedback {
	case "1", "10", "100", "1000":
		n, _ := strconv.Atoi(st.Settings.Feedback)
		return n
	}
	return 0
}

// Mini app launch modes.
const (
	ModeCompact    = "compact"
	ModeFullsize   = "fullsize"
	ModeFullscreen = "fullscreen"
)

// MainMode is the main app's launch mode; apps saved before modes open fullsize.
func (b Bot) MainMode() string {
	if b.State.MainApp.Mode == "" {
		return ModeFullsize
	}
	return b.State.MainApp.Mode
}

// AdminRight names, as the BotFather page posts them. Order is the page's.
var (
	GroupRightNames   = []string{"change_info", "delete_messages", "ban_users", "invite_users", "pin_messages", "add_admins", "manage_call", "anonymous", "manage_topics", "post_stories", "edit_stories", "delete_stories", "other"}
	ChannelRightNames = []string{"change_info", "post_messages", "edit_messages", "delete_messages", "ban_users", "add_admins", "invite_users", "manage_call", "manage_direct_messages", "post_stories", "edit_stories", "delete_stories", "other"}
)

// AdminRights turns the page's names into what bot_group_admin_rights carries.
func AdminRights(names []string) tg.ChatAdminRights {
	var r tg.ChatAdminRights
	for _, n := range names {
		switch n {
		case "change_info":
			r.ChangeInfo = true
		case "post_messages":
			r.PostMessages = true
		case "edit_messages":
			r.EditMessages = true
		case "delete_messages":
			r.DeleteMessages = true
		case "ban_users":
			r.BanUsers = true
		case "invite_users":
			r.InviteUsers = true
		case "pin_messages":
			r.PinMessages = true
		case "add_admins":
			r.AddAdmins = true
		case "manage_call":
			r.ManageCall = true
		case "anonymous":
			r.Anonymous = true
		case "manage_topics":
			r.ManageTopics = true
		case "manage_direct_messages":
			r.ManageDirectMessages = true
		case "post_stories":
			r.PostStories = true
		case "edit_stories":
			r.EditStories = true
		case "delete_stories":
			r.DeleteStories = true
		case "other":
			r.Other = true
		}
	}
	return r
}

// SetSetting writes one settings key; a nil value removes it.
func (s *Service) SetSetting(ctx context.Context, bot int64, key string, value any) error {
	if value == nil {
		return s.bump(ctx, bot, "state = state #- array['settings', $2::text]", key)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.bump(ctx, bot, `state = jsonb_set(jsonb_set(state, '{settings}', coalesce(state->'settings', '{}')), array['settings', $2::text], $3::jsonb)`, key, string(raw))
}

// SetWelcomePhoto is the picture above "What can this bot do?"; 0 removes it.
func (s *Service) SetWelcomePhoto(ctx context.Context, bot, photo int64) error {
	if photo == 0 {
		return s.bump(ctx, bot, "state = state - 'welcomePhotoId'")
	}
	return s.bump(ctx, bot, "state = jsonb_set(state, '{welcomePhotoId}', to_jsonb($2::bigint))", photo)
}

// SetMainAppMode is how the main app opens.
func (s *Service) SetMainAppMode(ctx context.Context, bot int64, mode string) error {
	return s.bump(ctx, bot, `state = jsonb_set(jsonb_set(state, '{mainApp}', coalesce(state->'mainApp', '{}')), '{mainApp,mode}', to_jsonb($2::text))`, mode)
}

func tokenContext(bot int64) string { return fmt.Sprintf("bot-token:%d", bot) }

// seal is what goes in token_sealed: NULL with no key or if encrypting failed,
// which only costs the owner seeing the token again.
func (s *Service) seal(bot int64, token string) *string {
	if s.TokenKey == nil {
		return nil
	}
	v, err := s.TokenKey.EncryptPhone(token, tokenContext(bot))
	if err != nil {
		return nil
	}
	return &v
}

// Token reads the bot's current token back, for its owner's bot page. false:
// it was made before tokens were kept, the owner has to revoke it for a new one.
func (s *Service) Token(ctx context.Context, bot int64) (string, bool, error) {
	if s.TokenKey == nil {
		return "", false, nil
	}
	var sealed *string
	err := s.db.QueryRow(ctx, "SELECT token_sealed FROM bots WHERE user_id=$1", bot).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && sealed == nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	token, err := s.TokenKey.DecryptPhone(*sealed, tokenContext(bot))
	if err != nil {
		// the secret file was replaced; same as an old token
		return "", false, nil
	}
	return token, token != "", nil
}
