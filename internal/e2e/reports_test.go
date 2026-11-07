package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/admin"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/reports"
)

// mountReports is mountAdmin with the Reports page's deletes and bans wired.
func (s *stack) mountReports() {
	adm := admin.New(s.db.Pool, s.accounts, s.settings, channels.New(s.db.Pool))
	adm.AccessChanged = s.api.AccessChanged
	adm.AnswerAppeal = s.api.AnswerAppeal
	adm.Moderator = s.api
	adm.Register(s.mux)
}

func send(ctx context.Context, t *testing.T, c *tg.Client, peer tg.InputPeerClass, text string, random int64) int {
	t.Helper()
	res, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peer, Message: text, RandomID: random})
	if err != nil {
		t.Fatalf("send %q: %v", text, err)
	}
	return sentMsg(t, res)
}

// reportAll walks messages.report the way Web A does: the first menu, the
// options given, then the comment. It returns every answer.
func reportAll(ctx context.Context, c *tg.Client, peer tg.InputPeerClass, ids []int, comment string, path ...string) ([]tg.ReportResultClass, error) {
	var out []tg.ReportResultClass
	option := []byte{}
	for i := 0; ; i++ {
		res, err := c.MessagesReport(ctx, &tg.MessagesReportRequest{Peer: peer, ID: ids, Option: option})
		if err != nil {
			return out, err
		}
		out = append(out, res)
		switch v := res.(type) {
		case *tg.ReportResultChooseOption:
			if i >= len(path) {
				return out, fmt.Errorf("menu %q past the path", v.Title)
			}
			found := false
			for _, o := range v.Options {
				if opt := string(o.Option); strings.HasSuffix(opt, "/"+path[i]) || strings.HasSuffix(opt, ":"+path[i]) {
					option, found = o.Option, true
				}
			}
			if !found {
				return out, fmt.Errorf("no %q in %+v", path[i], v.Options)
			}
		case *tg.ReportResultAddComment:
			res, err := c.MessagesReport(ctx, &tg.MessagesReportRequest{Peer: peer, ID: ids, Option: v.Option, Message: comment})
			if err != nil {
				return out, err
			}
			return append(out, res), nil
		case *tg.ReportResultReported:
			return out, nil
		}
	}
}

func reportRows(t *testing.T, s *stack, where string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM content_reports WHERE "+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func groupOf(t *testing.T, list map[string]any, key string) map[string]any {
	t.Helper()
	groups, _ := list["groups"].([]any)
	for _, g := range groups {
		if m := g.(map[string]any); m["key"] == key {
			return m
		}
	}
	t.Fatalf("no group %s in %v", key, list)
	return nil
}

