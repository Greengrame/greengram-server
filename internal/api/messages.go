package api

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/messages"
)

func (a *API) registerMessages() {
	handle(a, tlprofile.SemanticMethodMessagesSendMessage, a.sendMessage)
	handle(a, tlprofile.SemanticMethodMessagesForwardMessages, a.forwardMessages)
	handle(a, tlprofile.SemanticMethodMessagesEditMessage, a.editMessage)
	handle(a, tlprofile.SemanticMethodMessagesDeleteMessages, a.deleteMessages)
	handle(a, tlprofile.SemanticMethodMessagesReadHistory, a.readHistory)
	handle(a, tlprofile.SemanticMethodMessagesSetTyping, a.setTyping)
	handle(a, tlprofile.SemanticMethodMessagesGetDialogs, a.getDialogs)
	handle(a, tlprofile.SemanticMethodMessagesGetPeerDialogs, a.getPeerDialogs)
	handle(a, tlprofile.SemanticMethodMessagesGetPinnedDialogs, a.getPinnedDialogs)
	handle(a, tlprofile.SemanticMethodMessagesToggleDialogPin, a.toggleDialogPin)
	handle(a, tlprofile.SemanticMethodMessagesGetHistory, a.getHistory)
	handle(a, tlprofile.SemanticMethodMessagesGetMessages, a.getMessages)
	handle(a, tlprofile.SemanticMethodMessagesSearch, a.search)
}

func now() int { return int(time.Now().Unix()) }

// sentUpdates is what the sender's other sessions get: the same journal
// entries, minus the UpdateMessageID only the sending session needs.
func (a *API) echo(ctx context.Context, me account.User, list []tg.UpdateClass, users []tg.UserClass) {
	a.echoChats(ctx, me, list, users, []tg.ChatClass{})
}

// The caller's reply holds the same objects, and td's Encode writes the
// flags field while it encodes, so the reply and this push can't share
// them: the push gets its own copy, taken here before the reply goes out.
func (a *API) echoChats(ctx context.Context, me account.User, list []tg.UpdateClass, users []tg.UserClass, chats []tg.ChatClass) {
	skip := except(ctx)
	up, err := copyUpdates(&tg.Updates{Updates: list, Users: users, Chats: chats, Date: now()})
	if err != nil {
		slog.Warn("api: echo copy", "err", err)
		return
	}
	a.out.add(me.ID, func(ctx context.Context) { a.push(ctx, me.ID, up, skip) })
}

func copyUpdates(up *tg.Updates) (*tg.Updates, error) {
	var b bin.Buffer
	if err := up.Encode(&b); err != nil {
		return nil, err
	}
	out := &tg.Updates{}
	return out, out.Decode(&b)
}

func except(ctx context.Context) edge.Session {
	if c := edge.CallFrom(ctx); c != nil {
		return c.Session()
	}
	return edge.Session{}
}

func replyID(r tg.InputReplyToClass) int {
	if v, ok := r.(*tg.InputReplyToMessage); ok {
		return v.ReplyToMsgID
	}
	return 0
}

// replyTopic is the forum topic a send names, 0 for none.
func replyTopic(r tg.InputReplyToClass) int {
	if v, ok := r.(*tg.InputReplyToMessage); ok {
		return v.TopMsgID
	}
	return 0
}

func (a *API) sendMessage(ctx context.Context, r *tg.MessagesSendMessageRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	reply, topic := 0, 0
	if r.ReplyTo != nil {
		reply, topic = replyID(r.ReplyTo), replyTopic(r.ReplyTo)
	}
	o := outgoing{text: r.Message, entities: r.Entities, replyTo: reply, topic: topic, randomID: r.RandomID, silent: r.Silent, noWebpage: r.NoWebpage}
	if in, ok := r.GetRichMessage(); ok {
		if o.rich, o.text, err = a.richFromInput(ctx, me, in); err != nil {
			return nil, err
		}
		o.entities = nil
		// the scheduled table has no article column: fail rather than send plain words later
		if r.ScheduleDate > now() {
			return nil, tgerr.New(400, "SCHEDULE_DATE_INVALID")
		}
		o.noWebpage = true
	}
	a.presence.set(me.ID, true)
	out, err := a.sendOrSchedule(ctx, me, to, []outgoing{o},
		r.ScheduleDate, r.ScheduleRepeatPeriod)
	if err == nil && r.ClearDraft {
		a.dropSentDraft(ctx, me, to, r.ReplyTo)
	}
	return out, err
}

