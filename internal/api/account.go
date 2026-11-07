package api

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
)

func (a *API) registerAccount() {
	handle(a, tlprofile.SemanticMethodAccountGetAuthorizations, a.getAuthorizations)
	// sign-in is the site's /login, so nobody can have one
	handle(a, tlprofile.SemanticMethodAccountGetPasskeys, func(context.Context, *tg.AccountGetPasskeysRequest) (any, error) {
		return &tg.AccountPasskeys{Passkeys: []tg.Passkey{}}, nil
	})
	handle(a, tlprofile.SemanticMethodAccountResetAuthorization, func(ctx context.Context, r *tg.AccountResetAuthorizationRequest) (any, error) {
		me, current, err := a.session(ctx)
		if err != nil {
			return nil, err
		}
		list, err := a.accounts.Sessions(ctx, me.ID)
		if err != nil {
			return nil, err
		}
		for _, s := range list {
			if s.Hash != current && authHash(s.Hash) == r.Hash {
				_, err := a.accounts.EndSession(ctx, me.ID, s.Hash)
				return err == nil, err
			}
		}
		return nil, tgerr.New(400, "HASH_INVALID")
	})
	handle(a, tlprofile.SemanticMethodAccountGetPrivacy, func(ctx context.Context, r *tg.AccountGetPrivacyRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		key, ok := inputPrivacyKey(r.Key)
		if !ok {
			return nil, tgerr.New(400, "PRIVACY_KEY_INVALID")
		}
		p, err := a.accounts.Privacy(ctx, me.ID, key)
		if err != nil {
			return nil, privacyErr(err)
		}
		return a.privacyReply(ctx, me, p)
	})
	handle(a, tlprofile.SemanticMethodAccountSetPrivacy, func(ctx context.Context, r *tg.AccountSetPrivacyRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		key, ok := inputPrivacyKey(r.Key)
		if !ok {
			return nil, tgerr.New(400, "PRIVACY_KEY_INVALID")
		}
		p := fromInputRules(key, r.Rules)
		if err := a.accounts.SetPrivacy(ctx, me.ID, key, p); err != nil {
			return nil, privacyErr(err)
		}
		a.echo(ctx, me, []tg.UpdateClass{&tg.UpdatePrivacy{Key: privacyKeyTL(key), Rules: privacyRules(p)}}, []tg.UserClass{})
		return a.privacyReply(ctx, me, p)
	})
}

// privacyReply is the rules plus the people and chats named in them, so
// the exceptions lists show names.
func (a *API) privacyReply(ctx context.Context, me account.User, p account.Privacy) (*tg.AccountPrivacyRules, error) {
	out := &tg.AccountPrivacyRules{Rules: privacyRules(p), Users: []tg.UserClass{}, Chats: []tg.ChatClass{}}
	var err error
	if out.Users, err = a.usersFor(ctx, me.ID, append(chatIDs(p.AllowUserIDs), chatIDs(p.BlockUserIDs)...)); err != nil {
		return nil, err
	}
	out.Chats, err = a.chatsFor(ctx, me.ID, append(chatIDs(p.AllowChatIDs), chatIDs(p.BlockChatIDs)...))
	return out, err
}

// privacyKeyTL is inputPrivacyKey the other way, for updatePrivacy.
func privacyKeyTL(key string) tg.PrivacyKeyClass {
	switch key {
	case "lastSeen":
		return &tg.PrivacyKeyStatusTimestamp{}
	case "chatInvite":
		return &tg.PrivacyKeyChatInvite{}
	case "phoneCall":
		return &tg.PrivacyKeyPhoneCall{}
	case "phoneP2P":
		return &tg.PrivacyKeyPhoneP2P{}
	case "forwards":
		return &tg.PrivacyKeyForwards{}
	case "profilePhoto":
		return &tg.PrivacyKeyProfilePhoto{}
	case "phoneNumber":
		return &tg.PrivacyKeyPhoneNumber{}
	case "addByPhone":
		return &tg.PrivacyKeyAddedByPhone{}
	case "voiceMessages":
		return &tg.PrivacyKeyVoiceMessages{}
	case "bio":
		return &tg.PrivacyKeyAbout{}
	case "birthday":
		return &tg.PrivacyKeyBirthday{}
	case "gifts":
		return &tg.PrivacyKeyStarGiftsAutoSave{}
	case "noPaidMessages":
		return &tg.PrivacyKeyNoPaidMessages{}
	case "savedMusic":
		return &tg.PrivacyKeySavedMusic{}
	}
	return nil
}

