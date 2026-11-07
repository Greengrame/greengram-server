package api

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/reports"
)

// Reports from the apps: messages.report and stories.report walk the
// reason menu (reasons.go in internal/reports), the older calls take a
// reason or none. Every one ends in a row for the admins' Reports page.
//
// The reporter learns nothing from the answers: whatever state the target
// is in, whether the report was a repeat or over the hourly limit, they get
// the same reply. The reported side is told nothing.

var (
	errMessageIDRequired = tgerr.New(400, "MESSAGE_ID_REQUIRED")
	errOptionInvalid     = tgerr.New(400, "OPTION_INVALID")
	errMessageIDInvalid  = tgerr.New(400, "MESSAGE_ID_INVALID")
)

// maxReported is how many messages one report may point at.
const maxReported = 100

// spamContext is how many recent messages a spam report keeps when it
// names no messages itself.
const spamContext = 20

// RegisterReports stores reports in r; without it the calls are refused.
func (a *API) RegisterReports(r *reports.Service) {
	a.reports = r
	handle(a, tlprofile.SemanticMethodMessagesReport, a.reportMessages)
	handle(a, tlprofile.SemanticMethodStoriesReport, a.reportStories)
	handle(a, tlprofile.SemanticMethodAccountReportPeer, a.reportPeer)
	handle(a, tlprofile.SemanticMethodAccountReportProfilePhoto, a.reportProfilePhoto)
	handle(a, tlprofile.SemanticMethodMessagesReportSpam, a.reportSpam)
	handle(a, tlprofile.SemanticMethodChannelsReportSpam, a.reportChannelSpam)
	handle(a, tlprofile.SemanticMethodMessagesReportReaction, a.reportReaction)
}

// reportChat is where a report was made, as the reporter sees it.
type reportChat struct {
	user    account.User // private chat
	channel channels.Channel
	member  channels.Membership
}

func (c reportChat) isChannel() bool { return c.channel.ID != 0 }

// resolveReported finds the chat. A user is found in any state: a reporter
// mustn't be able to tell a locked account from an active one by
// reporting it.
func (a *API) resolveReported(ctx context.Context, me account.User, p tg.InputPeerClass) (reportChat, error) {
	switch v := p.(type) {
	case *tg.InputPeerChannel, *tg.InputPeerChannelFromMessage:
		var id int64
		if c, ok := v.(*tg.InputPeerChannel); ok {
			id = c.ChannelID
		} else {
			id = v.(*tg.InputPeerChannelFromMessage).ChannelID
		}
		c, m, err := a.chans.Access(ctx, id, me.ID)
		if err != nil {
			return reportChat{}, err
		}
		return reportChat{channel: c, member: m}, nil
	}
	var id int64
	switch v := p.(type) {
	case *tg.InputPeerSelf:
		id = me.ID
	case *tg.InputPeerUser:
		id = serverID(v.UserID)
	case *tg.InputPeerUserFromMessage:
		id = serverID(v.UserID)
	default:
		return reportChat{}, errPeerInvalid
	}
	return a.userChat(ctx, id)
}

func (a *API) userChat(ctx context.Context, id int64) (reportChat, error) {
	users, err := a.accounts.Users(ctx, []int64{id})
	if err != nil {
		return reportChat{}, err
	}
	u, ok := users[id]
	if !ok {
		return reportChat{}, errPeerInvalid
	}
	return reportChat{user: u}, nil
}

// base is a report about the chat itself.
func (c reportChat) base(me int64) reports.Report {
	r := reports.Report{Reporter: me}
	if c.isChannel() {
		r.ChatKind, r.ChatID = "channel", c.channel.ID
		r.TargetKey, r.TargetType = reports.ChatKey(c.channel.ID), chanType(c.channel)
		r.Snapshot.Peer = &reports.PeerShot{Name: c.channel.Title, Username: c.channel.Username, About: c.channel.About, PhotoID: idStr(c.channel.PhotoID)}
		return r
	}
	r.ChatKind, r.ChatID = "user", c.user.ID
	r.TargetKey, r.TargetType = reports.UserKey(c.user.ID), userType(c.user)
	r.Snapshot.Peer = userShot(c.user)
	return r
}