func TestContentReports(t *testing.T) {
	s := start(t)
	s.reports.SetNoticeGap(time.Hour) // TestReportNotices covers batching
	boss := s.browser(t)
	head := boss.must("/api/register", map[string]string{"username": "boss", "name": "Boss", "password": "correct horse boss"})
	bossID := int64(head["id"].(float64))
	s.mountReports()
	ada := s.signUp(t, "ada")
	bob := s.signUp(t, "bob")
	carol := s.signUp(t, "carol")
	bg := context.Background()
	ctx, cancel := context.WithTimeout(bg, 4*time.Minute)
	defer cancel()

	adaPush := make(chan tg.UpdatesClass, 512)
	bobPush := make(chan tg.UpdatesClass, 512)
	carolPush := make(chan tg.UpdatesClass, 512)
	ac := s.live(t, ada, adaPush)
	bc := s.live(t, bob, bobPush)
	cc := s.live(t, carol, carolPush)

	// private: bob writes ada twice
	send(ctx, t, bc, userPeer(ada.id), "buy cheap pills", 101)
	send(ctx, t, bc, userPeer(ada.id), "dm me your card", 102)
	got, err := history(ctx, ac, bob.id)
	if err != nil || len(got) != 2 {
		t.Fatalf("ada's history %d %v", len(got), err)
	}
	pills, card := got[1].ID, got[0].ID

	// the menus
	res, err := ac.MessagesReport(ctx, &tg.MessagesReportRequest{Peer: userPeer(bob.id)})
	if err != nil {
		t.Fatal(err)
	}
	root, ok := res.(*tg.ReportResultChooseOption)
	if !ok || root.Title == "" {
		t.Fatalf("first step %+v", res)
	}
	var texts []string
	for _, o := range root.Options {
		texts = append(texts, o.Text)
	}
	for _, want := range []string{"Spam or scam", "Violence", "Child abuse", "Illegal goods and services", "Personal data", "Copyright", "Other"} {
		if !strings.Contains(strings.Join(texts, "|"), want) {
			t.Fatalf("no %q in %v", want, texts)
		}
	}
	res, err = ac.MessagesReport(ctx, &tg.MessagesReportRequest{Peer: userPeer(bob.id), Option: []byte("r:spam")})
	if sub, ok := res.(*tg.ReportResultChooseOption); err != nil || !ok || len(sub.Options) != 4 {
		t.Fatalf("spam menu %+v %v", res, err)
	}
	if _, err := ac.MessagesReport(ctx, &tg.MessagesReportRequest{Peer: userPeer(bob.id), Option: []byte("nonsense")}); !tgerr.Is(err, "OPTION_INVALID") {
		t.Fatalf("bad option: %v", err)
	}
	// a private chat with messages in it: point at them, like Telegram
	if _, err := ac.MessagesReport(ctx, &tg.MessagesReportRequest{Peer: userPeer(bob.id), Option: []byte("r:spam/phishing")}); !tgerr.Is(err, "MESSAGE_ID_REQUIRED") {
		t.Fatalf("no messages picked: %v", err)
	}
	steps, err := reportAll(ctx, ac, userPeer(bob.id), []int{pills, card}, "  he wants my card  ", "spam", "phishing")
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := steps[2].(*tg.ReportResultAddComment); !ok || !c.Optional {
		t.Fatalf("comment step %+v", steps[2])
	}
	if _, ok := steps[3].(*tg.ReportResultReported); !ok {
		t.Fatalf("last step %+v", steps[3])
	}
	// "Other" needs words
	res, err = ac.MessagesReport(ctx, &tg.MessagesReportRequest{Peer: userPeer(bob.id), ID: []int{card}, Option: []byte("r:other")})
	if c, ok := res.(*tg.ReportResultAddComment); err != nil || !ok || c.Optional {
		t.Fatalf("other %+v %v", res, err)
	}
	res, err = ac.MessagesReport(ctx, &tg.MessagesReportRequest{Peer: userPeer(bob.id), ID: []int{card}, Option: []byte("c:other")})
	if _, ok := res.(*tg.ReportResultAddComment); err != nil || !ok {
		t.Fatalf("other without words %+v %v", res, err)
	}
	if _, err := ac.MessagesReport(ctx, &tg.MessagesReportRequest{Peer: userPeer(bob.id), ID: []int{99999}, Option: []byte("c:other"), Message: "x"}); !tgerr.Is(err, "MESSAGE_ID_INVALID") {
		t.Fatalf("someone else's id: %v", err)
	}
	if n := reportRows(t, s, "reporter_id=$1", ada.id); n != 1 {
		t.Fatalf("%d reports after the menus", n)
	}
	var snap, comment string
	s.db.Pool.QueryRow(bg, "SELECT snapshot::text, comment FROM content_reports WHERE reporter_id=$1", ada.id).Scan(&snap, &comment)
	if !strings.Contains(snap, "buy cheap pills") || comment != "he wants my card" {
		t.Fatalf("snapshot %s comment %q", snap, comment)
	}

	// edited and deleted afterwards: the panel still has what ada saw
	bh, err := history(ctx, bc, ada.id)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range bh {
		if m.Message == "buy cheap pills" {
			if _, err := bc.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{Peer: userPeer(ada.id), ID: m.ID, Message: "nothing to see"}); err != nil {
				t.Fatal(err)
			}
		}
		if m.Message == "dm me your card" {
			if _, err := bc.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{ID: []int{m.ID}, Revoke: true}); err != nil {
				t.Fatal(err)
			}
		}
	}

	// a group: bob posts, ada and carol report the same message
	var group *tg.Channel
	{
		res, err := cc.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{Title: "Lab", Users: []tg.InputUserClass{&tg.InputUser{UserID: ada.id}, &tg.InputUser{UserID: bob.id}}})
		if err != nil {
			t.Fatal(err)
		}
		group = channelOf(t, res.Updates)
	}
	g1 := send(ctx, t, bc, peerCh(group), "hate in the group", 201)
	g2 := send(ctx, t, bc, peerCh(group), "more of it", 202)
	carolSaid := send(ctx, t, cc, peerCh(group), "please stop", 203)
	if _, err := ac.MessagesReport(ctx, &tg.MessagesReportRequest{Peer: peerCh(group), Option: []byte("r:violence/hate")}); !tgerr.Is(err, "MESSAGE_ID_REQUIRED") {
		t.Fatalf("group without messages: %v", err)
	}
	for _, c := range []*tg.Client{ac, cc} {
		if _, err := reportAll(ctx, c, peerCh(group), []int{g1}, "", "violence", "hate"); err != nil {
			t.Fatal(err)
		}
	}
	// the same report twice is one row
	if _, err := reportAll(ctx, cc, peerCh(group), []int{g1}, "", "violence", "hate"); err != nil {
		t.Fatal(err)
	}
	if n := reportRows(t, s, "target_key=$1 AND chat_kind='channel'", reports.UserKey(bob.id)); n != 2 {
		t.Fatalf("%d group reports on bob", n)
	}
	// a group admin's "report spam" while deleting; others can't
	if ok, err := cc.ChannelsReportSpam(ctx, &tg.ChannelsReportSpamRequest{Channel: inCh(group), Participant: userPeer(bob.id), ID: []int{g2}}); err != nil || !ok {
		t.Fatalf("admin spam report %v %v", ok, err)
	}
	if _, err := ac.ChannelsReportSpam(ctx, &tg.ChannelsReportSpamRequest{Channel: inCh(group), Participant: userPeer(bob.id), ID: []int{g2}}); !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("member spam report: %v", err)
	}
	// a reaction
	if _, err := bc.MessagesSendReaction(ctx, &tg.MessagesSendReactionRequest{Peer: peerCh(group), MsgID: carolSaid, Reaction: emoji("👍")}); err != nil {
		t.Fatal(err)
	}
	if ok, err := cc.MessagesReportReaction(ctx, &tg.MessagesReportReactionRequest{Peer: peerCh(group), ID: carolSaid, ReactionPeer: userPeer(bob.id)}); err != nil || !ok {
		t.Fatalf("reaction report %v %v", ok, err)
	}
	s.db.Pool.QueryRow(bg, "SELECT snapshot->>'reaction' FROM content_reports WHERE kind='reaction'").Scan(&snap)
	if snap != "👍" {
		t.Fatalf("reaction snapshot %q", snap)
	}

	// a user from the profile, and a profile picture
	if ok, err := cc.AccountReportPeer(ctx, &tg.AccountReportPeerRequest{Peer: userPeer(bob.id), Reason: &tg.InputReportReasonFake{}, Message: "not the real bob"}); err != nil || !ok {
		t.Fatalf("report peer %v %v", ok, err)
	}
	if ok, err := cc.AccountReportProfilePhoto(ctx, &tg.AccountReportProfilePhotoRequest{Peer: userPeer(bob.id), PhotoID: &tg.InputPhoto{ID: 4242}, Reason: &tg.InputReportReasonPornography{}}); err != nil || !ok {
		t.Fatalf("report photo %v %v", ok, err)
	}
	// a story
	story, err := postStory(ctx, bc, 301, "look at this", &tg.InputPrivacyValueAllowAll{})
	if err != nil {
		t.Fatal(err)
	}
	sr, err := cc.StoriesReport(ctx, &tg.StoriesReportRequest{Peer: userPeer(bob.id), ID: []int{story.ID}})
	if _, ok := sr.(*tg.ReportResultChooseOption); err != nil || !ok {
		t.Fatalf("story menu %+v %v", sr, err)
	}
	sr, err = cc.StoriesReport(ctx, &tg.StoriesReportRequest{Peer: userPeer(bob.id), ID: []int{story.ID}, Option: []byte("r:copyright")})
	add, ok := sr.(*tg.ReportResultAddComment)
	if err != nil || !ok {
		t.Fatalf("story leaf %+v %v", sr, err)
	}
	if sr, err = cc.StoriesReport(ctx, &tg.StoriesReportRequest{Peer: userPeer(bob.id), ID: []int{story.ID}, Option: add.Option}); err != nil {
		t.Fatal(err)
	} else if _, ok := sr.(*tg.ReportResultReported); !ok {
		t.Fatalf("story filed %+v", sr)
	}
	s.db.Pool.QueryRow(bg, "SELECT snapshot->'story'->>'caption' FROM content_reports WHERE kind='story'").Scan(&snap)
	if snap != "look at this" {
		t.Fatalf("story snapshot %q", snap)
	}

	// a broadcast channel can be reported whole
	var news *tg.Channel
	{
		res, err := cc.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: true, Title: "News"})
		if err != nil {
			t.Fatal(err)
		}
		news = channelOf(t, res)
		if _, err := cc.ChannelsInviteToChannel(ctx, &tg.ChannelsInviteToChannelRequest{Channel: inCh(news), Users: []tg.InputUserClass{&tg.InputUser{UserID: ada.id}}}); err != nil {
			t.Fatal(err)
		}
		send(ctx, t, cc, peerCh(news), "stolen movie", 401)
	}
	if _, err := reportAll(ctx, ac, peerCh(news), nil, "pirated", "copyright"); err != nil {
		t.Fatalf("channel report: %v", err)
	}

	// spam from the chat's bar: keeps what bob sent carol
	send(ctx, t, bc, userPeer(carol.id), "free crypto here", 501)
	if ok, err := cc.MessagesReportSpam(ctx, userPeer(bob.id)); err != nil || !ok {
		t.Fatalf("report spam %v %v", ok, err)
	}
	s.db.Pool.QueryRow(bg, "SELECT snapshot::text FROM content_reports WHERE kind='spam' AND chat_kind='user'").Scan(&snap)
	if !strings.Contains(snap, "free crypto here") {
		t.Fatalf("spam snapshot %s", snap)
	}

	// the reporter can't tell anything about the target from the answers:
	// an active, a frozen and a locked account, over the limit or not, all
	// answer the same
	frozen := s.signUp(t, "frozen1")
	locked := s.signUp(t, "locked1")
	for _, x := range []struct {
		id    int64
		state string
	}{{frozen.id, "frozen"}, {locked.id, "locked"}} {
		if code, out := boss.call("PATCH", fmt.Sprintf("/api/admin/users/%d/access", x.id), map[string]any{"state": x.state}); code != 200 {
			t.Fatalf("%s: %d %v", x.state, code, out)
		}
	}
	dave := s.signUp(t, "dave")
	dc := s.live(t, dave, make(chan tg.UpdatesClass, 64))
	shape := func(list []tg.ReportResultClass) string {
		raw, _ := json.Marshal(list)
		return string(raw)
	}
	var want string
	for i, target := range []int64{carol.id, frozen.id, locked.id} {
		steps, err := reportAll(ctx, dc, userPeer(target), nil, "", "personal_data", "phone")
		if err != nil {
			t.Fatalf("report %d: %v", target, err)
		}
		ok, err := dc.AccountReportPeer(ctx, &tg.AccountReportPeerRequest{Peer: userPeer(target), Reason: &tg.InputReportReasonSpam{}})
		if err != nil || !ok {
			t.Fatalf("report peer %d: %v %v", target, ok, err)
		}
		if i == 0 {
			want = shape(steps)
		} else if got := shape(steps); got != want {
			t.Fatalf("answers differ for %d:\n%s\n%s", target, got, want)
		}
	}
	if n := reportRows(t, s, "reporter_id=$1", dave.id); n != 6 {
		t.Fatalf("dave filed %d", n)
	}
	if _, err := s.db.Pool.Exec(bg, `INSERT INTO content_reports(reporter_id, kind, chat_kind, chat_id, target_key, target_type, reason)
		SELECT $1::bigint, 'peer', 'user', $2::bigint, $3::text, 'user', '{other}' FROM generate_series(1, $4::int)`, dave.id, carol.id, reports.UserKey(carol.id), reports.PerHour); err != nil {
		t.Fatal(err)
	}
	over, err := reportAll(ctx, dc, userPeer(bob.id), nil, "", "personal_data", "phone")
	if err != nil || shape(over) != want {
		t.Fatalf("over the limit: %s %v", shape(over), err)
	}
	if ok, err := dc.AccountReportPeer(ctx, &tg.AccountReportPeerRequest{Peer: userPeer(bob.id), Reason: &tg.InputReportReasonSpam{}}); err != nil || !ok {
		t.Fatalf("report peer over the limit %v %v", ok, err)
	}
	if n := reportRows(t, s, "reporter_id=$1 AND target_key=$2", dave.id, reports.UserKey(bob.id)); n != 0 {
		t.Fatalf("over the limit and stored %d", n)
	}
	if _, err := s.db.Pool.Exec(bg, "DELETE FROM content_reports WHERE reporter_id=$1", dave.id); err != nil {
		t.Fatal(err)
	}
	// nobody tells bob
	var told int
	s.db.Pool.QueryRow(bg, "SELECT count(*) FROM messages WHERE owner_id=$1 AND NOT out AND (text ILIKE '%report%')", bob.id).Scan(&told)
	if told != 0 {
		t.Fatalf("bob was told %d times", told)
	}

	// the panel
	member := s.browser(t)
	member.must("/api/register", map[string]string{"username": "mallory", "name": "Mallory", "password": "correct horse mallory"})
	if code, _ := member.call("GET", "/api/admin/reports", nil); code != 403 {
		t.Fatalf("a member read the reports: %d", code)
	}
	code, list := boss.call("GET", "/api/admin/reports", nil)
	if code != 200 {
		t.Fatalf("list %d %v", code, list)
	}
	bg1 := groupOf(t, list, reports.UserKey(bob.id))
	if bg1["open"].(float64) < 6 || bg1["reporters"].(float64) != 2 || bg1["canFreeze"] != true || bg1["canBan"] != true || bg1["canDelete"] != true {
		t.Fatalf("bob's group %v", bg1)
	}
	raw, _ := json.Marshal(bg1)
	if !strings.Contains(string(raw), "buy cheap pills") || !strings.Contains(string(raw), "dm me your card") || strings.Contains(string(raw), "nothing to see") {
		t.Fatalf("snapshots changed: %s", raw)
	}
	if !strings.Contains(string(raw), "Spam or scam › Phishing") || !strings.Contains(string(raw), "Violence › Hate speech or symbols") {
		t.Fatalf("reason labels: %s", raw)
	}
	if code, l := boss.call("GET", "/api/admin/reports?target=channel", nil); code != 200 || len(l["groups"].([]any)) != 1 {
		t.Fatalf("channel filter %v", l)
	} else if g := l["groups"].([]any)[0].(map[string]any); g["key"] != reports.ChatKey(news.ID) || g["canFreeze"] != false {
		t.Fatalf("channel group %v", g)
	}
	if code, l := boss.call("GET", "/api/admin/reports?reason=copyright", nil); code != 200 || len(l["groups"].([]any)) != 2 {
		t.Fatalf("reason filter %v", l)
	}
	var overview map[string]any
	_, overview = boss.call("GET", "/api/admin", nil)
	if overview["openReports"].(float64) < 8 {
		t.Fatalf("openReports %v", overview["openReports"])
	}

	// the channel: dismissed
	newsGroup := groupOf(t, list, reports.ChatKey(news.ID))
	newsReport := int64(newsGroup["reports"].([]any)[0].(map[string]any)["id"].(float64))
	if code, out := boss.call("POST", fmt.Sprintf("/api/admin/reports/%d/resolve", newsReport), map[string]any{"freeze": true}); code != 400 {
		t.Fatalf("froze a channel: %d %v", code, out)
	}
	if code, out := boss.call("POST", fmt.Sprintf("/api/admin/reports/%d/dismiss", newsReport), map[string]any{"note": "fair use"}); code != 200 {
		t.Fatalf("dismiss %d %v", code, out)
	}
	if code, _ := boss.call("POST", fmt.Sprintf("/api/admin/reports/%d/dismiss", newsReport), nil); code != 409 {
		t.Fatal("dismissed twice")
	}

	// bob: banned from the group, messages gone everywhere, frozen; one
	// report filed after the admin looked stays open
	var ids []any
	for _, r := range bg1["reports"].([]any) {
		ids = append(ids, r.(map[string]any)["id"])
	}
	first := int64(ids[0].(float64))
	send(ctx, t, bc, userPeer(ada.id), "still here", 103)
	bh2, _ := history(ctx, ac, bob.id)
	if _, err := reportAll(ctx, ac, userPeer(bob.id), []int{bh2[0].ID}, "", "spam", "spam"); err != nil {
		t.Fatal(err)
	}
	code, out := boss.call("POST", fmt.Sprintf("/api/admin/reports/%d/resolve", first), map[string]any{"delete": true, "freeze": true, "ban": true, "note": "spam wave", "reports": ids})
	if code != 200 {
		t.Fatalf("resolve %d %v", code, out)
	}
	if n := reportRows(t, s, "target_key=$1 AND status='open'", reports.UserKey(bob.id)); n != 1 {
		t.Fatalf("%d left open on bob", n)
	}
	var state, status string
	s.db.Pool.QueryRow(bg, "SELECT account_state FROM users WHERE id=$1", bob.id).Scan(&state)
	s.db.Pool.QueryRow(bg, "SELECT status FROM channel_members WHERE channel_id=$1 AND user_id=$2", group.ID, bob.id).Scan(&status)
	if state != "frozen" || status != channels.Kicked {
		t.Fatalf("bob %s, in the group %s", state, status)
	}
	msgs, _, err := channelHistory(ctx, cc, group)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Message == "hate in the group" || m.Message == "more of it" {
			t.Fatalf("%q still in the group", m.Message)
		}
	}
	var left int
	s.db.Pool.QueryRow(bg, "SELECT count(*) FROM messages WHERE text IN ('buy cheap pills', 'nothing to see', 'free crypto here')").Scan(&left)
	if left != 0 {
		t.Fatalf("%d private copies left", left)
	}
	s.db.Pool.QueryRow(bg, "SELECT count(*) FROM messages WHERE text = 'still here'").Scan(&left)
	if left != 2 {
		t.Fatalf("the unseen report's message went too: %d", left)
	}
	if _, err := bc.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(ada.id), Message: "hi", RandomID: 104}); !tgerr.Is(err, "FROZEN_METHOD_INVALID") {
		t.Fatalf("frozen bob sends: %v", err)
	}
	// and can appeal
	sb := resolve(ctx, t, bc, "spambot")
	say(ctx, t, bc, sb, "/start")
	m := waitFrom(t, bobPush, sb.ID, "Your account was frozen")
	if _, err := bc.MessagesGetBotCallbackAnswer(ctx, &tg.MessagesGetBotCallbackAnswerRequest{Peer: &tg.InputPeerUser{UserID: sb.ID, AccessHash: sb.AccessHash}, MsgID: m.ID, Data: buttonData(t, m)}); err != nil {
		t.Fatal(err)
	}
	waitFrom(t, bobPush, sb.ID, "Tell us why")
	say(ctx, t, bc, sb, "it was a joke")
	waitFrom(t, bobPush, sb.ID, "Thank you")
	if code, out := boss.call("GET", "/api/admin/appeals", nil); code != 200 || len(out["appeals"].([]any)) != 1 {
		t.Fatalf("appeals %v", out)
	}

	for _, action := range []string{"report_ban", "report_delete_messages", "account_frozen", "reports_resolved", "reports_dismissed"} {
		var n int
		s.db.Pool.QueryRow(bg, "SELECT count(*) FROM audit_log WHERE action=$1 AND actor_id=$2", action, bossID).Scan(&n)
		if n == 0 {
			t.Fatalf("no %s in the audit log", action)
		}
	}
	code, closed := boss.call("GET", "/api/admin/reports?status=closed", nil)
	cg := groupOf(t, closed, reports.UserKey(bob.id))
	r0 := cg["reports"].([]any)[0].(map[string]any)
	if code != 200 || r0["status"] != "resolved" || r0["closedBy"] != "Boss" || len(r0["actions"].([]any)) != 3 {
		t.Fatalf("closed %v", r0)
	}
	if code, _ := boss.call("POST", fmt.Sprintf("/api/admin/reports/%d/resolve", first), map[string]any{"delete": true, "reports": ids}); code != 409 {
		t.Fatalf("resolved twice: %d", code)
	}
	// the owner of a group can't be banned from it
	if _, err := reportAll(ctx, ac, peerCh(group), []int{carolSaid}, "", "violence", "insults"); err != nil {
		t.Fatal(err)
	}
	var carolReport int64
	s.db.Pool.QueryRow(bg, "SELECT id FROM content_reports WHERE target_key=$1", reports.UserKey(carol.id)).Scan(&carolReport)
	if _, l := boss.call("GET", "/api/admin/reports", nil); groupOf(t, l, reports.UserKey(carol.id))["canBan"] != false {
		t.Fatal("offered to ban the group's owner")
	}
	if code, out := boss.call("POST", fmt.Sprintf("/api/admin/reports/%d/resolve", carolReport), map[string]any{"ban": true}); code != 409 {
		t.Fatalf("banned the owner: %d %v", code, out)
	}
}

