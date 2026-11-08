package api

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/edge"
)

func init() {
	for _, m := range []tlprofile.SemanticID{
		tlprofile.SemanticMethodAccountGetContentSettings,
		tlprofile.SemanticMethodAccountGetWebAuthorizations,
		tlprofile.SemanticMethodAccountGetCollectibleEmojiStatuses,
		tlprofile.SemanticMethodAccountCheckUsername,
		tlprofile.SemanticMethodAccountUnregisterDevice,
	} {
		frozenOK[m] = true
	}
}

// registerProfile is the account's own settings: profile, username,
// birthday and the switches in Privacy and Security.
func (a *API) registerProfile() {
	handle(a, tlprofile.SemanticMethodAccountUpdateProfile, a.updateProfile)
	handle(a, tlprofile.SemanticMethodAccountCheckUsername, func(ctx context.Context, r *tg.AccountCheckUsernameRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		free, err := a.accounts.CheckUsername(ctx, me.ID, r.Username)
		return free, privacyErr(err)
	})
	handle(a, tlprofile.SemanticMethodAccountUpdateUsername, func(ctx context.Context, r *tg.AccountUpdateUsernameRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		u, err := a.accounts.SetUsername(ctx, me, r.Username)
		if err != nil {
			return nil, privacyErr(err)
		}
		return a.profileChanged(ctx, u)
	})
	handle(a, tlprofile.SemanticMethodAccountToggleUsername, func(ctx context.Context, r *tg.AccountToggleUsernameRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		if err := a.accounts.ToggleUsername(ctx, me, r.Username, r.Active); err != nil {
			return nil, privacyErr(err)
		}
		a.UsernamesChanged(me.ID)
		return true, nil
	})
	handle(a, tlprofile.SemanticMethodAccountReorderUsernames, func(ctx context.Context, r *tg.AccountReorderUsernamesRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		if err := a.accounts.ReorderUsernames(ctx, me, r.Order); err != nil {
			return nil, privacyErr(err)
		}
		a.UsernamesChanged(me.ID)
		return true, nil
	})
	handle(a, tlprofile.SemanticMethodAccountUpdateBirthday, func(ctx context.Context, r *tg.AccountUpdateBirthdayRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		var b account.Birthday
		if v, ok := r.GetBirthday(); ok {
			b = account.Birthday{Day: v.Day, Month: v.Month}
			b.Year, _ = v.GetYear()
		}
		if err := a.accounts.SetBirthday(ctx, me.ID, b); err != nil {
			return nil, privacyErr(err)
		}
		// clients learn it from getFullUser; poke the other sessions
		a.echo(ctx, me, []tg.UpdateClass{&tg.UpdateUser{UserID: tgUserID(me)}}, []tg.UserClass{a.selfUser(me)})
		return true, nil
	})

	handle(a, tlprofile.SemanticMethodAccountGetGlobalPrivacySettings, func(ctx context.Context, _ *tg.AccountGetGlobalPrivacySettingsRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		p, err := a.accounts.Prefs(ctx, me.ID)
		if err != nil {
			return nil, err
		}
		return globalPrivacy(p), nil
	})
	handle(a, tlprofile.SemanticMethodAccountSetGlobalPrivacySettings, func(ctx context.Context, r *tg.AccountSetGlobalPrivacySettingsRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		s := r.Settings
		p := account.Prefs{
			ArchiveNewPeers:     s.ArchiveAndMuteNewNoncontactPeers,
			KeepArchivedUnmuted: s.KeepArchivedUnmuted,
			KeepArchivedFolders: s.KeepArchivedFolders,
			HideReadMarks:       s.HideReadMarks,
			// paid messages and premium-only chats need the stars and
			// premium checks in sendMessage first; until then they stay off
			DisplayGiftsButton: s.DisplayGiftsButton,
		}
		if err := a.accounts.SetGlobalPrivacy(ctx, me.ID, p); err != nil {
			return nil, privacyErr(err)
		}
		return globalPrivacy(p), nil
	})
	handle(a, tlprofile.SemanticMethodAccountGetContentSettings, func(ctx context.Context, _ *tg.AccountGetContentSettingsRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		p, err := a.accounts.Prefs(ctx, me.ID)
		if err != nil {
			return nil, err
		}
		return &tg.AccountContentSettings{SensitiveEnabled: p.SensitiveEnabled, SensitiveCanChange: true}, nil
	})
	handle(a, tlprofile.SemanticMethodAccountSetContentSettings, func(ctx context.Context, r *tg.AccountSetContentSettingsRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		return true, a.accounts.SetSensitive(ctx, me.ID, r.SensitiveEnabled)
	})
	handle(a, tlprofile.SemanticMethodAccountGetAccountTTL, func(ctx context.Context, _ *tg.AccountGetAccountTTLRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		p, err := a.accounts.Prefs(ctx, me.ID)
		if err != nil {
			return nil, err
		}
		return &tg.AccountDaysTTL{Days: p.AccountTTLDays}, nil
	})
	handle(a, tlprofile.SemanticMethodAccountSetAccountTTL, func(ctx context.Context, r *tg.AccountSetAccountTTLRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		return true, privacyErr(a.accounts.SetAccountTTL(ctx, me.ID, r.TTL.Days))
	})
	handle(a, tlprofile.SemanticMethodAccountSetAuthorizationTTL, func(ctx context.Context, r *tg.AccountSetAuthorizationTTLRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		return true, privacyErr(a.accounts.SetAuthorizationTTL(ctx, me.ID, r.AuthorizationTTLDays))
	})
	handle(a, tlprofile.SemanticMethodAccountChangeAuthorizationSettings, func(ctx context.Context, r *tg.AccountChangeAuthorizationSettingsRequest) (any, error) {
		me, current, err := a.session(ctx)
		if err != nil {
			return nil, err
		}
		hash := current
		if r.Hash != 0 {
			hash = ""
			list, err := a.accounts.Sessions(ctx, me.ID)
			if err != nil {
				return nil, err
			}
			for _, s := range list {
				if authHash(s.Hash) == r.Hash {
					hash = s.Hash
				}
			}
			if hash == "" {
				return nil, tgerr.New(400, "HASH_INVALID")
			}
		}
		var calls, secret *bool
		if v, ok := r.GetCallRequestsDisabled(); ok {
			calls = &v
		}
		if v, ok := r.GetEncryptedRequestsDisabled(); ok {
			secret = &v
		}
		return true, a.accounts.SetAuthorizationSettings(ctx, me.ID, hash, calls, secret)
	})
	handle(a, tlprofile.SemanticMethodAccountGetContactSignUpNotification, func(ctx context.Context, _ *tg.AccountGetContactSignUpNotificationRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		p, err := a.accounts.Prefs(ctx, me.ID)
		if err != nil {
			return nil, err
		}
		// Telegram answers with the "silent" flag (tdesktop and Android
		// read it so; Web A reads it the other way round)
		return p.ContactSignupSilent, nil
	})
	handle(a, tlprofile.SemanticMethodAccountSetContactSignUpNotification, func(ctx context.Context, r *tg.AccountSetContactSignUpNotificationRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		return true, a.accounts.SetContactSignupSilent(ctx, me.ID, r.Silent)
	})

	// nobody logs in to other sites with a Greengram account
	handle(a, tlprofile.SemanticMethodAccountGetWebAuthorizations, func(context.Context, *tg.AccountGetWebAuthorizationsRequest) (any, error) {
		return &tg.AccountWebAuthorizations{Authorizations: []tg.WebAuthorization{}, Users: []tg.UserClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodAccountResetWebAuthorization, func(context.Context, *tg.AccountResetWebAuthorizationRequest) (any, error) {
		return nil, tgerr.New(400, "HASH_INVALID")
	})
	handle(a, tlprofile.SemanticMethodAccountResetWebAuthorizations, func(context.Context, *tg.AccountResetWebAuthorizationsRequest) (any, error) {
		return true, nil
	})
	handle(a, tlprofile.SemanticMethodAccountUnregisterDevice, func(context.Context, *tg.AccountUnregisterDeviceRequest) (any, error) {
		return true, nil
	})
	handle(a, tlprofile.SemanticMethodAccountGetCollectibleEmojiStatuses, a.getCollectibleEmojiStatuses)
}

func globalPrivacy(p account.Prefs) *tg.GlobalPrivacySettings {
	out := &tg.GlobalPrivacySettings{
		ArchiveAndMuteNewNoncontactPeers: p.ArchiveNewPeers,
		KeepArchivedUnmuted:              p.KeepArchivedUnmuted,
		KeepArchivedFolders:              p.KeepArchivedFolders,
		HideReadMarks:                    p.HideReadMarks,
		NewNoncontactPeersRequirePremium: p.NewPeersNeedPremium,
		DisplayGiftsButton:               p.DisplayGiftsButton,
	}
	if p.NoncontactPaidStars > 0 {
		out.SetNoncontactPeersPaidStars(p.NoncontactPaidStars)
	}
	return out
}

func (a *API) updateProfile(ctx context.Context, r *tg.AccountUpdateProfileRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	var first, last, about *string
	if v, ok := r.GetFirstName(); ok {
		first = &v
	}
	if v, ok := r.GetLastName(); ok {
		last = &v
	}
	if v, ok := r.GetAbout(); ok {
		about = &v
	}
	u, err := a.accounts.UpdateProfile(ctx, me, first, last, about)
	if err != nil {
		return nil, privacyErr(err)
	}
	return a.profileChanged(ctx, u)
}

// selfUser is u as its own sessions see it.
func (a *API) selfUser(u account.User) *tg.User {
	t := tgUser(u, true)
	t.Status = a.presence.status(u.ID)
	if u.PhotoID != 0 {
		t.Photo = &tg.UserProfilePhoto{PhotoID: u.PhotoID, DCID: a.in.DC}
	}
	return t
}

// profileChanged answers a profile edit with the new self user, sends
// updateUserName to the user's other sessions, and tells the people who
// have a chat with them (or them as a contact) to look again.
func (a *API) profileChanged(ctx context.Context, u account.User) (*tg.User, error) {
	first, last := splitName(u.Name)
	name := &tg.UpdateUserName{UserID: tgUserID(u), FirstName: first, LastName: last, Usernames: []tg.Username{}}
	if u.UsernamePublic || len(u.Aliases) > 0 {
		name.Usernames = usernames(u, true)
	}
	a.echo(ctx, u, []tg.UpdateClass{name}, []tg.UserClass{a.selfUser(u)})
	go a.tellWatchers(u.ID, func(context.Context, int64) (tg.UpdateClass, bool) {
		return &tg.UpdateUser{UserID: tgUserID(u)}, true
	})
	return a.selfUser(u), nil
}

// UsernamesChanged tells every session of user, the caller's too (Web A waits
// for the update after reordering), and whoever watches them.
func (a *API) UsernamesChanged(user int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	u, ok, err := a.accounts.User(ctx, user)
	if err != nil || !ok {
		slog.Warn("api: usernames changed", "user", user, "err", err)
		return
	}
	if _, err := a.profileChanged(ctx, u); err != nil {
		slog.Warn("api: usernames changed", "user", user, "err", err)
	}
}

// IdentityReset follows the admin panel's Reset Info: the account's own
// apps get the new name, usernames and phone, and everyone online who has a
// chat with it or keeps it as a contact gets the user again as they may see
// it, so no app keeps showing the old number, name or photo.
func (a *API) IdentityReset(user int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	u, ok, err := a.accounts.User(ctx, user)
	if err != nil || !ok {
		slog.Warn("api: identity reset", "user", user, "err", err)
		return
	}
	first, last := splitName(u.Name)
	name := &tg.UpdateUserName{UserID: tgUserID(u), FirstName: first, LastName: last, Usernames: []tg.Username{}}
	if u.UsernamePublic || len(u.Aliases) > 0 {
		name.Usernames = usernames(u, true)
	}
	phone := strings.TrimPrefix(u.Phone, "+")
	a.echo(ctx, u, []tg.UpdateClass{name, &tg.UpdateUserPhone{UserID: tgUserID(u), Phone: phone}, &tg.UpdateUser{UserID: tgUserID(u)}},
		[]tg.UserClass{a.selfUser(u)})
	go a.tellWatchers(u.ID, func(ctx context.Context, viewer int64) (tg.UpdateClass, bool) {
		if a.canSee(ctx, u.ID, viewer, "phoneNumber") {
			return &tg.UpdateUserPhone{UserID: tgUserID(u), Phone: phone}, true
		}
		return &tg.UpdateUser{UserID: tgUserID(u)}, true
	})
}

// ChannelUsernameReleased tells the channel's members a Fragment sale took
// its public username.
func (a *API) ChannelUsernameReleased(channel int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	events, err := a.chans.UsernameChanged(ctx, channel)
	if err != nil {
		slog.Warn("api: channel released", "channel", channel, "err", err)
		return
	}
	a.deliverChannel(events, edge.Session{})
}

// tellWatchers pushes an update about user to everyone online who has a
// private chat with them or keeps them as a contact, with the user object
// as each of them sees it. build makes a fresh update per viewer (td fills
// flags while encoding, so pushes never share one) or says to skip them.
func (a *API) tellWatchers(user int64, build func(ctx context.Context, viewer int64) (tg.UpdateClass, bool)) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	who, err := a.msgs.Watchers(ctx, user)
	if err != nil {
		slog.Warn("api: watchers", "err", err)
		return
	}
	for _, viewer := range who {
		if viewer == user || !a.hub.online(viewer) {
			continue
		}
		a.out.add(viewer, func(ctx context.Context) {
			upd, ok := build(ctx, viewer)
			if !ok {
				return
			}
			users, err := a.usersFor(ctx, viewer, []int64{user})
			if err != nil {
				slog.Warn("api: watcher user", "err", err)
				return
			}
			a.push(ctx, viewer, &tg.Updates{Updates: []tg.UpdateClass{upd}, Users: users, Chats: []tg.ChatClass{}, Date: now()}, edge.Session{})
		})
	}
}