func chanType(c channels.Channel) string {
	if c.Megagroup {
		return "group"
	}
	return "channel"
}

func userType(u account.User) string {
	if u.IsBot {
		return "bot"
	}
	return "user"
}

func userShot(u account.User) *reports.PeerShot {
	return &reports.PeerShot{Name: u.Name, Username: u.Username, About: u.Bio, PhotoID: idStr(u.PhotoID)}
}

func idStr(id int64) string {
	if id == 0 {
		return ""
	}
	return itoa64(id)
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// needsMessages is Telegram's MESSAGE_ID_REQUIRED: a report on a private
// chat or a group has to point at what's wrong, once there is anything to
// point at. A channel can be reported as a whole.
func (a *API) needsMessages(ctx context.Context, me account.User, c reportChat) (bool, error) {
	if c.isChannel() {
		if !c.channel.Megagroup {
			return false, nil
		}
		return a.reports.HasOthers(ctx, c.channel.ID, me.ID)
	}
	if c.user.ID == me.ID {
		return false, nil
	}
	return a.reports.HasIncoming(ctx, me.ID, c.user.ID)
}

// menuStep answers a step before the comment one; done is false when the
// caller has to file.
func menuStep(option []byte) (reports.Step, tg.ReportResultClass, error) {
	step, ok := reports.ParseOption(option)
	if !ok {
		return step, nil, errOptionInvalid
	}
	if step.Comment {
		return step, nil, nil
	}
	return step, reports.Menu(step.Path), nil
}

// comment cleans what the reporter typed; ok is false when the reason needs
// one and there isn't.
func comment(path []string, text string) (string, bool) {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > reports.MaxComment {
		text = string([]rune(text)[:reports.MaxComment])
	}
	r, _ := reports.Find(path)
	return text, text != "" || !r.CommentRequired
}

func (a *API) reportMessages(ctx context.Context, r *tg.MessagesReportRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	c, err := a.resolveReported(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if len(r.ID) > maxReported {
		return nil, errMessageIDInvalid
	}
	step, next, err := menuStep(r.Option)
	if err != nil {
		return nil, err
	}
	if next != nil {
		if _, comment := next.(*tg.ReportResultAddComment); comment && len(r.ID) == 0 {
			need, err := a.needsMessages(ctx, me, c)
			if err != nil {
				return nil, err
			}
			if need {
				return nil, errMessageIDRequired
			}
		}
		return next, nil
	}
	text, ok := comment(step.Path, r.Message)
	if !ok {
		return reports.Menu(step.Path), nil
	}
	rep := c.base(me.ID)
	rep.Kind, rep.Reason, rep.Comment = reports.KindPeer, step.Path, text
	if len(r.ID) > 0 {
		rep.Kind = reports.KindMessage
		found, err := a.snapshotMessages(ctx, me, c, r.ID, &rep)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, errMessageIDInvalid
		}
	}
	a.file(ctx, rep)
	return &tg.ReportResultReported{}, nil
}

// snapshotMessages fills the report with the messages as they are now and
// points it at whoever wrote them; false if none of ids is there.
func (a *API) snapshotMessages(ctx context.Context, me account.User, c reportChat, list []int, rep *reports.Report) (bool, error) {
	var shots []reports.MsgShot
	senders := map[int64]bool{}
	post := false
	if c.isChannel() {
		msgs, err := a.chans.Messages(ctx, c.channel.ID, list)
		if err != nil {
			return false, err
		}
		names, err := a.senderNames(ctx, chanSenders(msgs))
		if err != nil {
			return false, err
		}
		for _, m := range msgs {
			s := reports.MsgShot{ID: m.ID, From: m.From, FromName: names[m.From], Post: m.Post, Date: m.Date, EditDate: m.EditDate,
				Text: m.Text, Media: mediaShot(m.Media), Service: m.Action != nil}
			if m.Post {
				s.FromName, post = c.channel.Title, true
			} else if m.From != me.ID {
				senders[m.From] = true
			}
			shots = append(shots, s)
			rep.MsgIDs = append(rep.MsgIDs, m.ID)
		}
	} else {
		msgs, err := a.msgs.Get(ctx, me.ID, list)
		if err != nil {
			return false, err
		}
		names, err := a.senderNames(ctx, []int64{me.ID, c.user.ID})
		if err != nil {
			return false, err
		}
		for _, m := range msgs {
			if m.Peer != c.user.ID {
				continue
			}
			shots = append(shots, reports.MsgShot{ID: m.ID, From: m.From, FromName: names[m.From], Date: m.Date, EditDate: m.EditDate,
				Text: m.Text, Media: mediaShot(m.Media), Service: m.Action != nil})
			rep.MsgIDs = append(rep.MsgIDs, m.ID)
			rep.SharedIDs = append(rep.SharedIDs, m.Shared)
		}
	}
	if len(shots) == 0 {
		return false, nil
	}
	rep.Snapshot.Messages = shots
	// in a group, the one person who wrote them; a channel's own posts, or
	// several people, leave it on the chat
	if c.isChannel() && !post && len(senders) == 1 {
		for id := range senders {
			users, err := a.accounts.Users(ctx, []int64{id})
			if err != nil {
				return false, err
			}
			if u, ok := users[id]; ok {
				rep.TargetKey, rep.TargetType = reports.UserKey(id), userType(u)
				rep.Snapshot.Peer = userShot(u)
			}
		}
	}
	return true, nil
}

func chanSenders(list []channels.Msg) []int64 {
	out := make([]int64, 0, len(list))
	for _, m := range list {
		out = append(out, m.From)
	}
	return out
}

func (a *API) senderNames(ctx context.Context, list []int64) (map[int64]string, error) {
	users, err := a.accounts.Users(ctx, list)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(users))
	for id, u := range users {
		out[id] = u.Name
	}
	return out, nil
}