// richFromInput takes an article in. Writing them is a Premium feature;
// reading them and forwarding them isn't.
func (a *API) richFromInput(ctx context.Context, me account.User, in tg.InputRichMessageClass) ([]byte, string, error) {
	if !me.Premium(time.Now()) {
		return nil, "", tgerr.New(403, "PREMIUM_ACCOUNT_REQUIRED")
	}
	return a.media.RichFromInput(ctx, in)
}

// outgoing is one message to send: typed, or a forward of an earlier one.
type outgoing struct {
	text     string
	entities []tg.MessageEntityClass
	replyTo  int
	topic    int // forum topic (top_msg_id); channels only
	randomID int64
	fwd      messages.Origin
	media    media.Ref
	grouped  int64 // album
	silent   bool
	// no link preview
	noWebpage bool
	rich     []byte
	// an inline result: the bot it came through, and the bot's keyboard
	via    int64
	markup tg.ReplyMarkupClass
}

// mayWrite is the check sendTo's writes make, for callers that take an
// upload in first and shouldn't before it's known the message may go.
func (a *API) mayWrite(ctx context.Context, me account.User, to peer, withMedia bool) error {
	if to.channel != 0 {
		return a.chans.CanPost(ctx, me.ID, to.channel, withMedia)
	}
	_, err := a.sendDropped(ctx, me.ID, to.user)
	return err
}

// sendTo writes messages to a private chat or a channel and answers like
// Telegram: UpdateMessageID per random_id, then the new-message updates.
func (a *API) sendTo(ctx context.Context, me account.User, to peer, list []outgoing) (*tg.Updates, error) {
	out := &tg.Updates{Updates: []tg.UpdateClass{}, Chats: []tg.ChatClass{}, Date: now()}
	var dropped bool
	if to.channel == 0 {
		var err error
		if dropped, err = a.sendDropped(ctx, me.ID, to.user); err != nil {
			return nil, err
		}
	}
	var mine []tg.UpdateClass
	var users, chans []int64
	var chEvents []channels.Event
	var events []messages.Event
	var later []pendingPreview
	for _, o := range list {
		var named []int64
		if o.fwd.Empty() {
			o.entities, named = a.mentionsIn(ctx, me, o.text, o.entities)
		}
		preview, fetch := a.cachedPreview(ctx, o)
		if !preview.Empty() {
			o.media = preview
		}
		if to.channel != 0 {
			s, err := a.chans.Send(ctx, me.ID, to.channel, channels.Draft{Text: o.text, Entities: o.entities, ReplyTo: o.replyTo, RandomID: o.randomID, Fwd: o.fwd,
				Media: o.media, Grouped: o.grouped, TopMsgID: o.topic, Silent: o.silent, Mentions: named, Rich: o.rich, ViaBot: o.via, ReplyMarkup: o.markup})
			if err != nil {
				return nil, err
			}
			out.Updates = append(out.Updates, &tg.UpdateMessageID{ID: s.Msg.ID, RandomID: o.randomID})
			mine = append(mine, channels.SetOut(s.Update, me.ID))
			if fetch != "" {
				later = append(later, pendingPreview{url: fetch, channel: to.channel, id: s.Msg.ID})
			}
			us, cs := mentioned(s.Msg.TL())
			users, chans = append(users, us...), append(chans, cs...)
			chEvents = append(chEvents, s.Events...)
			continue
		}
		s, err := a.msgs.Send(ctx, me.ID, to.user, messages.Draft{Text: o.text, Entities: o.entities, ReplyTo: o.replyTo, RandomID: o.randomID, Fwd: o.fwd,
			Media: o.media, Grouped: o.grouped, Silent: o.silent, Rich: o.rich, SenderOnly: dropped, ViaBot: o.via, ReplyMarkup: o.markup})
		if err != nil {
			return nil, err
		}
		out.Updates = append(out.Updates, &tg.UpdateMessageID{ID: s.Msg.ID, RandomID: o.randomID})
		mine = append(mine, s.Update)
		if fetch != "" {
			later = append(later, pendingPreview{url: fetch, user: me.ID, id: s.Msg.ID})
		}
		users = append(users, messages.Users([]messages.Msg{s.Msg})...)
		if s.Msg.Fwd.Chan != 0 {
			chans = append(chans, s.Msg.Fwd.Chan)
		}
		events = append(events, s.Events...)
	}
	users = append(users, a.ownPolls(ctx, me.ID, mine)...)
	out.Updates = append(out.Updates, mine...)
	var err error
	if out.Users, err = a.usersFor(ctx, me.ID, users); err != nil {
		return nil, err
	}
	if to.channel != 0 {
		chans = append(chans, to.channel)
	}
	if out.Chats, err = a.chatsFor(ctx, me.ID, chans); err != nil {
		return nil, err
	}
	// after the pushes above are queued, so the edit comes after the message
	defer a.attachLater(later)
	if to.channel != 0 {
		// members, including the sender's other sessions, get it from the events
		a.deliverChannel(chEvents, except(ctx))
		return out, nil
	}
	a.echoChats(ctx, me, mine, out.Users, out.Chats)
	a.deliver(events, except(ctx))
	a.botReads(ctx, me.ID, to.user)
	return out, nil
}

