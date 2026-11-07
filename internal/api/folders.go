package api

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/messages"
)

// Chat folders and the archive. The apps sort chats into folders
// themselves from the flags and peer lists; the server keeps the lists
// and tells the user's other sessions. Folder updates carry no pts, so
// they're pushed, not journaled; the archive's updateFolderPeers does
// carry one and goes through the user's journal.
func (a *API) registerFolders() {
	handle(a, tlprofile.SemanticMethodMessagesGetDialogFilters, a.getDialogFilters)
	handle(a, tlprofile.SemanticMethodMessagesGetSuggestedDialogFilters, a.suggestedDialogFilters)
	handle(a, tlprofile.SemanticMethodMessagesUpdateDialogFilter, a.updateDialogFilter)
	handle(a, tlprofile.SemanticMethodMessagesUpdateDialogFiltersOrder, a.updateDialogFiltersOrder)
	handle(a, tlprofile.SemanticMethodMessagesToggleDialogFilterTags, a.toggleDialogFilterTags)
	handle(a, tlprofile.SemanticMethodFoldersEditPeerFolders, a.editPeerFolders)
}

func (a *API) getDialogFilters(ctx context.Context, _ *tg.MessagesGetDialogFiltersRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	list, tags, err := a.msgs.Filters(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesDialogFilters{TagsEnabled: tags, Filters: list}, nil
}

func title(s string) tg.TextWithEntities {
	return tg.TextWithEntities{Text: s, Entities: []tg.MessageEntityClass{}}
}

// suggestions are fixed, minus what the user already has by that name.
var suggestions = []tg.DialogFilterSuggested{
	{Filter: &tg.DialogFilter{ID: 2, Title: title("Unread"), Emoticon: "✅", Contacts: true, NonContacts: true, Groups: true, Broadcasts: true, Bots: true, ExcludeRead: true},
		Description: "New messages from all chats."},
	{Filter: &tg.DialogFilter{ID: 3, Title: title("Personal"), Emoticon: "👤", Contacts: true, NonContacts: true},
		Description: "Only messages from personal chats."},
	{Filter: &tg.DialogFilter{ID: 4, Title: title("Groups"), Emoticon: "👥", Groups: true},
		Description: "Only messages from groups."},
	{Filter: &tg.DialogFilter{ID: 5, Title: title("Channels"), Emoticon: "📢", Broadcasts: true},
		Description: "Only messages from channels."},
	{Filter: &tg.DialogFilter{ID: 6, Title: title("Bots"), Emoticon: "🤖", Bots: true},
		Description: "Only messages from bots."},
}

func (a *API) suggestedDialogFilters(ctx context.Context, _ *tg.MessagesGetSuggestedDialogFiltersRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	have, _, err := a.msgs.Filters(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	for _, f := range have {
		if v, ok := f.(*tg.DialogFilter); ok {
			taken[v.Title.Text] = true
		}
	}
	out := []tg.DialogFilterSuggested{}
	for _, s := range suggestions {
		f := s.Filter.(*tg.DialogFilter)
		if taken[f.Title.Text] {
			continue
		}
		// a copy each time: the peer lists must be empty vectors, not nil
		c := *f
		c.PinnedPeers, c.IncludePeers, c.ExcludePeers = []tg.InputPeerClass{}, []tg.InputPeerClass{}, []tg.InputPeerClass{}
		out = append(out, tg.DialogFilterSuggested{Filter: &c, Description: s.Description})
	}
	return out, nil
}

func (a *API) updateDialogFilter(ctx context.Context, r *tg.MessagesUpdateDialogFilterRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	f, _ := r.GetFilter()
	if _, ok := f.(*tg.DialogFilterDefault); ok {
		return nil, tgerr.New(400, "FILTER_ID_INVALID")
	}
	if err := a.msgs.SaveFilter(ctx, me.ID, r.ID, f); err != nil {
		return nil, err
	}
	u := &tg.UpdateDialogFilter{ID: r.ID}
	if f != nil {
		// what's stored: the id inside is the one asked for
		list, _, err := a.msgs.Filters(ctx, me.ID)
		if err != nil {
			return nil, err
		}
		for _, x := range list {
			if id, ok := filterID(x); ok && id == r.ID {
				u.SetFilter(x)
			}
		}
	}
	a.echo(ctx, me, []tg.UpdateClass{u}, []tg.UserClass{})
	return true, nil
}

func filterID(f tg.DialogFilterClass) (int, bool) {
	switch v := f.(type) {
	case *tg.DialogFilter:
		return v.ID, true
	case *tg.DialogFilterChatlist:
		return v.ID, true
	}
	return 0, false
}

func (a *API) updateDialogFiltersOrder(ctx context.Context, r *tg.MessagesUpdateDialogFiltersOrderRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	order, err := a.msgs.OrderFilters(ctx, me.ID, r.Order)
	if err != nil {
		return nil, err
	}
	if order == nil {
		order = []int{}
	}
	a.echo(ctx, me, []tg.UpdateClass{&tg.UpdateDialogFilterOrder{Order: order}}, []tg.UserClass{})
	return true, nil
}

func (a *API) toggleDialogFilterTags(ctx context.Context, r *tg.MessagesToggleDialogFilterTagsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.msgs.SetFilterTags(ctx, me.ID, r.Enabled); err != nil {
		return nil, err
	}
	// there's no update for the flag alone; this one makes the apps reload
	a.echo(ctx, me, []tg.UpdateClass{&tg.UpdateDialogFilters{}}, []tg.UserClass{})
	return true, nil
}

func (a *API) editPeerFolders(ctx context.Context, r *tg.FoldersEditPeerFoldersRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if len(r.FolderPeers) > 100 {
		return nil, tgerr.New(400, "LIMIT_INVALID")
	}
	var moves []messages.FolderMove
	var users, chans []int64
	for _, fp := range r.FolderPeers {
		p, err := a.peer(ctx, me, fp.Peer)
		if err != nil {
			return nil, err
		}
		moves = append(moves, messages.FolderMove{User: p.user, Channel: p.channel, Folder: fp.FolderID})
		if p.channel != 0 {
			chans = append(chans, p.channel)
		} else {
			users = append(users, p.user)
		}
	}
	u, err := a.msgs.SetFolders(ctx, me.ID, moves)
	if err != nil {
		return nil, err
	}
	out := emptyUpdates()
	if u == nil {
		return out, nil
	}
	out.Updates = []tg.UpdateClass{u}
	if out.Users, err = a.usersFor(ctx, me.ID, users); err != nil {
		return nil, err
	}
	if out.Chats, err = a.chatsFor(ctx, me.ID, chans); err != nil {
		return nil, err
	}
	a.echoChats(ctx, me, out.Updates, out.Users, out.Chats)
	return out, nil
}