// mediaShot is enough to find the file again and say what it was.
func mediaShot(r media.Ref) *reports.MediaShot {
	switch m := r.TL.(type) {
	case nil:
		return nil
	case *tg.MessageMediaPhoto:
		if p, ok := m.GetPhoto(); ok {
			if ph, ok := p.(*tg.Photo); ok {
				return &reports.MediaShot{Type: "photo", ID: itoa64(ph.ID)}
			}
		}
		return &reports.MediaShot{Type: "photo"}
	case *tg.MessageMediaDocument:
		d, ok := m.GetDocument()
		if !ok {
			return &reports.MediaShot{Type: "document"}
		}
		doc, ok := d.(*tg.Document)
		if !ok {
			return &reports.MediaShot{Type: "document"}
		}
		s := &reports.MediaShot{Type: "document", ID: itoa64(doc.ID), Mime: doc.MimeType, Size: doc.Size}
		for _, at := range doc.Attributes {
			switch v := at.(type) {
			case *tg.DocumentAttributeSticker:
				s.Type, s.Emoji = "sticker", v.Alt
			case *tg.DocumentAttributeCustomEmoji:
				s.Type, s.Emoji = "sticker", v.Alt
			case *tg.DocumentAttributeAnimated:
				if s.Type == "document" || s.Type == "video" {
					s.Type = "gif"
				}
			case *tg.DocumentAttributeVideo:
				if s.Type == "document" {
					s.Type = "video"
				}
			case *tg.DocumentAttributeAudio:
				if s.Type == "document" {
					s.Type = "audio"
					if v.Voice {
						s.Type = "voice"
					}
				}
			case *tg.DocumentAttributeFilename:
				s.Name = v.FileName
			}
		}
		return s
	}
	return &reports.MediaShot{Type: "other", What: strings.TrimPrefix(r.TL.TypeName(), "messageMedia")}
}

// file stores a report. Its outcome never reaches the reporter.
func (a *API) file(ctx context.Context, rep reports.Report) {
	if a.reports == nil {
		return
	}
	stored, err := a.reports.File(ctx, rep)
	if err != nil {
		slog.Error("api: file report", "reporter", rep.Reporter, "err", err)
		return
	}
	if !stored {
		slog.Info("api: report not stored (repeat or over the hourly limit)", "reporter", rep.Reporter, "target", rep.TargetKey)
	}
}

