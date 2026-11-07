package api

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/messages"
)

func (a *API) registerContacts() {
	handle(a, tlprofile.SemanticMethodContactsGetContacts, func(ctx context.Context, _ *tg.ContactsGetContactsRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		book, err := a.msgs.Contacts(ctx, me.ID)
		if err != nil {
			return nil, err
		}
		out := &tg.ContactsContacts{Contacts: []tg.Contact{}, SavedCount: len(book)}
		who := contactIDs(book)
		mutual, err := a.msgs.HaveAsContact(ctx, me.ID, who)
		if err != nil {
			return nil, err
		}
		for _, id := range who {
			out.Contacts = append(out.Contacts, tg.Contact{UserID: clientID(id), Mutual: mutual[id]})
		}
		out.Users, err = a.usersFor(ctx, me.ID, who)
		return out, err
	})
	handle(a, tlprofile.SemanticMethodContactsGetStatuses, func(ctx context.Context, _ *tg.ContactsGetStatusesRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		book, err := a.msgs.Contacts(ctx, me.ID)
		if err != nil {
			return nil, err
		}
		who := contactIDs(book)
		au, err := a.audienceFor(ctx, me.ID, who, "lastSeen")
		if err != nil {
			return nil, err
		}
		out := []tg.ContactStatus{}
		for _, id := range who {
			if au.can(id, "lastSeen") {
				out = append(out, tg.ContactStatus{UserID: clientID(id), Status: a.presence.status(id)})
			}
		}
		return out, nil
	})
	// phone book upload: only numbers whose owner lets the uploader find them
	handle(a, tlprofile.SemanticMethodContactsImportContacts, func(ctx context.Context, r *tg.ContactsImportContactsRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		if len(r.Contacts) > 500 {
			return nil, tgerr.New(400, "CONTACTS_TOO_MANY")
		}
		out := &tg.ContactsImportedContacts{Imported: []tg.ImportedContact{}, PopularInvites: []tg.PopularContact{}, RetryContacts: []int64{}}
		type hit struct {
			in tg.InputPhoneContact
			id int64
		}
		var hits []hit
		var found []int64
		for _, c := range r.Contacts {
			u, ok, err := a.accounts.UserByPhone(ctx, c.Phone)
			if err != nil {
				return nil, err
			}
			if !ok || u.ID == me.ID || u.Disabled || u.IsSystem {
				continue
			}
			hits = append(hits, hit{c, u.ID})
			found = append(found, u.ID)
		}
		au, err := a.audienceFor(ctx, me.ID, found, "addByPhone")
		if err != nil {
			return nil, err
		}
		var who []int64
		for _, h := range hits {
			if !au.can(h.id, "addByPhone") {
				continue
			}
			if err := a.msgs.AddContact(ctx, me.ID, messages.Contact{UserID: h.id, FirstName: h.in.FirstName, LastName: h.in.LastName}); err != nil {
				return nil, err
			}
			out.Imported = append(out.Imported, tg.ImportedContact{UserID: clientID(h.id), ClientID: h.in.ClientID})
			who = append(who, h.id)
		}
		out.Users, err = a.usersFor(ctx, me.ID, who)
		return out, err
	})
	handle(a, tlprofile.SemanticMethodContactsAddContact, func(ctx context.Context, r *tg.ContactsAddContactRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		id, err := a.inputUser(ctx, me, r.ID)
		if err != nil {
			return nil, err
		}
		if err := a.msgs.AddContact(ctx, me.ID, messages.Contact{UserID: id, FirstName: r.FirstName, LastName: r.LastName}); err != nil {
			return nil, err
		}
		users, err := a.usersFor(ctx, me.ID, []int64{id})
		if err != nil {
			return nil, err
		}
		return &tg.Updates{Updates: []tg.UpdateClass{}, Users: users, Chats: []tg.ChatClass{}, Date: now()}, nil
	})
	handle(a, tlprofile.SemanticMethodContactsDeleteContacts, func(ctx context.Context, r *tg.ContactsDeleteContactsRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		var list []int64
		for _, in := range r.ID {
			if id, err := a.inputUser(ctx, me, in); err == nil {
				list = append(list, id)
			}
		}
		if err := a.msgs.DeleteContacts(ctx, me.ID, list); err != nil {
			return nil, err
		}
		users, err := a.usersFor(ctx, me.ID, list)
		if err != nil {
			return nil, err
		}
		return &tg.Updates{Updates: []tg.UpdateClass{}, Users: users, Chats: []tg.ChatClass{}, Date: now()}, nil
	})
	handle(a, tlprofile.SemanticMethodContactsResolveUsername, func(ctx context.Context, r *tg.ContactsResolveUsernameRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		u, ok, err := a.accounts.UserByUsername(ctx, r.Username)
		if err != nil {
			return nil, err
		}
		if !ok {
			if u, ok, err = a.accounts.UserByAlias(ctx, r.Username); err != nil {
				return nil, err
			}
			if ok {
				u.UsernamePublic = true // the alias is public, whatever the primary name is
			}
		}
		if !ok || u.Disabled || (!u.UsernamePublic && u.ID != me.ID) {
			c, found, err := a.chans.ByUsername(ctx, r.Username)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, tgerr.New(400, "USERNAME_NOT_OCCUPIED")
			}
			chats, err := a.chatsFor(ctx, me.ID, []int64{c.ID})
			if err != nil {
				return nil, err
			}
			return &tg.ContactsResolvedPeer{Peer: &tg.PeerChannel{ChannelID: c.ID}, Users: []tg.UserClass{}, Chats: chats}, nil
		}
		users, err := a.usersFor(ctx, me.ID, []int64{u.ID})
		if err != nil {
			return nil, err
		}
		return &tg.ContactsResolvedPeer{Peer: &tg.PeerUser{UserID: tgUserID(u)}, Users: users, Chats: []tg.ChatClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodAccountUpdateStatus, func(ctx context.Context, r *tg.AccountUpdateStatusRequest) (any, error) {
		me, err := a.me(ctx)
		if err != nil {
			return nil, err
		}
		if a.presence.set(me.ID, !r.Offline) {
			go a.tellWatchers(me.ID, func(ctx context.Context, viewer int64) (tg.UpdateClass, bool) {
				// canSee also says no to people the user blocked
				if !a.canSee(ctx, me.ID, viewer, "lastSeen") {
					return nil, false
				}
				return &tg.UpdateUserStatus{UserID: tgUserID(me), Status: a.presence.status(me.ID)}, true
			})
		}
		return true, nil
	})
	handle(a, tlprofile.SemanticMethodContactsBlock, func(ctx context.Context, r *tg.ContactsBlockRequest) (any, error) {
		return a.setBlocked(ctx, r.ID, r.MyStoriesFrom, true)
	})
	handle(a, tlprofile.SemanticMethodContactsUnblock, func(ctx context.Context, r *tg.ContactsUnblockRequest) (any, error) {
		return a.setBlocked(ctx, r.ID, r.MyStoriesFrom, false)
	})
	handle(a, tlprofile.SemanticMethodContactsGetBlocked, a.getBlocked)
}

func contactIDs(book []messages.Contact) []int64 {
	out := make([]int64, len(book))
	for i, c := range book {
		out[i] = c.UserID
	}
	return out
}

// setBlocked is contacts.block and contacts.unblock. Only people can be
// blocked here; with stories set it's the "hide my stories from" list.
func (a *API) setBlocked(ctx context.Context, in tg.InputPeerClass, stories, block bool) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if stories {
		return a.setStoriesBlocked(ctx, me, in, block)
	}
	id, err := a.peerUser(ctx, me, in)
	if err != nil {
		return nil, err
	}
	if id == me.ID {
		return nil, tgerr.New(400, "CONTACT_ID_INVALID")
	}
	var changed bool
	if block {
		changed, err = a.msgs.Block(ctx, me.ID, id)
	} else {
		changed, err = a.msgs.Unblock(ctx, me.ID, id)
	}
	if err != nil {
		return nil, err
	}
	if changed {
		users, err := a.usersFor(ctx, me.ID, []int64{id})
		if err != nil {
			return nil, err
		}
		a.echo(ctx, me, []tg.UpdateClass{&tg.UpdatePeerBlocked{Blocked: block, PeerID: &tg.PeerUser{UserID: clientID(id)}}}, users)
		// an open chat with me would keep the status it last heard
		var status tg.UserStatusClass = &tg.UserStatusEmpty{}
		if !block && a.canSee(ctx, me.ID, id, "lastSeen") {
			status = a.presence.status(me.ID)
		}
		a.deliver([]messages.Event{{UserID: id, Update: &tg.UpdateUserStatus{UserID: tgUserID(me), Status: status}}}, edge.Session{})
		// the Add contact / Block user bar goes, or comes back, on every app of mine
		settings, err := a.peerSettings(ctx, me, id)
		if err != nil {
			return nil, err
		}
		a.deliver([]messages.Event{{UserID: me.ID, Update: &tg.UpdatePeerSettings{Peer: &tg.PeerUser{UserID: clientID(id)}, Settings: settings}}}, edge.Session{})
	}
	return true, nil
}

