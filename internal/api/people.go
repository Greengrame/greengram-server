package api

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
)

var errPeerInvalid = tgerr.New(400, "PEER_ID_INVALID")

// peerUser resolves an input peer to a user id; private chats only until
// phase 5 brings groups and channels.
func (a *API) peerUser(ctx context.Context, me account.User, p tg.InputPeerClass) (int64, error) {
	var id int64
	switch v := p.(type) {
	case *tg.InputPeerSelf:
		return me.ID, nil
	case *tg.InputPeerUser:
		id = serverID(v.UserID)
	case *tg.InputPeerUserFromMessage:
		id = serverID(v.UserID)
	default:
		return 0, errPeerInvalid
	}
	if id == me.ID {
		return id, nil
	}
	users, err := a.accounts.Users(ctx, []int64{id})
	if err != nil {
		return 0, err
	}
	u, ok := users[id]
	if !ok || u.Disabled {
		return 0, errPeerInvalid
	}
	return id, nil
}

func (a *API) inputUser(ctx context.Context, me account.User, in tg.InputUserClass) (int64, error) {
	switch v := in.(type) {
	case *tg.InputUserSelf:
		return me.ID, nil
	case *tg.InputUser:
		return a.peerUser(ctx, me, &tg.InputPeerUser{UserID: v.UserID})
	case *tg.InputUserFromMessage:
		return a.peerUser(ctx, me, &tg.InputPeerUser{UserID: v.UserID})
	}
	return 0, tgerr.New(400, "USER_ID_INVALID")
}

// presence is who is online; it lives in memory and resets on restart,
// which shows everyone as "last seen recently" until they come back.
type presence struct {
	mu   sync.Mutex
	seen map[int64]presenceState
}

type presenceState struct{ onlineUntil, lastSeen int64 }

const onlineFor = 5 * time.Minute

// set records a status change; changed is false for a repeat (clients
// re-send "online" every few minutes while open).
func (p *presence) set(user int64, online bool) (changed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen == nil {
		p.seen = map[int64]presenceState{}
	}
	now := time.Now()
	st, known := p.seen[user]
	changed = !known || (st.onlineUntil > now.Unix()) != online
	st.lastSeen = now.Unix()
	if online {
		st.onlineUntil = now.Add(onlineFor).Unix()
	} else {
		st.onlineUntil = 0
	}
	p.seen[user] = st
	return changed
}

func (p *presence) status(user int64) tg.UserStatusClass {
	p.mu.Lock()
	st, ok := p.seen[user]
	p.mu.Unlock()
	now := time.Now().Unix()
	switch {
	case !ok:
		return &tg.UserStatusRecently{}
	case st.onlineUntil > now:
		return &tg.UserStatusOnline{Expires: int(st.onlineUntil)}
	default:
		return &tg.UserStatusOffline{WasOnline: int(st.lastSeen)}
	}
}

// canSee applies owner's privacy rule for key to viewer. Someone owner
// blocked passes no rule.
func (a *API) canSee(ctx context.Context, owner, viewer int64, key string) bool {
	if owner == viewer {
		return true
	}
	au, err := a.audienceFor(ctx, viewer, []int64{owner}, key)
	return err == nil && au.can(owner, key)
}

// audience is what privacy decisions for one viewer need about a set of
// owners, loaded in a fixed number of queries however many owners there are.
type audience struct {
	viewer     int64
	theyHaveMe map[int64]bool // owners with the viewer in their book
	blockedMe  map[int64]bool // owners who blocked the viewer
	rules      map[int64]map[string]account.Privacy
	chats      map[int64]bool // chats named in those rules that the viewer is in
}