// botReads marks a message to a bot read right away: a bot has no chat to open,
// so the sender would sit on one tick forever. Groups stay as they are, there a
// bot doesn't count as someone seeing it.
func (a *API) botReads(ctx context.Context, sender, peer int64) {
	users, err := a.accounts.Users(ctx, []int64{peer})
	if err != nil || !users[peer].IsBot {
		return
	}
	_, events, err := a.msgs.ReadHistory(ctx, peer, sender, 0)
	if err != nil {
		slog.Warn("api: bot read", "err", err)
		return
	}
	a.deliver(events, edge.Session{})
}

// origins are the forward headers for messages me forwards, by author:
// the author's link, or only their name where their forwards privacy
// doesn't let me add it.
func (a *API) origins(ctx context.Context, me int64, authors []int64) (map[int64]messages.Origin, error) {
	out := map[int64]messages.Origin{}
	au, err := a.audienceFor(ctx, me, authors, "forwards")
	if err != nil {
		return nil, err
	}
	var hidden []int64
	for _, id := range authors {
		if au.can(id, "forwards") {
			out[id] = messages.Origin{User: id}
		} else {
			hidden = append(hidden, id)
		}
	}
	if len(hidden) == 0 {
		return out, nil
	}
	users, err := a.accounts.Users(ctx, hidden)
	if err != nil {
		return nil, err
	}
	for _, id := range hidden {
		name := "Deleted Account"
		if u, ok := users[id]; ok {
			name = u.Name
		}
		out[id] = messages.Origin{Name: name}
	}
	return out, nil
}

func (a *API) forwardMessages(ctx context.Context, r *tg.MessagesForwardMessagesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.ToPeer)
	if err != nil {
		return nil, err
	}
	if len(r.ID) == 0 || len(r.ID) > 100 || len(r.RandomID) != len(r.ID) {
		return nil, tgerr.New(400, "RANDOM_ID_INVALID")
	}
	type orig struct {
		text     string
		entities []tg.MessageEntityClass
		author   int64 // a person's message that isn't a forward yet
		fwd      messages.Origin
		media    media.Ref
		grouped  int64
		rich     []byte
	}
	byID := map[int]orig{}
	var authors []int64
	// a private message id names the message in the caller's box, so a
	// from_peer of inputPeerEmpty (the Android app) still works
	from, _ := a.peer(ctx, me, r.FromPeer)
	if from.channel != 0 {
		if _, _, err := a.chans.Access(ctx, from.channel, me.ID); err != nil {
			return nil, err
		}
		list, err := a.chans.Messages(ctx, from.channel, r.ID)
		if err != nil {
			return nil, err
		}
		for _, m := range list {
			if m.Action != nil {
				continue
			}
			o := orig{text: m.Text, entities: m.Entities, fwd: m.Fwd, media: m.Media, grouped: m.Grouped, rich: m.Rich}
			// a post speaks as the channel; from_id is the admin who
			// wrote it and stays secret
			switch {
			case !m.Fwd.Empty():
			case m.FwdChanID != 0:
				o.fwd = messages.Origin{Chan: m.FwdChanID, Post: m.FwdChanMs, Date: m.Date}
			case m.Post:
				o.fwd = messages.Origin{Chan: from.channel, Post: m.ID, Date: m.Date}
			default:
				o.author, o.fwd.Date = m.From, m.Date
				authors = append(authors, m.From)
			}
			byID[m.ID] = o
		}
	} else {
		list, err := a.msgs.Get(ctx, me.ID, r.ID)
		if err != nil {
			return nil, err
		}
		for _, m := range list {
			if m.Action != nil {
				continue
			}
			o := orig{text: m.Text, entities: m.Entities, fwd: m.Fwd, media: m.Media, grouped: m.Grouped, rich: m.Rich}
			if m.Fwd.Empty() {
				o.author, o.fwd.Date = m.From, m.Date
				authors = append(authors, m.From)
			}
			byID[m.ID] = o
		}
	}
	heads, err := a.origins(ctx, me.ID, authors)
	if err != nil {
		return nil, err
	}
	var list []outgoing
	// a forwarded album stays an album, under a new id
	albums := map[int64]int64{}
	for i, id := range r.ID {
		m, ok := byID[id]
		if !ok {
			continue
		}
		o := outgoing{text: m.text, entities: m.entities, randomID: r.RandomID[i], fwd: m.fwd, media: m.media, topic: r.TopMsgID, rich: m.rich}
		if m.author != 0 {
			o.fwd = heads[m.author]
			o.fwd.Date = m.fwd.Date
		}
		if m.grouped != 0 {
			if albums[m.grouped] == 0 {
				albums[m.grouped] = groupedID()
			}
			o.grouped = albums[m.grouped]
		}
		if r.DropAuthor {
			o.fwd = messages.Origin{}
		}
		list = append(list, o)
	}
	if len(list) == 0 {
		return nil, tgerr.New(400, "MESSAGE_ID_INVALID")
	}
	return a.sendOrSchedule(ctx, me, to, list, r.ScheduleDate, r.ScheduleRepeatPeriod)
}