func (a *API) reportStories(ctx context.Context, r *tg.StoriesReportRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	step, next, err := menuStep(r.Option)
	if err != nil {
		return nil, err
	}
	if next != nil {
		return next, nil
	}
	text, ok := comment(step.Path, r.Message)
	if !ok {
		return reports.Menu(step.Path), nil
	}
	owner := int64(0)
	if a.stories != nil {
		if owner, err = a.storyPeer(ctx, me, r.Peer); err != nil && !errors.Is(err, errPeerInvalid) {
			return nil, err
		}
	}
	// channels have no stories here; nothing to keep
	if owner == 0 || len(r.ID) == 0 {
		return &tg.ReportResultReported{}, nil
	}
	c, err := a.userChat(ctx, owner)
	if err != nil {
		return nil, err
	}
	rep := c.base(me.ID)
	rep.Kind, rep.Reason, rep.Comment, rep.StoryID = reports.KindStory, step.Path, text, r.ID[0]
	// gone or out of sight by now: the report still goes in, the answer
	// mustn't say which
	if list, err := a.stories.ByID(ctx, me.ID, owner, r.ID[:1]); err == nil && len(list) > 0 {
		st := list[0]
		rep.Snapshot.Story = &reports.StoryShot{ID: st.ID, Date: st.Date, Caption: st.Caption, Media: mediaShot(st.Media)}
	} else if err != nil {
		slog.Warn("api: story for a report", "owner", owner, "err", err)
	}
	a.file(ctx, rep)
	return &tg.ReportResultReported{}, nil
}

