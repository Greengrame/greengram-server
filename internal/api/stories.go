package api

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/stories"
)

// RegisterStories installs users' stories. Channels have none here: their
// lists come back empty and posting to one is refused.
func (a *API) RegisterStories(s *stories.Service) {
	a.stories = s
	handle(a, tlprofile.SemanticMethodStoriesSendStory, a.sendStory)
	handle(a, tlprofile.SemanticMethodStoriesEditStory, a.editStory)
	handle(a, tlprofile.SemanticMethodStoriesDeleteStories, a.deleteStories)
	handle(a, tlprofile.SemanticMethodStoriesTogglePinned, a.togglePinnedStories)
	handle(a, tlprofile.SemanticMethodStoriesTogglePinnedToTop, a.togglePinnedToTop)
	handle(a, tlprofile.SemanticMethodStoriesGetAllStories, a.getAllStories)
	handle(a, tlprofile.SemanticMethodStoriesGetPeerStories, a.getPeerStories)
	handle(a, tlprofile.SemanticMethodStoriesGetPeerMaxIDs, a.getPeerMaxIDs)
	handle(a, tlprofile.SemanticMethodStoriesGetStoriesByID, a.getStoriesByID)
	handle(a, tlprofile.SemanticMethodStoriesGetPinnedStories, a.getPinnedStories)
	handle(a, tlprofile.SemanticMethodStoriesGetStoriesArchive, a.getStoriesArchive)
	handle(a, tlprofile.SemanticMethodStoriesReadStories, a.readStories)
	handle(a, tlprofile.SemanticMethodStoriesIncrementStoryViews, a.incrementStoryViews)
	handle(a, tlprofile.SemanticMethodStoriesGetStoriesViews, a.getStoriesViews)
	handle(a, tlprofile.SemanticMethodStoriesGetStoryViewsList, a.getStoryViewsList)
	handle(a, tlprofile.SemanticMethodStoriesSendReaction, a.sendStoryReaction)
	handle(a, tlprofile.SemanticMethodStoriesTogglePeerStoriesHidden, a.togglePeerStoriesHidden)
	handle(a, tlprofile.SemanticMethodStoriesGetAllReadPeerStories, a.getAllReadPeerStories)
	handle(a, tlprofile.SemanticMethodStoriesCanSendStory, a.canSendStory)
	handle(a, tlprofile.SemanticMethodStoriesExportStoryLink, a.exportStoryLink)
	handle(a, tlprofile.SemanticMethodContactsEditCloseFriends, a.editCloseFriends)
	handle(a, tlprofile.SemanticMethodStoriesGetChatsToSend, func(context.Context, *tg.StoriesGetChatsToSendRequest) (any, error) {
		return &tg.MessagesChats{Chats: []tg.ChatClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodStoriesToggleAllStoriesHidden, func(context.Context, *tg.StoriesToggleAllStoriesHiddenRequest) (any, error) {
		return true, nil
	})
	handle(a, tlprofile.SemanticMethodStoriesActivateStealthMode, func(context.Context, *tg.StoriesActivateStealthModeRequest) (any, error) {
		return emptyUpdates(), nil
	})
}

const day = 86400

// storyPeriod checks a story's lifetime: 6, 12 or 24 hours for everyone,
// 48 with Premium, like Telegram. Test DCs also take 60 and 300 seconds.
func (a *API) storyPeriod(me account.User, period int) (int, error) {
	switch period {
	case 0:
		return day, nil
	case day / 4, day / 2, day:
		return period, nil
	case 2 * day:
		if !me.Premium(nowTime()) {
			return 0, tgerr.New(400, "PREMIUM_ACCOUNT_REQUIRED")
		}
		return period, nil
	case 60, 300:
		if a.in.TestMode {
			return period, nil
		}
	}
	return 0, tgerr.New(400, "STORY_PERIOD_INVALID")
}

// Telegram's story_caption_length_limit_default and _premium.
const (
	storyCaptionMax        = 200
	storyCaptionMaxPremium = 2048
)

func checkStoryCaption(me account.User, caption string) error {
	limit := storyCaptionMax
	if me.Premium(nowTime()) {
		limit = storyCaptionMaxPremium
	}
	if len([]rune(caption)) > limit {
		return tgerr.New(400, "MEDIA_CAPTION_TOO_LONG")
	}
	return nil
}

// storyMedia stores the photo or video of a story.
func (a *API) storyMedia(ctx context.Context, me account.User, in tg.InputMediaClass) (media.Ref, error) {
	if a.media == nil {
		return media.Ref{}, tgerr.New(400, "MEDIA_INVALID")
	}
	ref, err := a.media.FromInput(ctx, me.ID, in)
	if err != nil {
		return media.Ref{}, err
	}
	if ref.Kind != media.KindPhoto && ref.Kind != media.KindVideo {
		return media.Ref{}, tgerr.New(400, "MEDIA_INVALID")
	}
	return ref, nil
}

// storyRules reads input privacy rules. Without a main rule (allow all,
// contacts or close friends) only the allow lists see the story, which is
// Telegram's "selected contacts".
func (a *API) storyRules(ctx context.Context, me account.User, in []tg.InputPrivacyRuleClass) (stories.Rules, error) {
	r := stories.Rules{}
	users := func(list []tg.InputUserClass) []int64 {
		var out []int64
		for _, u := range list {
			if id, err := a.inputUser(ctx, me, u); err == nil && id != me.ID {
				out = append(out, id)
			}
		}
		return out
	}
	main := func(v string) {
		if r.Audience == "" {
			r.Audience = v
		}
	}
	for _, rule := range in {
		switch v := rule.(type) {
		case *tg.InputPrivacyValueAllowAll:
			main(stories.Everybody)
		case *tg.InputPrivacyValueAllowContacts:
			main(stories.Contacts)
		case *tg.InputPrivacyValueAllowCloseFriends:
			main(stories.CloseFriends)
		case *tg.InputPrivacyValueDisallowAll:
			main(stories.Selected)
		case *tg.InputPrivacyValueAllowUsers:
			r.AllowUsers = append(r.AllowUsers, users(v.Users)...)
		case *tg.InputPrivacyValueDisallowUsers:
			r.DenyUsers = append(r.DenyUsers, users(v.Users)...)
		case *tg.InputPrivacyValueAllowChatParticipants:
			r.AllowChats = append(r.AllowChats, v.Chats...)
		case *tg.InputPrivacyValueDisallowChatParticipants:
			r.DenyChats = append(r.DenyChats, v.Chats...)
		}
	}
	if r.Audience == "" {
		r.Audience = stories.Selected
	}
	if len(r.AllowUsers)+len(r.DenyUsers)+len(r.AllowChats)+len(r.DenyChats) > 1000 {
		return r, tgerr.New(400, "PRIVACY_TOO_LONG")
	}
	return r, nil
}

// storyPrivacy is the rules as the author's apps show them.
func storyPrivacy(r stories.Rules) []tg.PrivacyRuleClass {
	ids := func(list []int64) []int64 {
		out := make([]int64, len(list))
		for i, id := range list {
			out[i] = clientID(id)
		}
		return out
	}
	out := []tg.PrivacyRuleClass{}
	if len(r.AllowUsers) > 0 {
		out = append(out, &tg.PrivacyValueAllowUsers{Users: ids(r.AllowUsers)})
	}
	if len(r.AllowChats) > 0 {
		out = append(out, &tg.PrivacyValueAllowChatParticipants{Chats: append([]int64{}, r.AllowChats...)})
	}
	if len(r.DenyUsers) > 0 {
		out = append(out, &tg.PrivacyValueDisallowUsers{Users: ids(r.DenyUsers)})
	}
	if len(r.DenyChats) > 0 {
		out = append(out, &tg.PrivacyValueDisallowChatParticipants{Chats: append([]int64{}, r.DenyChats...)})
	}
	switch r.Audience {
	case stories.Everybody:
		out = append(out, &tg.PrivacyValueAllowAll{})
	case stories.Contacts:
		out = append(out, &tg.PrivacyValueAllowContacts{})
	case stories.CloseFriends:
		out = append(out, &tg.PrivacyValueAllowCloseFriends{})
	default:
		out = append(out, &tg.PrivacyValueDisallowAll{})
	}
	return out
}

func userPeerTL(id int64) *tg.PeerUser { return &tg.PeerUser{UserID: clientID(id)} }

// storyItem is st as viewer sees it. Only the author gets the rules, the
// view counts and out.
func storyItem(viewer int64, st stories.Story, c *stories.Counts, mine tg.ReactionClass) *tg.StoryItem {
	it := &tg.StoryItem{
		ID: st.ID, Date: st.Date, ExpireDate: st.Expire, Pinned: st.Pinned, Noforwards: st.Noforwards, Edited: st.EditDate != 0,
		Caption: st.Caption, Entities: st.Entities, Media: st.Media.TL,
	}
	switch st.Rules.Audience {
	case stories.Everybody:
		it.Public = true
	case stories.Contacts:
		it.Contacts = true
	case stories.CloseFriends:
		it.CloseFriends = true
	default:
		it.SelectedContacts = true
	}
	if viewer == st.Owner {
		it.Out = true
		it.Privacy = storyPrivacy(st.Rules)
		if c != nil {
			v := tg.StoryViews{HasViewers: c.Views > 0, ViewsCount: c.Views, ReactionsCount: c.Reactions}
			for _, rc := range c.ByReaction {
				v.Reactions = append(v.Reactions, tg.ReactionCount{Reaction: rc.Reaction, Count: rc.Count})
			}
			for _, u := range c.Recent {
				v.RecentViewers = append(v.RecentViewers, clientID(u))
			}
			it.Views = v
		}
	}
	if mine != nil {
		it.SentReaction = mine
	}
	return it
}

// storyItems renders owner's stories for viewer with the counts or the
// viewer's reactions they carry, plus the users those mention.
func (a *API) storyItems(ctx context.Context, viewer, owner int64, list []stories.Story) ([]tg.StoryItemClass, []int64, error) {
	ids := make([]int, len(list))
	for i, st := range list {
		ids[i] = st.ID
	}
	var (
		counts map[int]*stories.Counts
		mine   map[int]tg.ReactionClass
		users  []int64
		err    error
	)
	if viewer == owner {
		if counts, err = a.stories.Counts(ctx, owner, ids); err != nil {
			return nil, nil, err
		}
		for _, c := range counts {
			users = append(users, c.Recent...)
		}
	} else if mine, err = a.stories.MyReactions(ctx, viewer, owner, ids); err != nil {
		return nil, nil, err
	}
	out := make([]tg.StoryItemClass, len(list))
	for i, st := range list {
		out[i] = storyItem(viewer, st, counts[st.ID], mine[st.ID])
	}
	return out, users, nil
}

// storyPeer is whose stories an input peer means; 0 for a chat, which has
// none here.
func (a *API) storyPeer(ctx context.Context, me account.User, p tg.InputPeerClass) (int64, error) {
	switch p.(type) {
	case *tg.InputPeerChannel, *tg.InputPeerChannelFromMessage, *tg.InputPeerChat:
		return 0, nil
	}
	return a.peerUser(ctx, me, p)
}

// ownStories is storyPeer for calls only the author can make.
func (a *API) ownStories(ctx context.Context, me account.User, p tg.InputPeerClass) error {
	id, err := a.storyPeer(ctx, me, p)
	if err != nil {
		return err
	}
	if id != me.ID {
		return errPeerInvalid
	}
	return nil
}

func (a *API) sendStory(ctx context.Context, r *tg.StoriesSendStoryRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.ownStories(ctx, me, r.Peer); err != nil {
		return nil, err
	}
	if r.FwdFromID != nil || r.FwdFromStory != 0 {
		// reposts need the original's rules carried along; not yet
		return nil, tgerr.New(400, "STORY_FORWARD_UNSUPPORTED")
	}
	period, err := a.storyPeriod(me, r.Period)
	if err != nil {
		return nil, err
	}
	if err := checkStoryCaption(me, r.Caption); err != nil {
		return nil, err
	}
	rules, err := a.storyRules(ctx, me, r.PrivacyRules)
	if err != nil {
		return nil, err
	}
	ref, err := a.storyMedia(ctx, me, r.Media)
	if err != nil {
		return nil, err
	}
	st, err := a.stories.Post(ctx, me.ID, stories.Draft{
		RandomID: r.RandomID, Period: period, Pinned: r.Pinned, Noforwards: r.Noforwards,
		Caption: r.Caption, Entities: r.Entities, Media: ref, Rules: rules,
	})
	if err != nil {
		return nil, err
	}
	a.presence.set(me.ID, true)
	a.fanOutStory(ctx, st, nil)
	return a.storyReply(ctx, me, st, &tg.UpdateStoryID{ID: st.ID, RandomID: r.RandomID})
}

// storyReply is the Updates a call about one of the caller's stories
// answers with.
func (a *API) storyReply(ctx context.Context, me account.User, st stories.Story, first ...tg.UpdateClass) (*tg.Updates, error) {
	items, mentioned, err := a.storyItems(ctx, me.ID, me.ID, []stories.Story{st})
	if err != nil {
		return nil, err
	}
	out := &tg.Updates{Updates: append(first, &tg.UpdateStory{Peer: userPeerTL(me.ID), Story: items[0]}), Chats: []tg.ChatClass{}, Date: now()}
	if out.Users, err = a.usersFor(ctx, me.ID, append(mentioned, me.ID)); err != nil {
		return nil, err
	}
	return out, nil
}

// fanOutStory pushes st to the author's other sessions and to everyone
// with it in their feed; gone are the watchers who lost it (deleted, or
// its rules changed), who get it as deleted.
func (a *API) fanOutStory(ctx context.Context, st stories.Story, gone []int64) {
	watchers, err := a.stories.Watchers(ctx, st)
	if err != nil {
		slog.Warn("api: story watchers", "err", err)
		return
	}
	skip := except(ctx)
	a.deliverStory(st, false, append(watchers, st.Owner), skip)
	if len(gone) > 0 {
		a.deliverStory(st, true, gone, skip)
	}
}

// deliverStory queues an updateStory for each user, built in their own job
// from their own copy of st.
func (a *API) deliverStory(st stories.Story, deleted bool, to []int64, skip edge.Session) {
	seen := map[int64]bool{}
	for _, viewer := range to {
		if seen[viewer] || !a.hub.online(viewer) {
			continue
		}
		seen[viewer] = true
		c, err := st.Clone()
		if err != nil {
			slog.Warn("api: story clone", "err", err)
			return
		}
		a.out.add(viewer, func(ctx context.Context) {
			var (
				item      tg.StoryItemClass = &tg.StoryItemDeleted{ID: c.ID}
				mentioned []int64
			)
			if !deleted {
				items, users, err := a.storyItems(ctx, viewer, c.Owner, []stories.Story{c})
				if err != nil {
					slog.Warn("api: story deliver", "err", err)
					return
				}
				item, mentioned = items[0], users
			}
			users, err := a.usersFor(ctx, viewer, append(mentioned, c.Owner))
			if err != nil {
				slog.Warn("api: story deliver users", "err", err)
				return
			}
			a.push(ctx, viewer, &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateStory{Peer: userPeerTL(c.Owner), Story: item}},
				Users: users, Chats: []tg.ChatClass{}, Date: now()}, skip)
		})
	}
}