func (a *API) editMessage(ctx context.Context, r *tg.MessagesEditMessageRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if date, ok := r.GetScheduleDate(); ok && date != 0 {
		return a.editScheduled(ctx, me, to, r, date)
	}
	if in, ok := r.GetMedia(); ok && a.media != nil {
		return a.editMedia(ctx, me, to, r, in)
	}
	text, ok := r.GetMessage()
	var rich []byte
	entities := r.Entities
	if in, isRich := r.GetRichMessage(); isRich {
		if rich, text, err = a.richFromInput(ctx, me, in); err != nil {
			return nil, err
		}
		ok, entities = true, nil
	}
	if !ok {
		return nil, tgerr.New(400, "MESSAGE_NOT_MODIFIED")
	}
	r.Entities, _ = a.mentionsIn(ctx, me, text, r.Entities)
	if to.channel != 0 {
		_, u, events, err := a.chans.Edit(ctx, me.ID, to.channel, r.ID, text, entities, rich)
		if err != nil {
			return nil, err
		}
		up, err := a.opUpdates(ctx, me, to.channel, events)
		if err != nil {
			return nil, err
		}
		if len(up.Updates) == 0 {
			up.Updates = []tg.UpdateClass{channels.SetOut(u, me.ID)}
		}
		return up, nil
	}
	peer := to.user
	m, u, events, err := a.msgs.Edit(ctx, me.ID, peer, r.ID, text, entities, rich)
	if err != nil {
		return nil, err
	}
	users, err := a.usersFor(ctx, me.ID, messages.Users([]messages.Msg{m}))
	if err != nil {
		return nil, err
	}
	a.echo(ctx, me, []tg.UpdateClass{u}, users)
	a.deliver(events, except(ctx))
	return &tg.Updates{Updates: []tg.UpdateClass{u}, Users: users, Chats: []tg.ChatClass{}, Date: now()}, nil
}

func (a *API) deleteMessages(ctx context.Context, r *tg.MessagesDeleteMessagesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	res, events, err := a.msgs.Delete(ctx, me.ID, r.ID, r.Revoke)
	if err != nil {
		return nil, err
	}
	a.deliver(events, except(ctx))
	return &res, nil
}

func (a *API) readHistory(ctx context.Context, r *tg.MessagesReadHistoryRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if to.channel != 0 {
		_, err := a.channelReadHistory(ctx, &tg.ChannelsReadHistoryRequest{Channel: &tg.InputChannel{ChannelID: to.channel}, MaxID: r.MaxID})
		return &tg.MessagesAffectedMessages{}, err
	}
	peer := to.user
	res, events, err := a.msgs.ReadHistory(ctx, me.ID, peer, r.MaxID)
	if err != nil {
		return nil, err
	}
	a.presence.set(me.ID, true)
	a.deliver(events, except(ctx))
	return &res, nil
}

func (a *API) setTyping(ctx context.Context, r *tg.MessagesSetTypingRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if to.channel != 0 {
		c, m, err := a.chans.Access(ctx, to.channel, me.ID)
		if err != nil || !channels.InChat(m.Status) || !c.Megagroup {
			return true, nil
		}
		members, err := a.chans.MemberIDs(ctx, to.channel)
		if err != nil {
			return nil, err
		}
		var others []int64
		for _, u := range members {
			if u != me.ID {
				others = append(others, u)
			}
		}
		u := &tg.UpdateChannelUserTyping{ChannelID: to.channel, FromID: &tg.PeerUser{UserID: clientID(me.ID)}, Action: r.Action}
		// without it Web A shows the typing under General
		if top, ok := r.GetTopMsgID(); ok {
			u.SetTopMsgID(top)
		}
		a.deliverChannel([]channels.Event{{Users: others, Update: u}}, edge.Session{})
		return true, nil
	}
	peer := to.user
	if peer != me.ID && a.checkBlocked(ctx, me.ID, peer) == nil {
		// typing isn't journaled: it only matters right now
		a.deliver([]messages.Event{{UserID: peer, Update: &tg.UpdateUserTyping{UserID: clientID(me.ID), Action: r.Action}}}, edge.Session{})
	}
	return true, nil
}