func (a *API) reportPeer(ctx context.Context, r *tg.AccountReportPeerRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	c, err := a.resolveReported(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	rep := c.base(me.ID)
	rep.Kind, rep.Reason = reports.KindPeer, reports.FromReason(r.Reason)
	rep.Comment, _ = comment(nil, r.Message)
	a.file(ctx, rep)
	return true, nil
}

func (a *API) reportProfilePhoto(ctx context.Context, r *tg.AccountReportProfilePhotoRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	c, err := a.resolveReported(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	in, ok := r.PhotoID.(*tg.InputPhoto)
	if !ok {
		return nil, tgerr.New(400, "PHOTO_INVALID")
	}
	rep := c.base(me.ID)
	rep.Kind, rep.Reason, rep.PhotoID = reports.KindPhoto, reports.FromReason(r.Reason), in.ID
	rep.Comment, _ = comment(nil, r.Message)
	rep.Snapshot.Photo = &reports.MediaShot{Type: "photo", ID: itoa64(in.ID)}
	a.file(ctx, rep)
	return true, nil
}

// reportSpam is the "Report spam" bar on a chat with someone new, or a
// group or channel someone added you to. It keeps the latest messages
// from them, since it names none.
func (a *API) reportSpam(ctx context.Context, r *tg.MessagesReportSpamRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	c, err := a.resolveReported(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	rep := c.base(me.ID)
	rep.Kind, rep.Reason = reports.KindSpam, []string{"spam", "spam"}
	var recent []int
	if c.isChannel() {
		recent, err = a.reports.RecentInChannel(ctx, c.channel.ID, me.ID, spamContext)
	} else if c.user.ID != me.ID {
		recent, err = a.reports.RecentFrom(ctx, me.ID, c.user.ID, spamContext)
	}
	if err != nil {
		return nil, err
	}
	if len(recent) > 0 {
		peer := rep.Snapshot.Peer
		if _, err := a.snapshotMessages(ctx, me, c, recent, &rep); err != nil {
			return nil, err
		}
		// it's about the chat, whoever wrote in it
		rep.TargetKey, rep.TargetType, rep.Snapshot.Peer = c.base(me.ID).TargetKey, c.base(me.ID).TargetType, peer
		if c.isChannel() {
			// context, not the offence: other members wrote some of it
			rep.MsgIDs = nil
		}
	}
	a.file(ctx, rep)
	return true, nil
}

// reportChannelSpam is a group admin's "Report spam" when deleting someone's
// messages.
func (a *API) reportChannelSpam(ctx context.Context, r *tg.ChannelsReportSpamRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	id, err := inputChannel(r.Channel)
	if err != nil {
		return nil, err
	}
	c, err := a.resolveReported(ctx, me, &tg.InputPeerChannel{ChannelID: id})
	if err != nil {
		return nil, err
	}
	if c.member.Status != channels.Creator && c.member.Status != channels.Admin {
		return nil, tgerr.New(400, "CHAT_ADMIN_REQUIRED")
	}
	if len(r.ID) > maxReported {
		return nil, errMessageIDInvalid
	}
	rep := c.base(me.ID)
	rep.Kind, rep.Reason = reports.KindSpam, []string{"spam", "spam"}
	if len(r.ID) > 0 {
		// deleting races this call; whatever is left gets kept
		if _, err := a.snapshotMessages(ctx, me, c, r.ID, &rep); err != nil {
			return nil, err
		}
	}
	if p, ok := r.Participant.(*tg.InputPeerUser); ok {
		uid := serverID(p.UserID)
		users, err := a.accounts.Users(ctx, []int64{uid})
		if err != nil {
			return nil, err
		}
		if u, ok := users[uid]; ok {
			rep.TargetKey, rep.TargetType, rep.Snapshot.Peer = reports.UserKey(uid), userType(u), userShot(u)
		}
	}
	a.file(ctx, rep)
	return true, nil
}

// reportReaction is about who reacted, not the message.
func (a *API) reportReaction(ctx context.Context, r *tg.MessagesReportReactionRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	c, err := a.resolveReported(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	who, err := a.resolveReported(ctx, me, r.ReactionPeer)
	if err != nil {
		return nil, err
	}
	if who.isChannel() {
		return nil, errPeerInvalid
	}
	rep := c.base(me.ID)
	rep.Kind, rep.Reason = reports.KindReaction, []string{"spam", "spam"}
	if found, err := a.snapshotMessages(ctx, me, c, []int{r.ID}, &rep); err != nil {
		return nil, err
	} else if !found {
		return nil, errMessageIDInvalid
	}
	var shared int64
	if len(rep.SharedIDs) > 0 {
		shared = rep.SharedIDs[0]
	}
	list, err := a.reports.ReactionsBy(ctx, c.channel.ID, rep.MsgIDs[0], shared, who.user.ID)
	if err != nil {
		return nil, err
	}
	rep.Snapshot.Reaction = strings.Join(list, " ")
	// the message is someone else's: kept to show, not to delete
	rep.MsgIDs, rep.SharedIDs = nil, nil
	rep.TargetKey, rep.TargetType, rep.Snapshot.Peer = reports.UserKey(who.user.ID), userType(who.user), userShot(who.user)
	a.file(ctx, rep)
	return true, nil
}

// What the panel's Reports page does through the same code the apps use,
// so everyone's apps hear about it.

// DeleteForEveryone removes reported messages: in a private chat by the ids
// both copies share, so it works after either side deleted theirs.
func (a *API) DeleteForEveryone(ctx context.Context, chatKind string, chat int64, list []int, shared []int64) (int, error) {
	if chatKind == "channel" {
		n, events, err := a.chans.Remove(ctx, chat, list)
		if err != nil {
			return 0, err
		}
		a.deliverChannel(events, edge.Session{})
		return n, nil
	}
	if len(shared) == 0 {
		return 0, nil
	}
	copies, err := a.reports.Copies(ctx, shared)
	if err != nil {
		return 0, err
	}
	n := 0
	for owner, ids := range copies {
		_, events, err := a.msgs.Delete(ctx, owner, ids, true)
		if err != nil {
			return n, err
		}
		a.deliver(events, edge.Session{})
		n += len(ids)
	}
	return n, nil
}

// BanFromChannel removes and bans user, as the chat's owner would.
func (a *API) BanFromChannel(ctx context.Context, channel, user int64) error {
	c, ok, err := a.chans.Get(ctx, channel)
	if err != nil {
		return err
	}
	if !ok || c.Deleted {
		return tgerr.New(400, "CHANNEL_INVALID")
	}
	if c.Creator == user {
		return reports.ErrOwner
	}
	events, err := a.chans.EditBanned(ctx, c.Creator, channel, user, tg.ChatBannedRights{ViewMessages: true})
	if err != nil {
		return err
	}
	a.deliverChannel(events, edge.Session{})
	return nil
}