func (a *API) audienceFor(ctx context.Context, viewer int64, owners []int64, keys ...string) (*audience, error) {
	au := &audience{viewer: viewer}
	var others []int64
	for _, id := range owners {
		if id != viewer {
			others = append(others, id)
		}
	}
	var err error
	if au.theyHaveMe, err = a.msgs.HaveAsContact(ctx, viewer, others); err != nil {
		return nil, err
	}
	if au.blockedMe, err = a.msgs.BlockedBy(ctx, viewer, others); err != nil {
		return nil, err
	}
	if au.rules, err = a.accounts.PrivacyMany(ctx, others, keys); err != nil {
		return nil, err
	}
	var named []int64
	for _, byKey := range au.rules {
		for _, p := range byKey {
			named = append(named, chatIDs(p.BlockChatIDs)...)
			named = append(named, chatIDs(p.AllowChatIDs)...)
		}
	}
	if au.chats, err = a.chans.InWhich(ctx, viewer, named); err != nil {
		return nil, err
	}
	return au, nil
}

// can is canSee for an owner the audience was loaded with.
func (au *audience) can(owner int64, key string) bool {
	if owner == au.viewer {
		return true
	}
	p, ok := au.rules[owner][key]
	if !ok || au.blockedMe[owner] {
		return false
	}
	v := strconv.FormatInt(au.viewer, 10)
	if slices.Contains(p.BlockUserIDs, v) || au.inAny(p.BlockChatIDs) {
		return false
	}
	if slices.Contains(p.AllowUserIDs, v) || au.inAny(p.AllowChatIDs) {
		return true
	}
	switch p.Visibility {
	case "everybody":
		return true
	case "contacts":
		return au.theyHaveMe[owner]
	}
	return false
}

func (au *audience) inAny(list []string) bool {
	for _, id := range chatIDs(list) {
		if au.chats[id] {
			return true
		}
	}
	return false
}

func chatIDs(list []string) []int64 {
	var out []int64
	for _, s := range list {
		if id, err := strconv.ParseInt(s, 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// usersFor builds user objects as viewer sees them: contact names, and
// phone and last seen only where the owner's privacy allows.
func (a *API) usersFor(ctx context.Context, viewer int64, list []int64) ([]tg.UserClass, error) {
	out := []tg.UserClass{}
	if len(list) == 0 {
		return out, nil
	}
	users, err := a.accounts.Users(ctx, list)
	if err != nil {
		return nil, err
	}
	found := make([]int64, 0, len(users))
	for id := range users {
		found = append(found, id)
	}
	mine, err := a.msgs.Contacts(ctx, viewer)
	if err != nil {
		return nil, err
	}
	book := map[int64]string{}
	for _, c := range mine {
		book[c.UserID] = strings.TrimSpace(c.FirstName + " " + c.LastName)
	}
	au, err := a.audienceFor(ctx, viewer, found, "phoneNumber", "lastSeen", "profilePhoto")
	if err != nil {
		return nil, err
	}
	var botList map[int64]bots.Bot
	if a.bots != nil {
		var want []int64
		for _, u := range users {
			if u.IsBot {
				want = append(want, u.ID)
			}
		}
		if botList, err = a.bots.Many(ctx, want); err != nil {
			return nil, err
		}
	}
	seen := map[int64]bool{}
	for _, id := range list {
		u, ok := users[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, a.userView(viewer, u, book, au, botList))
	}
	if err := a.storiesBadges(ctx, viewer, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *API) userView(viewer int64, u account.User, book map[int64]string, au *audience, botList map[int64]bots.Bot) *tg.User {
	if u.ID == viewer {
		t := tgUser(u, true)
		t.Status = a.presence.status(u.ID)
		a.setUserPhoto(t, u, au)
		return t
	}
	t := tgUser(u, false)
	name, contact := book[u.ID]
	if contact {
		t.Contact = true
		t.MutualContact = au.theyHaveMe[u.ID]
		if name != "" {
			t.FirstName, t.LastName = splitName(name)
		}
	}
	if au.can(u.ID, "phoneNumber") {
		t.Phone = strings.TrimPrefix(u.Phone, "+")
	}
	switch {
	case au.blockedMe[u.ID]:
		// "last seen a long time ago", what Telegram shows the people you block
		t.Status = &tg.UserStatusEmpty{}
	case au.can(u.ID, "lastSeen"):
		t.Status = a.presence.status(u.ID)
	}
	t.Premium = u.Premium(time.Now())
	a.setUserPhoto(t, u, au)
	if b, ok := botList[u.ID]; ok {
		botFlags(viewer, t, b)
	}
	return t
}