// entry is one chat-list row, private or channel, ready to send.
type entry struct {
	date, top int
	pinned    bool
	folder    int
	unread    int
	dialog    tg.DialogClass
	msg       tg.MessageClass
}

func dialogTL(d messages.Dialog) tg.DialogClass {
	out := &tg.Dialog{
		Peer:            &tg.PeerUser{UserID: clientID(d.Peer)},
		TopMessage:      d.TopID,
		ReadInboxMaxID:  d.ReadInboxMax,
		ReadOutboxMaxID: d.ReadOutboxMax,
		UnreadCount:     d.Unread,
		NotifySettings:  tg.PeerNotifySettings{},
	}
	out.SetPinned(d.Pinned)
	if d.Folder != 0 {
		out.SetFolderID(d.Folder)
	}
	out.UnreadReactionsCount = d.UnreadReactions
	return out
}

func channelDialogTL(d channels.Dialog) tg.DialogClass {
	out := &tg.Dialog{
		Peer:            &tg.PeerChannel{ChannelID: d.Channel.ID},
		TopMessage:      d.Channel.TopID,
		ReadInboxMaxID:  d.Member.ReadInboxMax,
		ReadOutboxMaxID: d.Channel.ReadOutboxMax,
		UnreadCount:     d.Unread,
		NotifySettings:  tg.PeerNotifySettings{},
	}
	out.SetPinned(d.Member.Pinned)
	out.SetViewForumAsMessages(d.Member.ForumAsMessages)
	out.UnreadReactionsCount = d.UnreadReactions
	out.UnreadMentionsCount = d.UnreadMentions
	out.SetPts(d.Channel.Pts)
	if d.Folder != 0 {
		out.SetFolderID(d.Folder)
	}
	return out
}

// entries is the whole chat list, newest first; nil filters mean all.
func (a *API) entries(ctx context.Context, me account.User, wantUsers map[int64]bool, wantChans map[int64]bool) ([]entry, error) {
	var out []entry
	private, tops, err := a.msgs.AllDialogs(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	topByID := map[int]messages.Msg{}
	for _, m := range tops {
		topByID[m.ID] = m
	}
	for _, d := range private {
		if wantUsers != nil && !wantUsers[d.Peer] {
			continue
		}
		e := entry{date: d.TopDate, top: d.TopID, pinned: d.Pinned, folder: d.Folder, unread: d.Unread, dialog: dialogTL(d)}
		if m, ok := topByID[d.TopID]; ok {
			e.msg = m.TL()
		}
		out = append(out, e)
	}
	chans, err := a.chans.Dialogs(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	for _, d := range chans {
		if wantChans != nil && !wantChans[d.Channel.ID] {
			continue
		}
		e := entry{date: d.Channel.TopDate, top: d.Channel.TopID, pinned: d.Member.Pinned, folder: d.Folder, unread: d.Unread, dialog: channelDialogTL(d)}
		if d.Top != nil {
			e.msg = channels.MarkOut(d.Top.TL(), me.ID)
		}
		out = append(out, e)
	}
	notify, err := a.peerNotifyMap(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	for _, e := range out {
		d := e.dialog.(*tg.Dialog)
		var k peer
		switch p := d.Peer.(type) {
		case *tg.PeerUser:
			k.user = serverID(p.UserID)
		case *tg.PeerChannel:
			k.channel = p.ChannelID
		}
		if v, ok := notify[k]; ok {
			d.NotifySettings = v
		}
	}
	if err := a.attachDrafts(ctx, me, out); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].date != out[j].date {
			return out[i].date > out[j].date
		}
		return out[i].top > out[j].top
	})
	return out, nil
}

// attachDrafts puts the user's saved drafts on their dialogs.
func (a *API) attachDrafts(ctx context.Context, me account.User, out []entry) error {
	drafts, err := a.msgs.Drafts(ctx, me.ID)
	if err != nil {
		return err
	}
	if len(drafts) > 0 {
		byKey := map[messages.DraftKey]*tg.DraftMessage{}
		for i := range drafts {
			if drafts[i].Thread == 0 {
				byKey[drafts[i].DraftKey] = &drafts[i].Draft
			}
		}
		for _, e := range out {
			d := e.dialog.(*tg.Dialog)
			var k messages.DraftKey
			switch p := d.Peer.(type) {
			case *tg.PeerUser:
				k.User = serverID(p.UserID)
			case *tg.PeerChannel:
				k.Channel = p.ChannelID
			}
			if dm, ok := byKey[k]; ok {
				d.SetDraft(dm)
			}
		}
	}
	return nil
}