func (a *API) editStory(ctx context.Context, r *tg.StoriesEditStoryRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.ownStories(ctx, me, r.Peer); err != nil {
		return nil, err
	}
	var e stories.Edit
	if m, ok := r.GetMedia(); ok {
		ref, err := a.storyMedia(ctx, me, m)
		if err != nil {
			return nil, err
		}
		e.Media = &ref
	}
	if c, ok := r.GetCaption(); ok {
		if err := checkStoryCaption(me, c); err != nil {
			return nil, err
		}
		e.Caption, e.Entities = &c, r.Entities
	}
	if p, ok := r.GetPrivacyRules(); ok {
		rules, err := a.storyRules(ctx, me, p)
		if err != nil {
			return nil, err
		}
		e.Rules = &rules
	}
	before, after, err := a.stories.Edit(ctx, me.ID, r.ID, e)
	if err != nil {
		return nil, err
	}
	if after.Active(now()) {
		var gone []int64
		if e.Rules != nil {
			was, err := a.stories.Watchers(ctx, before)
			if err != nil {
				return nil, err
			}
			now, err := a.stories.Watchers(ctx, after)
			if err != nil {
				return nil, err
			}
			for _, id := range was {
				if !slices.Contains(now, id) {
					gone = append(gone, id)
				}
			}
		}
		a.fanOutStory(ctx, after, gone)
	} else {
		a.deliverStory(after, false, []int64{me.ID}, except(ctx))
	}
	return a.storyReply(ctx, me, after)
}