func (a *API) getBlocked(ctx context.Context, r *tg.ContactsGetBlockedRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	empty := &tg.ContactsBlocked{Blocked: []tg.PeerBlocked{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
	if r.MyStoriesFrom {
		return a.storiesBlockedList(ctx, me, r.Offset, r.Limit)
	}
	list, total, err := a.msgs.Blocked(ctx, me.ID, r.Offset, r.Limit)
	if err != nil {
		return nil, err
	}
	var who []int64
	for _, b := range list {
		empty.Blocked = append(empty.Blocked, tg.PeerBlocked{PeerID: &tg.PeerUser{UserID: clientID(b.UserID)}, Date: b.Date})
		who = append(who, b.UserID)
	}
	if empty.Users, err = a.usersFor(ctx, me.ID, who); err != nil {
		return nil, err
	}
	if r.Offset <= 0 && len(list) == total {
		return empty, nil
	}
	return &tg.ContactsBlockedSlice{Count: total, Blocked: empty.Blocked, Chats: empty.Chats, Users: empty.Users}, nil
}

// sendDropped is the block check for a private message, sent or forwarded.
// Writing to someone you blocked is refused. Being blocked looks like
// nothing at all, as on Telegram: the send works, true means only the
// sender's copy gets written.
func (a *API) sendDropped(ctx context.Context, from, to int64) (bool, error) {
	if from == to {
		return false, nil
	}
	iBlocked, theyBlocked, err := a.msgs.Blocks(ctx, from, to)
	if err != nil {
		return false, err
	}
	if iBlocked {
		return false, tgerr.New(400, "YOU_BLOCKED_USER")
	}
	// said out loud, not dropped like a block: the person should know why a bot
	// they can see won't answer
	if usable, err := a.bots.Usable(ctx, to, from); err != nil {
		return false, err
	} else if !usable {
		return false, errBotRestricted
	}
	return theyBlocked, nil
}

// checkBlocked refuses what can't be quietly dropped: touching the other
// side's copy of a message, a reaction, in either direction.
func (a *API) checkBlocked(ctx context.Context, from, to int64) error {
	if from == to {
		return nil
	}
	iBlocked, theyBlocked, err := a.msgs.Blocks(ctx, from, to)
	switch {
	case err != nil:
		return err
	case iBlocked:
		return tgerr.New(400, "YOU_BLOCKED_USER")
	case theyBlocked:
		return tgerr.New(403, "USER_IS_BLOCKED")
	}
	return nil
}