// render turns entries into the dialogs, messages, chats and users lists.
func (a *API) render(ctx context.Context, me account.User, list []entry) ([]tg.DialogClass, []tg.MessageClass, []tg.ChatClass, []tg.UserClass, error) {
	dialogs := []tg.DialogClass{}
	msgs := []tg.MessageClass{}
	users := []int64{me.ID}
	var chans []int64
	for _, e := range list {
		dialogs = append(dialogs, e.dialog)
		switch p := e.dialog.(*tg.Dialog).Peer.(type) {
		case *tg.PeerUser:
			users = append(users, serverID(p.UserID))
		case *tg.PeerChannel:
			chans = append(chans, p.ChannelID)
		}
		if e.msg != nil {
			msgs = append(msgs, e.msg)
			us, cs := mentioned(e.msg)
			users = append(users, us...)
			chans = append(chans, cs...)
		}
	}
	users = append(users, a.livePolls(ctx, me.ID, msgs)...)
	us, err := a.usersFor(ctx, me.ID, users)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	cs, err := a.chatsFor(ctx, me.ID, chans)
	return dialogs, msgs, cs, us, err
}

func (a *API) getDialogs(ctx context.Context, r *tg.MessagesGetDialogsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	folder, _ := r.GetFolderID()
	every, err := a.entries(ctx, me, nil, nil)
	if err != nil {
		return nil, err
	}
	var all, archived []entry
	for _, e := range every {
		switch e.folder {
		case folder:
			all = append(all, e)
		case messages.FolderArchive:
			archived = append(archived, e)
		}
	}
	total := len(all)
	if r.OffsetDate == 0 && !r.ExcludePinned {
		// first page: pinned chats lead
		sort.SliceStable(all, func(i, j int) bool { return all[i].pinned && !all[j].pinned })
	}
	var page []entry
	for _, e := range all {
		if r.ExcludePinned && e.pinned {
			continue
		}
		if r.OffsetDate > 0 && (e.date > r.OffsetDate || e.date == r.OffsetDate && e.top >= r.OffsetID) {
			continue
		}
		page = append(page, e)
	}
	limit := r.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if len(page) > limit {
		page = page[:limit]
	}
	// the archive shows as one row on top of the first page; Android
	// finds it only this way, Web A skips it and asks for folder 1
	var archive *tg.DialogFolder
	if folder == messages.FolderMain && r.OffsetDate == 0 && !r.ExcludePinned && len(archived) > 0 {
		archive = archiveRow(archived)
		page = append(page, archived[0])
	}
	dialogs, msgs, chats, users, err := a.render(ctx, me, page)
	if err != nil {
		return nil, err
	}
	if archive != nil {
		dialogs = append([]tg.DialogClass{archive}, dialogs[:len(dialogs)-1]...)
	}
	if r.OffsetDate == 0 && len(page) == total {
		return &tg.MessagesDialogs{Dialogs: dialogs, Messages: msgs, Chats: chats, Users: users}, nil
	}
	return &tg.MessagesDialogsSlice{Count: total, Dialogs: dialogs, Messages: msgs, Chats: chats, Users: users}, nil
}

// archiveRow is the archive as a chat-list row: the newest archived chat
// and the unread totals. Nothing is muted here, so all of it is unmuted.
func archiveRow(archived []entry) *tg.DialogFolder {
	top := archived[0].dialog.(*tg.Dialog)
	out := &tg.DialogFolder{Folder: tg.Folder{ID: messages.FolderArchive, Title: "Archived Chats"}, Peer: top.Peer, TopMessage: top.TopMessage}
	for _, e := range archived {
		if e.unread > 0 {
			out.UnreadUnmutedPeersCount++
			out.UnreadUnmutedMessagesCount += e.unread
		}
	}
	return out
}

func (a *API) getPeerDialogs(ctx context.Context, r *tg.MessagesGetPeerDialogsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	users, chans := map[int64]bool{}, map[int64]bool{}
	for _, p := range r.Peers {
		v, ok := p.(*tg.InputDialogPeer)
		if !ok {
			continue
		}
		if pr, err := a.peer(ctx, me, v.Peer); err == nil {
			if pr.channel != 0 {
				chans[pr.channel] = true
			} else {
				users[pr.user] = true
			}
		}
	}
	list, err := a.entries(ctx, me, users, chans)
	if err != nil {
		return nil, err
	}
	blank, err := a.blankDialogs(ctx, me, list, users, chans)
	if err != nil {
		return nil, err
	}
	if err := a.attachDrafts(ctx, me, blank); err != nil {
		return nil, err
	}
	return a.peerDialogs(ctx, me, append(list, blank...))
}