func (a *API) deleteStories(ctx context.Context, r *tg.StoriesDeleteStoriesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.ownStories(ctx, me, r.Peer); err != nil {
		return nil, err
	}
	list, err := a.stories.Delete(ctx, me.ID, r.ID)
	if err != nil {
		return nil, err
	}
	ids := []int{}
	for _, st := range list {
		ids = append(ids, st.ID)
		to := []int64{me.ID}
		if st.Active(now()) {
			w, err := a.stories.Watchers(ctx, st)
			if err != nil {
				slog.Warn("api: story watchers", "err", err)
			}
			to = append(to, w...)
		}
		a.deliverStory(st, true, to, except(ctx))
	}
	return ids, nil
}

func (a *API) togglePinnedStories(ctx context.Context, r *tg.StoriesTogglePinnedRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.ownStories(ctx, me, r.Peer); err != nil {
		return nil, err
	}
	list, err := a.stories.SetPinned(ctx, me.ID, r.ID, r.Pinned)
	if err != nil {
		return nil, err
	}
	ids := []int{}
	for _, st := range list {
		ids = append(ids, st.ID)
		a.deliverStory(st, false, []int64{me.ID}, except(ctx))
	}
	return ids, nil
}

func (a *API) togglePinnedToTop(ctx context.Context, r *tg.StoriesTogglePinnedToTopRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.ownStories(ctx, me, r.Peer); err != nil {
		return nil, err
	}
	return true, a.stories.SetPinnedTop(ctx, me.ID, r.ID)
}

