package api

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/ids"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/stars"
)

const serviceNotifications = ids.ServiceNotifications

func isAnnouncements(u account.User) bool {
	return u.IsSystem && (u.Username == "announcements" || u.Username == "instance_announcements")
}

func clientID(id int64) int64 { return ids.Client(id) }

func serverID(id int64) int64 { return ids.Server(id) }

func tgUserID(u account.User) int64 {
	if isAnnouncements(u) {
		return serviceNotifications
	}
	return clientID(u.ID)
}

// Access hashes only have to be stable per id; Greengram has no secrets
// behind them (visibility is checked on every call).
func accessHash(id int64) int64 {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(id))
	sum := sha256.Sum256(append([]byte("greengram-peer"), b[:]...))
	return int64(binary.LittleEndian.Uint64(sum[:8]) >> 1)
}

func splitName(name string) (string, string) {
	first, last, _ := strings.Cut(strings.TrimSpace(name), " ")
	return first, strings.TrimSpace(last)
}

func tgUser(u account.User, self bool) *tg.User {
	first, last := splitName(u.Name)
	id := tgUserID(u)
	out := &tg.User{
		ID:         id,
		AccessHash: accessHash(id),
		Self:       self,
		Bot:        u.IsBot,
		FirstName:  first,
		LastName:   last,
		Status:     &tg.UserStatusRecently{},
		Premium:    u.Premium(nowTime()), // public, like Telegram's star badge
	}
	if list := usernames(u, self); len(u.Aliases) > 0 && len(list) > 0 {
		out.Usernames = list
	} else if u.UsernamePublic || self {
		out.Username = u.Username
	}
	if doc, until, ok := u.Status(nowTime()); ok {
		st := &tg.EmojiStatus{DocumentID: doc}
		if until != 0 {
			st.SetUntil(int(until))
		}
		out.SetEmojiStatus(st)
	} else if c := u.Collectible; c != nil && u.Premium(nowTime()) && (u.EmojiUntil == 0 || u.EmojiUntil > nowTime().Unix()) {
		st := collectibleStatus(*c)
		if u.EmojiUntil != 0 {
			st.SetUntil(int(u.EmojiUntil))
		}
		out.SetEmojiStatus(st)
	}
	if self {
		out.Phone = strings.TrimPrefix(u.Phone, "+")
	}
	return out
}

// usernames is the list a user object carries once there's more than the
// primary name: owned collectibles, the inactive ones for their owner only.
func usernames(u account.User, self bool) []tg.Username {
	var out []tg.Username
	if u.UsernamePublic || self {
		out = append(out, tg.Username{Username: u.Username, Active: u.UsernamePublic, Editable: true})
	}
	for _, al := range u.Aliases {
		if al.Active || self {
			out = append(out, tg.Username{Username: al.Name, Active: al.Active})
		}
	}
	return out
}

func isSelf(in tg.InputUserClass, u account.User) bool {
	switch v := in.(type) {
	case *tg.InputUserSelf:
		return true
	case *tg.InputUser:
		return v.UserID == tgUserID(u)
	}
	return false
}

func collectibleStatus(c account.CollectibleStatus) *tg.EmojiStatusCollectible {
	return &tg.EmojiStatusCollectible{CollectibleID: c.ID, DocumentID: c.Document, Title: c.Title, Slug: c.Slug, PatternDocumentID: c.Pattern,
		CenterColor: c.Center, EdgeColor: c.Edge, PatternColor: c.PatternColor, TextColor: c.Text}
}

// updateEmojiStatus sets the custom emoji next to the name, or a collectible
// gift the user holds. Like Telegram it is a Premium feature.
func (a *API) updateEmojiStatus(ctx context.Context, r *tg.AccountUpdateEmojiStatusRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	var doc, until int64
	switch st := r.EmojiStatus.(type) {
	case *tg.EmojiStatusEmpty:
	case *tg.EmojiStatus:
		if !me.Premium(nowTime()) {
			return nil, tgerr.New(400, "PREMIUM_ACCOUNT_REQUIRED")
		}
		ok, err := a.media.IsStatusEmoji(ctx, st.DocumentID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, tgerr.New(400, "DOCUMENT_INVALID")
		}
		doc = st.DocumentID
		if u, ok := st.GetUntil(); ok {
			until = int64(u)
		}
	case *tg.InputEmojiStatusCollectible:
		if !me.Premium(nowTime()) {
			return nil, tgerr.New(400, "PREMIUM_ACCOUNT_REQUIRED")
		}
		if u, ok := st.GetUntil(); ok {
			until = int64(u)
		}
		ok, err := a.accounts.SetCollectibleStatus(ctx, me.ID, st.CollectibleID, until)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, tgerr.New(400, "COLLECTIBLE_INVALID")
		}
		a.pushUserChanged(me.ID)
		return true, nil
	default:
		return nil, tgerr.New(400, "EMOJI_STATUS_INVALID")
	}
	if err := a.accounts.SetEmojiStatus(ctx, me.ID, doc, until); err != nil {
		return nil, err
	}
	// every session of the user, the caller's too: Web A shows the new
	// status from this update
	a.pushUserChanged(me.ID)
	if doc != 0 {
		a.pushRecentStatuses(me.ID)
	}
	return true, nil
}

func (a *API) pushRecentStatuses(user int64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		a.push(ctx, user, &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateRecentEmojiStatuses{}}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: now()}, except(context.Background()))
	}()
}