// blankDialogs answers for the peers asked about that aren't in the chat
// list: nothing said yet, or a public chat the viewer hasn't joined.
// Telegram always returns a dialog here, and Web A only keeps a chat's
// message list once it has seen one. Without it an empty chat spins forever.
func (a *API) blankDialogs(ctx context.Context, me account.User, have []entry, users, chans map[int64]bool) ([]entry, error) {
	for _, e := range have {
		switch p := e.dialog.(*tg.Dialog).Peer.(type) {
		case *tg.PeerUser:
			delete(users, serverID(p.UserID))
		case *tg.PeerChannel:
			delete(chans, p.ChannelID)
		}
	}
	var out []entry
	for id := range users {
		out = append(out, entry{dialog: dialogTL(messages.Dialog{Peer: id})})
	}
	for id := range chans {
		c, _, err := a.chans.Access(ctx, id, me.ID)
		if err != nil {
			continue
		}
		d := &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: c.ID}, TopMessage: c.TopID, ReadInboxMaxID: c.TopID,
			ReadOutboxMaxID: c.ReadOutboxMax, NotifySettings: tg.PeerNotifySettings{}}
		d.SetPts(c.Pts)
		e := entry{dialog: d}
		top, _, err := a.chans.History(ctx, id, channels.HistoryQuery{Limit: 1})
		if err != nil {
			return nil, err
		}
		if len(top) > 0 {
			e.msg = channels.MarkOut(top[0].TL(), me.ID)
		}
		out = append(out, e)
	}
	return out, nil
}

// Web A takes the state in this answer as its own pts. A message pushed
// a moment later with a pts the state already counts is dropped as seen,
// and the open chat misses it until a reload. So the pushes already
// queued for this user go out first, and the state is read before the
// dialogs: a pts behind them is caught up by getDifference, one ahead
// loses a message.
func (a *API) peerDialogs(ctx context.Context, me account.User, list []entry) (any, error) {
	a.out.wait(ctx, me.ID, 2*time.Second)
	st, err := a.msgs.State(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	dialogs, msgs, chats, users, err := a.render(ctx, me, list)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesPeerDialogs{Dialogs: dialogs, Messages: msgs, Chats: chats, Users: users, State: st}, nil
}

func (a *API) getPinnedDialogs(ctx context.Context, r *tg.MessagesGetPinnedDialogsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	var pinned []entry
	all, err := a.entries(ctx, me, nil, nil)
	if err != nil {
		return nil, err
	}
	for _, e := range all {
		if e.pinned && e.folder == r.FolderID {
			pinned = append(pinned, e)
		}
	}
	return a.peerDialogs(ctx, me, pinned)
}

func (a *API) toggleDialogPin(ctx context.Context, r *tg.MessagesToggleDialogPinRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	p, ok := r.Peer.(*tg.InputDialogPeer)
	if !ok {
		return nil, errPeerInvalid
	}
	to, err := a.peer(ctx, me, p.Peer)
	if err != nil {
		return nil, err
	}
	var found bool
	var peerTL tg.PeerClass
	if to.channel != 0 {
		found, err = a.chans.Pin(ctx, me.ID, to.channel, r.Pinned)
		peerTL = &tg.PeerChannel{ChannelID: to.channel}
	} else {
		found, err = a.msgs.Pin(ctx, me.ID, to.user, r.Pinned)
		peerTL = &tg.PeerUser{UserID: clientID(to.user)}
	}
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errPeerInvalid
	}
	pin := &tg.UpdateDialogPinned{Peer: &tg.DialogPeer{Peer: peerTL}}
	pin.SetPinned(r.Pinned)
	a.echo(ctx, me, []tg.UpdateClass{pin}, []tg.UserClass{})
	return true, nil
}

// messagesBody renders private messages with the users and the channels
// (forwarded posts) they name.
func (a *API) messagesBody(ctx context.Context, me account.User, list []messages.Msg) ([]tg.MessageClass, []tg.UserClass, []tg.ChatClass, error) {
	out := make([]tg.MessageClass, 0, len(list))
	var chans []int64
	ids := make([]int, 0, len(list))
	for _, m := range list {
		out = append(out, m.TL())
		ids = append(ids, m.ID)
		if m.Fwd.Chan != 0 {
			chans = append(chans, m.Fwd.Chan)
		}
	}
	sets, err := a.msgs.Reactions(ctx, me.ID, ids)
	if err != nil {
		return nil, nil, nil, err
	}
	reactors := withReactions(out, sets, me.ID, true)
	reactors = append(reactors, a.livePolls(ctx, me.ID, out)...)
	users, err := a.usersFor(ctx, me.ID, append(messages.Users(list), reactors...))
	if err != nil {
		return nil, nil, nil, err
	}
	chats, err := a.chatsFor(ctx, me.ID, chans)
	return out, users, chats, err
}