// feedPage is how many peers one getAllStories answer carries.
const feedPage = 20

// getAllStories is the feed: the caller first, then people with unread
// stories, then the rest, newest first within each. state is a digest of
// the whole feed plus where the next page starts.
func (a *API) getAllStories(ctx context.Context, r *tg.StoriesGetAllStoriesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	feed, err := a.stories.Feed(ctx, me.ID, r.Hidden)
	if err != nil {
		return nil, err
	}
	owners := make([]int64, len(feed))
	for i, p := range feed {
		owners[i] = p.Owner
	}
	reads, err := a.stories.ReadMarks(ctx, me.ID, owners)
	if err != nil {
		return nil, err
	}
	rank := func(p stories.FeedPeer) int {
		switch {
		case p.Owner == me.ID:
			return 0
		case p.MaxID > reads[p.Owner]:
			return 1
		}
		return 2
	}
	slices.SortStableFunc(feed, func(x, y stories.FeedPeer) int { return rank(x) - rank(y) })

	h := fnv.New64a()
	fmt.Fprint(h, r.Hidden)
	for _, p := range feed {
		fmt.Fprint(h, ",", p.Owner, ":", p.MaxID, ":", p.LastDate, ":", reads[p.Owner])
	}
	digest := strconv.FormatUint(h.Sum64(), 36)
	stealth := tg.StoriesStealthMode{}
	if r.State != "" && !r.Next && strings.HasPrefix(r.State, digest+".") {
		return &tg.StoriesAllStoriesNotModified{State: r.State, StealthMode: stealth}, nil
	}
	start := 0
	if r.Next && r.State != "" {
		if _, at, ok := strings.Cut(r.State, "."); ok {
			start, _ = strconv.Atoi(at)
		}
	}
	start = min(max(start, 0), len(feed))
	end := min(start+feedPage, len(feed))
	page := feed[start:end]

	owners = owners[:0]
	for _, p := range page {
		owners = append(owners, p.Owner)
	}
	active, err := a.stories.Active(ctx, me.ID, owners)
	if err != nil {
		return nil, err
	}
	out := &tg.StoriesAllStories{HasMore: end < len(feed), Count: len(feed), State: digest + "." + strconv.Itoa(end),
		PeerStories: []tg.PeerStories{}, Chats: []tg.ChatClass{}, StealthMode: stealth}
	users := []int64{}
	for _, p := range page {
		list := active[p.Owner]
		if len(list) == 0 {
			continue
		}
		items, mentioned, err := a.storyItems(ctx, me.ID, p.Owner, list)
		if err != nil {
			return nil, err
		}
		ps := tg.PeerStories{Peer: userPeerTL(p.Owner), Stories: items}
		if m := reads[p.Owner]; m > 0 {
			ps.SetMaxReadID(m)
		}
		out.PeerStories = append(out.PeerStories, ps)
		users = append(append(users, p.Owner), mentioned...)
	}
	if out.Users, err = a.usersFor(ctx, me.ID, users); err != nil {
		return nil, err
	}
	return out, nil
}