// A wave of reports is one notice per gap, not one per report.
func TestReportNotices(t *testing.T) {
	s := start(t)
	s.reports.SetNoticeGap(3 * time.Second)
	boss := s.browser(t)
	head := boss.must("/api/register", map[string]string{"username": "boss", "name": "Boss", "password": "correct horse boss"})
	bossID := int64(head["id"].(float64))
	bg := context.Background()
	ctx, cancel := context.WithTimeout(bg, 2*time.Minute)
	defer cancel()
	var people []person
	for i := range 6 {
		people = append(people, s.signUp(t, fmt.Sprintf("user%d", i)))
	}
	spammer := s.signUp(t, "spammer")
	notices := func() []string {
		rows, err := s.db.Pool.Query(bg, "SELECT text FROM messages WHERE owner_id=$1 AND NOT out AND text LIKE '%Review them in the panel%' ORDER BY id", bossID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			rows.Scan(&s)
			out = append(out, s)
		}
		return out
	}
	waitNotices := func(n int, within time.Duration) []string {
		t.Helper()
		deadline := time.Now().Add(within)
		for time.Now().Before(deadline) {
			if got := notices(); len(got) >= n {
				return got
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("%d notices, want %d", len(notices()), n)
		return nil
	}
	report := func(p person) {
		t.Helper()
		run(t, s.client(p.storage), func(ctx context.Context, c *tg.Client) error {
			ok, err := c.AccountReportPeer(ctx, &tg.AccountReportPeerRequest{Peer: userPeer(spammer.id), Reason: &tg.InputReportReasonSpam{}})
			if err == nil && !ok {
				err = fmt.Errorf("not ok")
			}
			return err
		})
	}
	report(people[0])
	first := waitNotices(1, 10*time.Second)
	if !strings.HasPrefix(first[0], "New report.") || !strings.Contains(first[0], "spammer") || !strings.Contains(first[0], "/admin#reports") {
		t.Fatalf("first notice %q", first[0])
	}
	start := time.Now()
	for _, p := range people[1:] {
		report(p)
	}
	if time.Since(start) < 2*time.Second && len(notices()) != 1 {
		t.Fatalf("a notice per report: %d", len(notices()))
	}
	got := waitNotices(2, 15*time.Second)
	if !strings.HasPrefix(got[1], "5 new reports.") || !strings.Contains(got[1], "(5 reports)") {
		t.Fatalf("batched notice %q", got[1])
	}
	time.Sleep(4 * time.Second)
	if n := len(notices()); n != 2 {
		t.Fatalf("%d notices for 6 reports", n)
	}
	_ = ctx
}

// TestModerationRemoveMessage: the Moderation page's Remove message takes
// one message off the recent feed for everyone, with an audit row.
func TestModerationRemoveMessage(t *testing.T) {
	s := start(t)
	boss := s.browser(t)
	boss.must("/api/register", map[string]string{"username": "boss", "name": "Boss", "password": "correct horse boss"})
	s.mountReports()
	ada := s.signUp(t, "ada")
	bob := s.signUp(t, "bob")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adaPush := make(chan tg.UpdatesClass, 64)
	bobPush := make(chan tg.UpdatesClass, 64)
	ac := s.live(t, ada, adaPush)
	bc := s.live(t, bob, bobPush)

	send(ctx, t, bc, userPeer(ada.id), "rude words", 901)
	send(ctx, t, bc, userPeer(ada.id), "kind words", 902)
	res, err := bc.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: true, Title: "Board"})
	if err != nil {
		t.Fatal(err)
	}
	board := channelOf(t, res)
	if _, err := bc.ChannelsInviteToChannel(ctx, &tg.ChannelsInviteToChannelRequest{Channel: inCh(board), Users: []tg.InputUserClass{&tg.InputUser{UserID: ada.id}}}); err != nil {
		t.Fatal(err)
	}
	send(ctx, t, bc, peerCh(board), "rude post", 903)
	send(ctx, t, bc, peerCh(board), "kind post", 904)

	_, overview := boss.call("GET", "/api/admin", nil)
	feed := map[string]string{}
	for _, m := range overview["recent"].([]any) {
		m := m.(map[string]any)
		feed[m["body"].(string)] = m["id"].(string)
	}
	if feed["rude words"] == "" || feed["rude post"] == "" {
		t.Fatalf("feed %v", feed)
	}
	for _, body := range []string{"rude words", "rude post"} {
		if code, out := boss.call("DELETE", "/api/admin/messages/"+feed[body], map[string]any{}); code != 200 {
			t.Fatalf("remove %q: %d %v", body, code, out)
		}
	}
	// twice, or nonsense: not found
	for _, id := range []string{feed["rude words"], feed["rude post"], "x1", "c1_", "p0"} {
		if code, _ := boss.call("DELETE", "/api/admin/messages/"+id, map[string]any{}); code != 404 {
			t.Fatalf("remove %q again: %d", id, code)
		}
	}
	// gone from both sides of the private chat and from the channel, the rest stays
	for _, c := range []*tg.Client{ac, bc} {
		peer := bob.id
		if c == bc {
			peer = ada.id
		}
		h, err := history(ctx, c, peer)
		if err != nil || len(h) != 1 || h[0].Message != "kind words" {
			t.Fatalf("private history %+v %v", h, err)
		}
		ch, _, err := channelHistory(ctx, c, board)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ch {
			if m.Message == "rude post" {
				t.Fatalf("post still there")
			}
		}
	}
	var n int
	s.db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE action='delete_message' AND details->>'message' = ANY($1)",
		[]string{feed["rude words"], feed["rude post"]}).Scan(&n)
	if n != 2 {
		t.Fatalf("%d audit rows", n)
	}
	// someone who isn't an admin can't
	if code, _ := s.browser(t).call("DELETE", "/api/admin/messages/"+feed["kind words"], map[string]any{}); code < 400 {
		t.Fatalf("signed out: %d", code)
	}
}