func (a *API) getHistory(ctx context.Context, r *tg.MessagesGetHistoryRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if to.channel != 0 {
		c, _, err := a.chans.Access(ctx, to.channel, me.ID)
		if err != nil {
			return nil, err
		}
		list, total, err := a.chans.History(ctx, to.channel, channels.HistoryQuery{
			OffsetID: r.OffsetID, OffsetDate: r.OffsetDate, AddOffset: r.AddOffset, Limit: r.Limit, MaxID: r.MaxID, MinID: r.MinID,
		})
		if err != nil {
			return nil, err
		}
		msgs, users, chats, err := a.channelBody(ctx, me, c, list)
		if err != nil {
			return nil, err
		}
		return &tg.MessagesChannelMessages{Pts: c.Pts, Count: total, Messages: msgs, Topics: []tg.ForumTopicClass{}, Chats: chats, Users: users}, nil
	}
	peer := to.user
	list, total, err := a.msgs.History(ctx, me.ID, peer, messages.HistoryQuery{
		OffsetID: r.OffsetID, OffsetDate: r.OffsetDate, AddOffset: r.AddOffset, Limit: r.Limit, MaxID: r.MaxID, MinID: r.MinID,
	})
	if err != nil {
		return nil, err
	}
	msgs, users, chats, err := a.messagesBody(ctx, me, list)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesMessagesSlice{Count: total, Messages: msgs, Chats: chats, Users: users}, nil
}

func (a *API) getMessages(ctx context.Context, r *tg.MessagesGetMessagesRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	var want []int
	var replyOf []int
	for _, in := range r.ID {
		switch v := in.(type) {
		case *tg.InputMessageID:
			want = append(want, v.ID)
		case *tg.InputMessageReplyTo:
			replyOf = append(replyOf, v.ID)
		}
	}
	if len(replyOf) > 0 {
		quoted, err := a.msgs.Get(ctx, me.ID, replyOf)
		if err != nil {
			return nil, err
		}
		for _, m := range quoted {
			if m.ReplyTo != 0 {
				want = append(want, m.ReplyTo)
			}
		}
	}
	list, err := a.msgs.Get(ctx, me.ID, want)
	if err != nil {
		return nil, err
	}
	msgs, users, chats, err := a.messagesBody(ctx, me, list)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesMessages{Messages: msgs, Chats: chats, Users: users}, nil
}

func (a *API) search(ctx context.Context, r *tg.MessagesSearchRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	// the pinned bar and list
	if _, ok := r.Filter.(*tg.InputMessagesFilterPinned); ok {
		return a.filtered(ctx, me, to, messages.Pinned, r.OffsetID, r.AddOffset, r.Limit, r.MaxID, r.MinID)
	}
	kinds, ok := filterKinds(r.Filter)
	if !ok || r.Q == "" && len(kinds) == 0 {
		return &tg.MessagesMessages{Messages: []tg.MessageClass{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}, nil
	}
	if to.channel != 0 {
		c, _, err := a.chans.Access(ctx, to.channel, me.ID)
		if err != nil {
			return nil, err
		}
		list, total, err := a.chans.SearchTopic(ctx, to.channel, searchTopic(c, r.TopMsgID), r.Q, r.OffsetID, r.Limit, kinds...)
		if err != nil {
			return nil, err
		}
		msgs, users, chats, err := a.channelBody(ctx, me, c, list)
		if err != nil {
			return nil, err
		}
		return &tg.MessagesChannelMessages{Pts: c.Pts, Count: total, Messages: msgs, Topics: []tg.ForumTopicClass{}, Chats: chats, Users: users}, nil
	}
	peer := to.user
	list, total, err := a.msgs.Search(ctx, me.ID, peer, r.Q, r.OffsetID, r.Limit, kinds...)
	if err != nil {
		return nil, err
	}
	msgs, users, chats, err := a.messagesBody(ctx, me, list)
	if err != nil {
		return nil, err
	}
	return &tg.MessagesMessagesSlice{Count: total, Messages: msgs, Chats: chats, Users: users}, nil
}

// searchTopic is the topic a search names; top_msg_id means a comment thread
// outside forums, which search doesn't narrow to.
func searchTopic(c channels.Channel, top int) int {
	if c.Forum {
		return top
	}
	return 0
}