// peerStories is one owner's live stories as the caller sees them.
func (a *API) peerStories(ctx context.Context, me account.User, owner int64) (tg.PeerStories, []int64, error) {
	ps := tg.PeerStories{Peer: userPeerTL(owner), Stories: []tg.StoryItemClass{}}
	active, err := a.stories.Active(ctx, me.ID, []int64{owner})
	if err != nil {
		return ps, nil, err
	}
	items, mentioned, err := a.storyItems(ctx, me.ID, owner, active[owner])
	if err != nil {
		return ps, nil, err
	}
	ps.Stories = items
	reads, err := a.stories.ReadMarks(ctx, me.ID, []int64{owner})
	if err != nil {
		return ps, nil, err
	}
	if m := reads[owner]; m > 0 {
		ps.SetMaxReadID(m)
	}
	return ps, mentioned, nil
}

func (a *API) getPeerStories(ctx context.Context, r *tg.StoriesGetPeerStoriesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := a.storyPeer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	out := &tg.StoriesPeerStories{Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
	if owner == 0 {
		out.Stories = tg.PeerStories{Peer: inputPeerTL(r.Peer), Stories: []tg.StoryItemClass{}}
		return out, nil
	}
	ps, mentioned, err := a.peerStories(ctx, me, owner)
	if err != nil {
		return nil, err
	}
	out.Stories = ps
	if out.Users, err = a.usersFor(ctx, me.ID, append(mentioned, owner)); err != nil {
		return nil, err
	}
	return out, nil
}

// inputPeerTL is the peer an input peer names, for answers about chats.
func inputPeerTL(p tg.InputPeerClass) tg.PeerClass {
	switch v := p.(type) {
	case *tg.InputPeerChannel:
		return &tg.PeerChannel{ChannelID: v.ChannelID}
	case *tg.InputPeerChannelFromMessage:
		return &tg.PeerChannel{ChannelID: v.ChannelID}
	case *tg.InputPeerChat:
		return &tg.PeerChat{ChatID: v.ChatID}
	}
	return &tg.PeerUser{}
}

func (a *API) getPeerMaxIDs(ctx context.Context, r *tg.StoriesGetPeerMaxIDsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	owners := make([]int64, len(r.ID))
	for i, p := range r.ID {
		owners[i], _ = a.storyPeer(ctx, me, p)
	}
	badges, err := a.stories.Badges(ctx, me.ID, owners)
	if err != nil {
		return nil, err
	}
	out := make([]tg.RecentStory, len(owners))
	for i, o := range owners {
		if m := badges[o].MaxID; o != 0 && m > 0 {
			out[i].SetMaxID(m)
		}
	}
	return out, nil
}

// storyList answers the list calls (by id, pinned, archive).
func (a *API) storyList(ctx context.Context, me account.User, owner int64, list []stories.Story, count int, top []int) (*tg.StoriesStories, error) {
	items, mentioned, err := a.storyItems(ctx, me.ID, owner, list)
	if err != nil {
		return nil, err
	}
	out := &tg.StoriesStories{Count: count, Stories: items, Chats: []tg.ChatClass{}}
	if len(top) > 0 {
		out.SetPinnedToTop(top)
	}
	if out.Users, err = a.usersFor(ctx, me.ID, append(mentioned, owner)); err != nil {
		return nil, err
	}
	return out, nil
}

func emptyStories() *tg.StoriesStories {
	return &tg.StoriesStories{Stories: []tg.StoryItemClass{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
}

func (a *API) getStoriesByID(ctx context.Context, r *tg.StoriesGetStoriesByIDRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := a.storyPeer(ctx, me, r.Peer)
	if err != nil || owner == 0 {
		return emptyStories(), err
	}
	list, err := a.stories.ByID(ctx, me.ID, owner, r.ID)
	if err != nil {
		return nil, err
	}
	return a.storyList(ctx, me, owner, list, len(list), nil)
}

func (a *API) getPinnedStories(ctx context.Context, r *tg.StoriesGetPinnedStoriesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := a.storyPeer(ctx, me, r.Peer)
	if err != nil || owner == 0 {
		return emptyStories(), err
	}
	list, total, top, err := a.stories.Pinned(ctx, me.ID, owner, r.OffsetID, r.Limit)
	if err != nil {
		return nil, err
	}
	return a.storyList(ctx, me, owner, list, total, top)
}

func (a *API) getStoriesArchive(ctx context.Context, r *tg.StoriesGetStoriesArchiveRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.ownStories(ctx, me, r.Peer); err != nil {
		return emptyStories(), nil
	}
	list, total, err := a.stories.Archive(ctx, me.ID, r.OffsetID, r.Limit)
	if err != nil {
		return nil, err
	}
	return a.storyList(ctx, me, me.ID, list, total, nil)
}

func (a *API) readStories(ctx context.Context, r *tg.StoriesReadStoriesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := a.storyPeer(ctx, me, r.Peer)
	if err != nil || owner == 0 {
		return []int{}, err
	}
	ids, err := a.stories.Read(ctx, me.ID, owner, r.MaxID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []int{}, nil
	}
	users, err := a.usersFor(ctx, me.ID, []int64{owner})
	if err != nil {
		return nil, err
	}
	a.echo(ctx, me, []tg.UpdateClass{&tg.UpdateReadStories{Peer: userPeerTL(owner), MaxID: ids[len(ids)-1]}}, users)
	return ids, nil
}

func (a *API) incrementStoryViews(ctx context.Context, r *tg.StoriesIncrementStoryViewsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := a.storyPeer(ctx, me, r.Peer)
	if err != nil || owner == 0 {
		return true, err
	}
	if len(r.ID) > 200 {
		return nil, tgerr.New(400, "STORY_ID_INVALID")
	}
	return true, a.stories.View(ctx, me.ID, owner, r.ID)
}

// storyViewsTL is a story's numbers as getStoriesViews gives them.
func storyViewsTL(c *stories.Counts) tg.StoryViews {
	v := tg.StoryViews{}
	if c == nil {
		return v
	}
	v.HasViewers = c.Views > 0
	v.ViewsCount = c.Views
	v.SetReactionsCount(c.Reactions)
	for _, rc := range c.ByReaction {
		v.Reactions = append(v.Reactions, tg.ReactionCount{Reaction: rc.Reaction, Count: rc.Count})
	}
	for _, u := range c.Recent {
		v.RecentViewers = append(v.RecentViewers, clientID(u))
	}
	return v
}

func (a *API) getStoriesViews(ctx context.Context, r *tg.StoriesGetStoriesViewsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	out := &tg.StoriesStoryViews{Views: make([]tg.StoryViews, len(r.ID)), Users: []tg.UserClass{}}
	if err := a.ownStories(ctx, me, r.Peer); err != nil {
		// views are the author's
		return out, nil
	}
	counts, err := a.stories.Counts(ctx, me.ID, r.ID)
	if err != nil {
		return nil, err
	}
	var users []int64
	for i, id := range r.ID {
		c := counts[id]
		out.Views[i] = storyViewsTL(c)
		if c != nil {
			users = append(users, c.Recent...)
		}
	}
	if out.Users, err = a.usersFor(ctx, me.ID, users); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *API) getStoryViewsList(ctx context.Context, r *tg.StoriesGetStoryViewsListRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	out := &tg.StoriesStoryViewsList{Views: []tg.StoryViewClass{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
	if err := a.ownStories(ctx, me, r.Peer); err != nil {
		return out, nil
	}
	if _, err := a.stories.One(ctx, me.ID, me.ID, r.ID); err != nil {
		return nil, err
	}
	offset, _ := strconv.Atoi(r.Offset)
	list, total, err := a.stories.Viewers(ctx, me.ID, r.ID, stories.ViewersQuery{
		Q: r.Q, JustContacts: r.JustContacts, ReactionsFirst: r.ReactionsFirst, Offset: offset, Limit: r.Limit})
	if err != nil {
		return nil, err
	}
	counts, err := a.stories.Counts(ctx, me.ID, []int{r.ID})
	if err != nil {
		return nil, err
	}
	if c := counts[r.ID]; c != nil {
		out.ViewsCount, out.ReactionsCount = c.Views, c.Reactions
	}
	out.Count = total
	var users []int64
	for _, v := range list {
		sv := &tg.StoryView{Blocked: v.Blocked, BlockedMyStoriesFrom: v.BlockedFrom, UserID: clientID(v.User), Date: v.Date}
		if v.Reaction != nil {
			sv.SetReaction(v.Reaction)
		}
		out.Views = append(out.Views, sv)
		users = append(users, v.User)
	}
	if next := offset + len(list); next < total && len(list) > 0 {
		out.SetNextOffset(strconv.Itoa(next))
	}
	if out.Users, err = a.usersFor(ctx, me.ID, users); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *API) sendStoryReaction(ctx context.Context, r *tg.StoriesSendReactionRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := a.storyPeer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if owner == 0 {
		return nil, errPeerInvalid
	}
	if err := a.stories.React(ctx, me.ID, owner, r.StoryID, r.Reaction); err != nil {
		return nil, err
	}
	users, err := a.usersFor(ctx, me.ID, []int64{owner})
	if err != nil {
		return nil, err
	}
	reaction := func() tg.ReactionClass {
		switch v := r.Reaction.(type) {
		case *tg.ReactionEmoji:
			return &tg.ReactionEmoji{Emoticon: v.Emoticon}
		case *tg.ReactionCustomEmoji:
			return &tg.ReactionCustomEmoji{DocumentID: v.DocumentID}
		}
		return &tg.ReactionEmpty{}
	}
	up := func() tg.UpdateClass {
		return &tg.UpdateSentStoryReaction{Peer: userPeerTL(owner), StoryID: r.StoryID, Reaction: reaction()}
	}
	echoUsers, err := a.usersFor(ctx, me.ID, []int64{owner})
	if err != nil {
		return nil, err
	}
	a.echo(ctx, me, []tg.UpdateClass{up()}, echoUsers)
	return &tg.Updates{Updates: []tg.UpdateClass{up()}, Users: users, Chats: []tg.ChatClass{}, Date: now()}, nil
}

func (a *API) togglePeerStoriesHidden(ctx context.Context, r *tg.StoriesTogglePeerStoriesHiddenRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := a.storyPeer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if owner == 0 || owner == me.ID {
		return nil, errPeerInvalid
	}
	return true, a.stories.SetHidden(ctx, me.ID, owner, r.Hidden)
}

func (a *API) getAllReadPeerStories(ctx context.Context, _ *tg.StoriesGetAllReadPeerStoriesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	reads, err := a.stories.ReadMarks(ctx, me.ID, nil)
	if err != nil {
		return nil, err
	}
	out := emptyUpdates()
	var owners []int64
	for o, m := range reads {
		out.Updates = append(out.Updates, &tg.UpdateReadStories{Peer: userPeerTL(o), MaxID: m})
		owners = append(owners, o)
	}
	if out.Users, err = a.usersFor(ctx, me.ID, owners); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *API) canSendStory(ctx context.Context, r *tg.StoriesCanSendStoryRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if err := a.ownStories(ctx, me, r.Peer); err != nil {
		return nil, err
	}
	active, err := a.stories.Active(ctx, me.ID, []int64{me.ID})
	if err != nil {
		return nil, err
	}
	left := stories.MaxActive - len(active[me.ID])
	if left <= 0 {
		return nil, tgerr.New(400, "STORIES_TOO_MUCH")
	}
	return &tg.StoriesCanSendStoryCount{CountRemains: left}, nil
}

func (a *API) exportStoryLink(ctx context.Context, r *tg.StoriesExportStoryLinkRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := a.storyPeer(ctx, me, r.Peer)
	if err != nil || owner == 0 {
		return nil, errPeerInvalid
	}
	if _, err := a.stories.One(ctx, me.ID, owner, r.ID); err != nil {
		return nil, err
	}
	users, err := a.accounts.Users(ctx, []int64{owner})
	if err != nil {
		return nil, err
	}
	u, ok := users[owner]
	if !ok || u.Username == "" || !u.UsernamePublic {
		return nil, tgerr.New(400, "USER_PUBLIC_MISSING")
	}
	return &tg.ExportedStoryLink{Link: fmt.Sprintf("%s/%s/s/%d", strings.TrimRight(a.in.BaseURL, "/"), u.Username, r.ID)}, nil
}

// editCloseFriends replaces the list; the users' close_friend flags change
// in the next user objects the caller gets.
func (a *API) editCloseFriends(ctx context.Context, r *tg.ContactsEditCloseFriendsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if len(r.ID) > 1000 {
		return nil, tgerr.New(400, "USER_ID_INVALID")
	}
	ids := make([]int64, 0, len(r.ID))
	for _, id := range r.ID {
		ids = append(ids, serverID(id))
	}
	added, removed, err := a.stories.SetCloseFriends(ctx, me.ID, ids)
	if err != nil {
		return nil, err
	}
	// a live close-friends story appears or goes for the people changed
	if len(added)+len(removed) > 0 {
		active, err := a.stories.Active(ctx, me.ID, []int64{me.ID})
		if err != nil {
			return nil, err
		}
		for _, st := range active[me.ID] {
			if st.Rules.Audience != stories.CloseFriends {
				continue
			}
			a.deliverStory(st, false, added, except(ctx))
			a.deliverStory(st, true, removed, except(ctx))
		}
	}
	return true, nil
}

// storiesBadges sets what user objects say about stories: the newest live
// one the viewer may see (the ring), close friend and hidden.
func (a *API) storiesBadges(ctx context.Context, viewer int64, list []tg.UserClass) error {
	if a.stories == nil || len(list) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(list))
	for _, u := range list {
		if t, ok := u.(*tg.User); ok {
			ids = append(ids, serverID(t.ID))
		}
	}
	badges, err := a.stories.Badges(ctx, viewer, ids)
	if err != nil {
		return err
	}
	for _, u := range list {
		t, ok := u.(*tg.User)
		if !ok {
			continue
		}
		b, ok := badges[serverID(t.ID)]
		if !ok {
			continue
		}
		if b.MaxID > 0 {
			t.StoriesMaxID = tg.RecentStory{MaxID: b.MaxID}
		}
		t.CloseFriend = b.CloseFriend
		t.StoriesHidden = b.Hidden
	}
	return nil
}

// fullUserStories fills getFullUser's stories part.
func (a *API) fullUserStories(ctx context.Context, me account.User, owner int64, full *tg.UserFull) ([]int64, error) {
	if a.stories == nil {
		return nil, nil
	}
	ps, mentioned, err := a.peerStories(ctx, me, owner)
	if err != nil {
		return nil, err
	}
	if len(ps.Stories) > 0 {
		full.SetStories(ps)
	}
	if full.StoriesPinnedAvailable, err = a.stories.HasPinned(ctx, me.ID, owner); err != nil {
		return nil, err
	}
	if owner != me.ID {
		if full.BlockedMyStoriesFrom, err = a.stories.IsBlocked(ctx, me.ID, owner); err != nil {
			return nil, err
		}
	}
	return mentioned, nil
}

// setStoriesBlocked is contacts.block/unblock with my_stories_from: the
// "hide my stories from" list, apart from the real block list.
func (a *API) setStoriesBlocked(ctx context.Context, me account.User, in tg.InputPeerClass, block bool) (any, error) {
	if a.stories == nil {
		return true, nil
	}
	id, err := a.peerUser(ctx, me, in)
	if err != nil {
		return nil, err
	}
	if id == me.ID {
		return nil, tgerr.New(400, "CONTACT_ID_INVALID")
	}
	changed, err := a.stories.SetBlocked(ctx, me.ID, id, block)
	if err != nil {
		return nil, err
	}
	if changed {
		users, err := a.usersFor(ctx, me.ID, []int64{id})
		if err != nil {
			return nil, err
		}
		a.echo(ctx, me, []tg.UpdateClass{&tg.UpdatePeerBlocked{BlockedMyStoriesFrom: block, PeerID: userPeerTL(id)}}, users)
	}
	return true, nil
}

func (a *API) storiesBlockedList(ctx context.Context, me account.User, offset, limit int) (any, error) {
	empty := &tg.ContactsBlocked{Blocked: []tg.PeerBlocked{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
	if a.stories == nil {
		return empty, nil
	}
	list, total, err := a.stories.BlockedList(ctx, me.ID, offset, limit)
	if err != nil {
		return nil, err
	}
	var who []int64
	for _, b := range list {
		empty.Blocked = append(empty.Blocked, tg.PeerBlocked{PeerID: userPeerTL(b.User), Date: b.Date})
		who = append(who, b.User)
	}
	if empty.Users, err = a.usersFor(ctx, me.ID, who); err != nil {
		return nil, err
	}
	if offset <= 0 && len(list) == total {
		return empty, nil
	}
	return &tg.ContactsBlockedSlice{Count: total, Blocked: empty.Blocked, Chats: empty.Chats, Users: empty.Users}, nil
}

// storiesAppConfig are the app config keys the apps read before they offer
// posting a story.
func storiesAppConfig() []tg.JSONObjectValue {
	num := func(k string, v int) tg.JSONObjectValue {
		return tg.JSONObjectValue{Key: k, Value: &tg.JSONNumber{Value: float64(v)}}
	}
	str := func(k, v string) tg.JSONObjectValue {
		return tg.JSONObjectValue{Key: k, Value: &tg.JSONString{Value: v}}
	}
	return []tg.JSONObjectValue{
		str("stories_posting", "enabled"),
		str("stories_entities", "enabled"),
		num("story_expiring_limit_default", stories.MaxActive),
		num("story_expiring_limit_premium", stories.MaxActive),
		num("story_caption_length_limit_default", storyCaptionMax),
		num("story_caption_length_limit_premium", storyCaptionMaxPremium),
		num("stories_sent_weekly_limit_default", 700),
		num("stories_sent_weekly_limit_premium", 700),
		num("stories_sent_monthly_limit_default", 3000),
		num("stories_sent_monthly_limit_premium", 3000),
		num("stories_pinned_to_top_count_max", stories.MaxPinnedTop),
		num("story_expire_period", day),
		num("story_viewers_expire_period", day),
	}
}