func emojiStatuses(docs []int64, hash int64) tg.AccountEmojiStatusesClass {
	h := media.ListHash(docs)
	if hash != 0 && hash == h {
		return &tg.AccountEmojiStatusesNotModified{}
	}
	out := make([]tg.EmojiStatusClass, len(docs))
	for i, d := range docs {
		out[i] = &tg.EmojiStatus{DocumentID: d}
	}
	return &tg.AccountEmojiStatuses{Hash: h, Statuses: out}
}

func (a *API) registerUsers() {
	handle(a, tlprofile.SemanticMethodAccountUpdateEmojiStatus, a.updateEmojiStatus)
	handle(a, tlprofile.SemanticMethodAccountGetDefaultEmojiStatuses, func(ctx context.Context, r *tg.AccountGetDefaultEmojiStatusesRequest) (any, error) {
		if _, err := a.me(ctx); err != nil {
			return nil, err
		}
		_, items, err := a.media.DefaultStatusSet(ctx)
		if err != nil {
			return nil, err
		}
		docs := make([]int64, len(items))
		for i, it := range items {
			docs[i] = it.Doc.ID
		}
		return emojiStatuses(docs, r.Hash), nil
	})
	handle(a, tlprofile.SemanticMethodAccountGetRecentEmojiStatuses, func(ctx context.Context, r *tg.AccountGetRecentEmojiStatusesRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		docs, err := a.accounts.RecentStatuses(ctx, me.ID)
		if err != nil {
			return nil, err
		}
		return emojiStatuses(docs, r.Hash), nil
	})
	handle(a, tlprofile.SemanticMethodAccountClearRecentEmojiStatuses, func(ctx context.Context, _ *tg.AccountClearRecentEmojiStatusesRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		if err := a.accounts.ClearRecentStatuses(ctx, me.ID); err != nil {
			return nil, err
		}
		a.pushRecentStatuses(me.ID)
		return true, nil
	})
	handle(a, tlprofile.SemanticMethodUsersGetUsers, func(ctx context.Context, r *tg.UsersGetUsersRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		var want []int64
		for _, in := range r.ID {
			if id, err := a.inputUser(ctx, me, in); err == nil {
				want = append(want, id)
			}
		}
		return a.usersFor(ctx, me.ID, want)
	})
	handle(a, tlprofile.SemanticMethodUsersGetFullUser, func(ctx context.Context, r *tg.UsersGetFullUserRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		id, err := a.inputUser(ctx, me, r.ID)
		if err != nil {
			return nil, err
		}
		users, err := a.usersFor(ctx, me.ID, []int64{id})
		if err != nil || len(users) == 0 {
			return nil, tgerr.New(400, "USER_ID_INVALID")
		}
		all, err := a.accounts.Users(ctx, []int64{id})
		if err != nil {
			return nil, err
		}
		u := all[id]
		full := tg.UserFull{ID: tgUserID(u), Settings: tg.PeerSettings{}, NotifySettings: tg.PeerNotifySettings{}}
		au, err := a.audienceFor(ctx, me.ID, []int64{id}, "bio", "profilePhoto", "birthday")
		if err != nil {
			return nil, err
		}
		if au.can(id, "birthday") {
			p, err := a.accounts.Prefs(ctx, id)
			if err != nil {
				return nil, err
			}
			if p.Birthday.Day != 0 {
				full.SetBirthday(birthdayTL(p.Birthday))
			}
		}
		if id != me.ID {
			if _, n, err := a.chans.CommonChats(ctx, me.ID, id, 0, 1); err == nil {
				full.CommonChatsCount = n
			}
			if full.Settings, err = a.peerSettings(ctx, me, id); err != nil {
				return nil, err
			}
		}
		if au.can(id, "bio") && u.Bio != "" {
			full.SetAbout(u.Bio)
		}
		if u.PhotoID != 0 && au.can(id, "profilePhoto") {
			full.SetProfilePhoto(a.fullPhoto(ctx, u.PhotoID))
		}
		if full.NotifySettings, err = a.peerNotifyOf(ctx, me.ID, peer{user: id}); err != nil {
			return nil, err
		}
		if pin, err := a.msgs.TopPinned(ctx, me.ID, id); err != nil {
			return nil, err
		} else if pin != 0 {
			full.SetPinnedMsgID(pin)
		}
		if id != me.ID {
			blocked, _, err := a.msgs.Blocks(ctx, me.ID, id)
			if err != nil {
				return nil, err
			}
			full.Blocked = blocked
		}
		if a.stars != nil {
			if n, err := a.stars.GiftCount(ctx, stars.Holder{User: id}); err == nil && n > 0 {
				full.SetStargiftsCount(n)
			}
			// no rating yet: Telegram sends nothing, and a 0 badge on every
			// profile reads as broken. You still see your own, with its modal.
			if r, err := a.stars.ProfileRating(ctx, id); err == nil && (r.Stars != 0 || id == me.ID) {
				rating := tg.StarsRating{Level: r.Level, CurrentLevelStars: r.Current, Stars: r.Stars}
				if r.Next != 0 {
					rating.SetNextLevelStars(r.Next)
				}
				full.SetStarsRating(rating)
			}
		}
		a.botFull(ctx, me.ID, &full, u)
		mentioned, err := a.fullUserStories(ctx, me, id, &full)
		if err != nil {
			return nil, err
		}
		if len(mentioned) > 0 {
			// the user asked about goes first: Web A takes users[0] of getFullUser(self)
			// as who is signed in, so with viewers first it thought it was one of them
			if users, err = a.usersFor(ctx, me.ID, append([]int64{id}, mentioned...)); err != nil {
				return nil, err
			}
		}
		chats, err := a.personalChannel(ctx, me.ID, id, &full)
		if err != nil {
			return nil, err
		}
		return &tg.UsersUserFull{FullUser: full, Users: users, Chats: chats}, nil
	})
}