func privacyErr(err error) error {
	var e *account.Error
	if errors.As(err, &e) {
		return tgerr.New(400, e.Message)
	}
	return err
}

// authHash is what the client uses to name a session; the current one is 0.
func authHash(tokenHash string) int64 {
	b, _ := hex.DecodeString(tokenHash[:16])
	var v int64
	for _, c := range b {
		v = v<<8 | int64(c)
	}
	if v < 0 {
		v = -v
	}
	if v == 0 {
		v = 1
	}
	return v
}

func (a *API) getAuthorizations(ctx context.Context, _ *tg.AccountGetAuthorizationsRequest) (any, error) {
	me, current, err := a.session(ctx)
	if err != nil {
		return nil, err
	}
	list, err := a.accounts.Sessions(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	prefs, err := a.accounts.Prefs(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	out := &tg.AccountAuthorizations{AuthorizationTTLDays: prefs.AuthorizationTTLDays, Authorizations: []tg.Authorization{}}
	for _, s := range list {
		auth := tg.Authorization{
			Hash:                      authHash(s.Hash),
			DateCreated:               int(s.Created.Unix()),
			DateActive:                int(s.LastActive.Unix()),
			IP:                        s.IP,
			Country:                   "",
			Region:                    "",
			DeviceModel:               s.DeviceModel,
			SystemVersion:             s.SystemVersion,
			AppVersion:                s.AppVersion,
			APIID:                     s.APIID,
			AppName:                   "Greengram",
			Platform:                  "",
			CallRequestsDisabled:      s.CallsOff,
			EncryptedRequestsDisabled: s.SecretOff,
		}
		if s.DeviceModel == "" {
			// signed in on the website
			auth.DeviceModel, auth.AppName, auth.Platform = browserName(s.UserAgent), "Greengram Web", "Web"
		}
		if s.Hash == current {
			auth.Hash, auth.Current = 0, true
			out.Authorizations = append([]tg.Authorization{auth}, out.Authorizations...)
			continue
		}
		out.Authorizations = append(out.Authorizations, auth)
	}
	return out, nil
}

func browserName(ua string) string {
	switch {
	case ua == "":
		return "Browser"
	case strings.Contains(ua, "Firefox/"):
		return "Firefox"
	case strings.Contains(ua, "Edg/"):
		return "Edge"
	case strings.Contains(ua, "Chrome/"):
		return "Chrome"
	case strings.Contains(ua, "Safari/"):
		return "Safari"
	}
	if len(ua) > 40 {
		return ua[:40]
	}
	return ua
}

func inputPrivacyKey(k tg.InputPrivacyKeyClass) (string, bool) {
	switch k.(type) {
	case *tg.InputPrivacyKeyStatusTimestamp:
		return "lastSeen", true
	case *tg.InputPrivacyKeyChatInvite:
		return "chatInvite", true
	case *tg.InputPrivacyKeyPhoneCall:
		return "phoneCall", true
	case *tg.InputPrivacyKeyPhoneP2P:
		return "phoneP2P", true
	case *tg.InputPrivacyKeyForwards:
		return "forwards", true
	case *tg.InputPrivacyKeyProfilePhoto:
		return "profilePhoto", true
	case *tg.InputPrivacyKeyPhoneNumber:
		return "phoneNumber", true
	case *tg.InputPrivacyKeyAddedByPhone:
		return "addByPhone", true
	case *tg.InputPrivacyKeyVoiceMessages:
		return "voiceMessages", true
	case *tg.InputPrivacyKeyAbout:
		return "bio", true
	case *tg.InputPrivacyKeyBirthday:
		return "birthday", true
	case *tg.InputPrivacyKeyStarGiftsAutoSave:
		return "gifts", true
	case *tg.InputPrivacyKeyNoPaidMessages:
		return "noPaidMessages", true
	case *tg.InputPrivacyKeySavedMusic:
		return "savedMusic", true
	}
	return "", false
}

// privacyRules lists exceptions first and the general rule last, the order
// Telegram's clients expect.
func privacyRules(p account.Privacy) []tg.PrivacyRuleClass {
	var out []tg.PrivacyRuleClass
	if ids := userIDs(p.AllowUserIDs); len(ids) > 0 {
		out = append(out, &tg.PrivacyValueAllowUsers{Users: ids})
	}
	if ids := userIDs(p.BlockUserIDs); len(ids) > 0 {
		out = append(out, &tg.PrivacyValueDisallowUsers{Users: ids})
	}
	if ids := userIDs(p.AllowChatIDs); len(ids) > 0 {
		out = append(out, &tg.PrivacyValueAllowChatParticipants{Chats: ids})
	}
	if ids := userIDs(p.BlockChatIDs); len(ids) > 0 {
		out = append(out, &tg.PrivacyValueDisallowChatParticipants{Chats: ids})
	}
	if p.ShouldAllowPremium {
		out = append(out, &tg.PrivacyValueAllowPremium{})
	}
	switch p.BotsPrivacy {
	case "allow":
		out = append(out, &tg.PrivacyValueAllowBots{})
	case "disallow":
		out = append(out, &tg.PrivacyValueDisallowBots{})
	}
	switch p.Visibility {
	case "everybody":
		out = append(out, &tg.PrivacyValueAllowAll{})
	case "contacts":
		out = append(out, &tg.PrivacyValueAllowContacts{})
	default:
		out = append(out, &tg.PrivacyValueDisallowAll{})
	}
	return out
}

func userIDs(ids []string) []int64 {
	out := make([]int64, 0, len(ids))
	for _, s := range ids {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			out = append(out, clientID(v))
		}
	}
	return out
}

func fromInputRules(key string, rules []tg.InputPrivacyRuleClass) account.Privacy {
	p := account.DefaultPrivacy(key)
	p.Visibility = "nobody"
	add := func(list *[]string, ids []int64) {
		for _, id := range ids {
			*list = append(*list, strconv.FormatInt(serverID(id), 10))
		}
	}
	users := func(in []tg.InputUserClass) []int64 {
		var out []int64
		for _, u := range in {
			if v, ok := u.(*tg.InputUser); ok {
				out = append(out, v.UserID)
			}
		}
		return out
	}
	for _, r := range rules {
		switch v := r.(type) {
		case *tg.InputPrivacyValueAllowAll:
			p.Visibility = "everybody"
		case *tg.InputPrivacyValueAllowContacts:
			p.Visibility = "contacts"
		case *tg.InputPrivacyValueDisallowAll:
			p.Visibility = "nobody"
		case *tg.InputPrivacyValueAllowUsers:
			add(&p.AllowUserIDs, users(v.Users))
		case *tg.InputPrivacyValueDisallowUsers:
			add(&p.BlockUserIDs, users(v.Users))
		case *tg.InputPrivacyValueAllowChatParticipants:
			add(&p.AllowChatIDs, v.Chats)
		case *tg.InputPrivacyValueDisallowChatParticipants:
			add(&p.BlockChatIDs, v.Chats)
		case *tg.InputPrivacyValueAllowPremium:
			p.ShouldAllowPremium = true
		case *tg.InputPrivacyValueAllowBots:
			p.BotsPrivacy = "allow"
		case *tg.InputPrivacyValueDisallowBots:
			p.BotsPrivacy = "disallow"
		}
	}
	return p
}
