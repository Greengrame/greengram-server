package e2e

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"testing"

	"github.com/iamxvbaba/td/session"
	"github.com/iamxvbaba/td/tg"

	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/importer"
	wf "github.com/Greengrame/greengram-server/internal/importer/webfixture"
	"github.com/Greengrame/greengram-server/internal/secret"
)

func (b *browser) get(path string) (int, map[string]any) {
	b.t.Helper()
	resp, err := b.c.Get(b.base + path)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func msgList(t *testing.T, res tg.MessagesMessagesClass) ([]tg.MessageClass, int) {
	t.Helper()
	switch v := res.(type) {
	case *tg.MessagesMessages:
		return v.Messages, len(v.Messages)
	case *tg.MessagesMessagesSlice:
		return v.Messages, v.Count
	case *tg.MessagesChannelMessages:
		return v.Messages, v.Count
	}
	t.Fatalf("history %T", res)
	return nil, 0
}

func texts(list []tg.MessageClass) []string {
	var out []string
	for _, m := range list {
		switch v := m.(type) {
		case *tg.Message:
			out = append(out, v.Message)
		case *tg.MessageService:
			if a, ok := v.Action.(*tg.MessageActionCustomAction); ok {
				out = append(out, "service:"+a.Message)
			}
		}
	}
	return out
}

func chanPeer(id int64) *tg.InputPeerChannel {
	return &tg.InputPeerChannel{ChannelID: id, AccessHash: channels.AccessHash(id)}
}

// TestImportFromWeb imports a greengram-web database and then uses it the
// way people will on the morning after the switch.
func TestImportFromWeb(t *testing.T) {
	st := db(t)
	ctx := context.Background()
	inst := make([]byte, 32)
	rand.Read(inst)
	path := filepath.Join(t.TempDir(), "greengram.sqlite3")
	wf.Build(t, path, secret.FromBytes(inst))
	opts := importer.Options{SQLite: path, DB: st.Pool, Secret: secret.FromBytes(inst), Log: t.Logf}

	// a dry run against the real target writes nothing
	dry := opts
	dry.DryRun = true
	if _, err := importer.Run(ctx, dry); err != nil {
		t.Fatal(err)
	}
	var n int
	st.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM users) + (SELECT count(*) FROM import_stages)").Scan(&n)
	if n != 0 {
		t.Fatalf("dry run wrote %d rows", n)
	}

	if _, err := importer.Run(ctx, opts); err != nil {
		t.Fatal(err)
	}
	// again: every stage is already done
	rep, err := importer.Run(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range rep.Stages {
		if !s.Done {
			t.Fatalf("second run redid %s", s.Name)
		}
	}
	// a resumed run picks up after the last stage that finished
	st.Pool.Exec(ctx, "DELETE FROM import_stages WHERE stage IN ('channel', 'finish')")
	st.Pool.Exec(ctx, "DELETE FROM channel_messages; DELETE FROM import_channel_messages")
	rep, err = importer.Run(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Stages[6].Done != true || rep.Stages[7].Done || rep.Stages[7].Counts["messages"] != 6 {
		t.Fatalf("resume: %+v", rep.Stages)
	}
	// data but no record of this import: refuse, unless forced
	st.Pool.Exec(ctx, "DELETE FROM import_stages")
	if _, err := importer.Run(ctx, opts); err == nil {
		t.Fatal("imported into a non-empty target without --force")
	}
	forced := opts
	forced.Force = true
	if _, err := importer.Run(ctx, forced); err != nil {
		t.Fatal(err)
	}

	s := startOn(t, st, inst)

	// an erased account stays erased: the admin panel refuses to restore it
	var state string
	if err := st.Pool.QueryRow(ctx, "SELECT account_state FROM users WHERE id=$1", wf.Gone).Scan(&state); err != nil || state != "deleted" {
		t.Fatalf("erased account imported as %q (%v)", state, err)
	}
	// the web's BotFather is still BotFather, with this server's texts
	var kind, about string
	if err := st.Pool.QueryRow(ctx, "SELECT kind, description FROM bots WHERE user_id=$1", wf.BotFather).Scan(&kind, &about); err != nil || kind != "botfather" || about == "old text" {
		t.Fatalf("imported BotFather: kind=%q description=%q err=%v", kind, about, err)
	}

	// Ada signs in by phone with the password she had on the web
	ada := s.browser(t)
	ada.must("/api/auth/start", map[string]string{"mode": "phone"})
	if out := ada.must("/api/auth/code", map[string]string{"phone": wf.AdaPhone}); out["state"] != "password" {
		t.Fatalf("code: %v", out)
	}
	if code, _ := ada.post("/api/auth/password", map[string]string{"password": "not her password"}); code != 401 {
		t.Fatalf("wrong password: %d", code)
	}
	out := ada.must("/api/auth/password", map[string]string{"password": wf.AdaPassword})
	user := out["user"].(map[string]any)
	if user["id"].(float64) != wf.Ada || user["phoneNumber"] != wf.AdaPhone || user["role"] != "admin" || user["bio"] != "hi there" {
		t.Fatalf("ada after sign-in: %v", out)
	}
	// her web cookie from before the switch still works
	req, _ := http.NewRequest("GET", s.http.URL+"/api/me", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: wf.AdaToken})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("old session cookie: %d", resp.StatusCode)
	}
	// Bob had a username account
	bob := s.browser(t)
	if u := bob.must("/api/login", map[string]string{"username": "bob", "password": wf.BobPassword}); u["id"].(float64) != wf.Bob {
		t.Fatalf("bob: %v", u)
	}
	// erased and system accounts can't
	if code, _ := s.browser(t).post("/api/login", map[string]string{"username": "instance_premium_payments", "password": "!system-account"}); code == 200 {
		t.Fatal("system account signed in")
	}
	// new accounts continue after the imported ids
	// (built-in bots made at startup may sit in between)
	if u := s.browser(t).must("/api/register", map[string]string{"username": "newbie", "name": "New", "password": "correct horse 9"}); u["id"].(float64) <= wf.System {
		t.Fatalf("new account id %v", u["id"])
	}

	// old message links
	if code, l := bob.get("/api/messages/104/link"); code != 200 || l["peerId"] != "1" || l["messageId"].(float64) != 2 {
		t.Fatalf("bob's link to 104: %d %v", code, l)
	}
	if code, l := ada.get("/m/105"); code != 200 || l["peerId"] != "-1000000000013" || l["messageId"].(float64) != 1 {
		t.Fatalf("ada's link to the channel post: %d %v", code, l)
	}
	if code, _ := ada.get("/api/messages/111/link"); code != 404 {
		t.Fatalf("someone else's message: %d", code)
	}

	adaApp := &session.StorageMemory{}
	run(t, s.client(adaApp), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.AuthImportAuthorization(ctx, &tg.AuthImportAuthorizationRequest{ID: wf.Ada, Bytes: []byte(ada.session())}); err != nil {
			return err
		}
		res, err := c.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 50})
		if err != nil {
			return err
		}
		var list []tg.DialogClass
		switch v := res.(type) {
		case *tg.MessagesDialogs:
			list = v.Dialogs
		case *tg.MessagesDialogsSlice:
			list = v.Dialogs
		}
		dialogs := map[string]*tg.Dialog{}
		for _, d := range list {
			d := d.(*tg.Dialog)
			switch p := d.Peer.(type) {
			case *tg.PeerUser:
				dialogs[fmt.Sprint("user", p.UserID)] = d
			case *tg.PeerChannel:
				dialogs[fmt.Sprint("channel", p.ChannelID)] = d
			}
		}
		if len(dialogs) != 5 {
			return fmt.Errorf("dialogs %v", dialogs)
		}
		withBob := dialogs["user2"]
		if withBob == nil || withBob.TopMessage != 6 || withBob.UnreadCount != 1 || withBob.ReadInboxMaxID != 4 || withBob.ReadOutboxMaxID != 2 || !withBob.Pinned {
			return fmt.Errorf("dialog with bob %+v", withBob)
		}
		if d := dialogs["user1"]; d == nil || d.TopMessage != 1 || d.UnreadCount != 0 {
			return fmt.Errorf("saved messages %+v", d)
		}
		if d := dialogs["channel13"]; d == nil || !d.Pinned {
			return fmt.Errorf("news channel %+v", d)
		}
		if d := dialogs["channel12"]; d == nil || d.TopMessage != 3 || d.UnreadCount != 0 || d.ReadOutboxMaxID != 3 {
			return fmt.Errorf("team %+v", d)
		}

		h, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: &tg.InputPeerUser{UserID: wf.Bob}, Limit: 50})
		if err != nil {
			return err
		}
		msgs, _ := msgList(t, h)
		if got, want := texts(msgs), []string{"unread one", "fwd text", "edited later", "hi ada", "hi bob"}; !slices.Equal(got, want) {
			return fmt.Errorf("history with bob %q", got)
		}
		hiAda := msgs[3].(*tg.Message)
		reply, _ := hiAda.ReplyTo.(*tg.MessageReplyHeader)
		if hiAda.ID != 3 || hiAda.Out || reply == nil || reply.ReplyToMsgID != 2 || len(hiAda.Entities) != 1 {
			return fmt.Errorf("hi ada %+v", hiAda)
		}
		if _, ok := hiAda.Entities[0].(*tg.MessageEntityBold); !ok {
			return fmt.Errorf("entity %+v", hiAda.Entities[0])
		}
		if m := msgs[2].(*tg.Message); m.EditDate != wf.T0+500 || m.Date != wf.T0+108 {
			return fmt.Errorf("edited %+v", m)
		}
		fwd := msgs[1].(*tg.Message)
		if from, ok := fwd.FwdFrom.FromID.(*tg.PeerUser); !fwd.Out || !ok || from.UserID != wf.Carol || fwd.FwdFrom.Date != 1690000000 {
			return fmt.Errorf("forward %+v", fwd)
		}

		h, err = c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: chanPeer(wf.Team), Limit: 50})
		if err != nil {
			return err
		}
		msgs, count := msgList(t, h)
		if got, want := texts(msgs), []string{"service:Ada pinned a message", "welcome", "hello team"}; count != 3 || !slices.Equal(got, want) {
			return fmt.Errorf("team history %q (%d)", got, count)
		}
		if r, _ := msgs[1].(*tg.Message).ReplyTo.(*tg.MessageReplyHeader); r == nil || r.ReplyToMsgID != 1 {
			return fmt.Errorf("welcome reply %+v", msgs[1])
		}

		parts, err := c.ChannelsGetParticipants(ctx, &tg.ChannelsGetParticipantsRequest{
			Channel: &tg.InputChannel{ChannelID: wf.Team, AccessHash: channels.AccessHash(wf.Team)}, Filter: &tg.ChannelParticipantsRecent{}, Limit: 100})
		if err != nil {
			return err
		}
		roles := map[int64]string{}
		for _, p := range parts.(*tg.ChannelsChannelParticipants).Participants {
			switch v := p.(type) {
			case *tg.ChannelParticipantCreator:
				roles[v.UserID] = "creator"
			case *tg.ChannelParticipantAdmin:
				roles[v.UserID] = "admin"
			case *tg.ChannelParticipant:
				roles[v.UserID] = "member"
			case *tg.ChannelParticipantSelf:
				roles[v.UserID] = "member"
			}
		}
		if len(roles) != 3 || roles[wf.Ada] != "creator" || roles[wf.Carol] != "admin" || roles[wf.Bob] != "member" {
			return fmt.Errorf("team members %v", roles)
		}

		h, err = c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: chanPeer(wf.NewsChat), Limit: 50})
		if err != nil {
			return err
		}
		msgs, _ = msgList(t, h)
		if got := texts(msgs); !slices.Equal(got, []string{"nice post", "first post"}) {
			return fmt.Errorf("news chat %q", got)
		}
		cp := msgs[1].(*tg.Message)
		if from, ok := cp.FwdFrom.FromID.(*tg.PeerChannel); !ok || from.ChannelID != wf.News || cp.FwdFrom.ChannelPost != 1 {
			return fmt.Errorf("discussion copy %+v", cp)
		}
		if r, _ := msgs[0].(*tg.Message).ReplyTo.(*tg.MessageReplyHeader); r == nil || r.ReplyToMsgID != 1 {
			return fmt.Errorf("comment %+v", msgs[0])
		}
		h, err = c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: chanPeer(wf.News), Limit: 50})
		if err != nil {
			return err
		}
		msgs, _ = msgList(t, h)
		if len(msgs) != 1 || !msgs[0].(*tg.Message).Post {
			return fmt.Errorf("news %+v", msgs)
		}

		contacts, err := c.ContactsGetContacts(ctx, 0)
		if err != nil {
			return err
		}
		var who []int64
		for _, ct := range contacts.(*tg.ContactsContacts).Contacts {
			who = append(who, ct.UserID)
		}
		slices.Sort(who)
		if !slices.Equal(who, []int64{wf.Bob, wf.Carol}) {
			return fmt.Errorf("contacts %v", who)
		}

		p, err := c.AccountGetPrivacy(ctx, &tg.InputPrivacyKeyPhoneNumber{})
		if err != nil {
			return err
		}
		var kinds []string
		for _, r := range p.Rules {
			switch v := r.(type) {
			case *tg.PrivacyValueAllowUsers:
				kinds = append(kinds, fmt.Sprint("users", v.Users))
			case *tg.PrivacyValueAllowChatParticipants:
				kinds = append(kinds, fmt.Sprint("chats", v.Chats))
			case *tg.PrivacyValueDisallowAll:
				kinds = append(kinds, "nobody")
			default:
				kinds = append(kinds, fmt.Sprintf("%T", v))
			}
		}
		if !slices.Equal(kinds, []string{"users[2]", "chats[12]", "nobody"}) {
			return fmt.Errorf("phone privacy %v", kinds)
		}
		return nil
	})

	// Bob writes after the switch; ids go on from the imported ones
	bobApp := &session.StorageMemory{}
	run(t, s.client(bobApp), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.AuthImportAuthorization(ctx, &tg.AuthImportAuthorizationRequest{ID: wf.Bob, Bytes: []byte(bob.session())}); err != nil {
			return err
		}
		if _, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: &tg.InputPeerUser{UserID: wf.Ada}, Message: "after the switch", RandomID: 77}); err != nil {
			return err
		}
		// forwards of a channel post and of someone hiding their name
		h, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: &tg.InputPeerUser{UserID: wf.Carol}, Limit: 2})
		if err != nil {
			return err
		}
		msgs, _ := msgList(t, h)
		hidden, post := msgs[0].(*tg.Message), msgs[1].(*tg.Message)
		if hidden.Message != "fwd hidden" || hidden.FwdFrom.FromName != "Someone Else" || hidden.FwdFrom.FromID != nil || hidden.FwdFrom.Date != 1690000001 {
			return fmt.Errorf("hidden forward %+v", hidden)
		}
		if from, ok := post.FwdFrom.FromID.(*tg.PeerChannel); !ok || from.ChannelID != wf.News || post.FwdFrom.ChannelPost != 1 {
			return fmt.Errorf("post forward %+v", post)
		}
		var news bool
		for _, ch := range h.(*tg.MessagesMessagesSlice).Chats {
			news = news || ch.GetID() == wf.News
		}
		if !news {
			return fmt.Errorf("no news channel with the post forward")
		}
		// carol blocked bob on the web
		if _, err = c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: &tg.InputPeerUser{UserID: wf.Carol}, Message: "hello?", RandomID: 78}); err != nil {
			return fmt.Errorf("a blocked send should look like it worked: %v", err)
		}
		var copies int
		if err := s.db.Pool.QueryRow(ctx, "SELECT count(*) FROM messages WHERE owner_id=$1 AND text='hello?'", wf.Carol).Scan(&copies); err != nil {
			return err
		}
		if copies != 0 {
			return fmt.Errorf("carol blocked bob on the web and got his message")
		}
		return nil
	})
	run(t, s.client(adaApp), func(ctx context.Context, c *tg.Client) error {
		h, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: &tg.InputPeerUser{UserID: wf.Bob}, Limit: 1})
		if err != nil {
			return err
		}
		msgs, _ := msgList(t, h)
		if m := msgs[0].(*tg.Message); m.ID != 7 || m.Message != "after the switch" {
			return fmt.Errorf("new message %+v", m)
		}
		return nil
	})
	var shared int64
	st.Pool.QueryRow(ctx, "SELECT shared_id FROM messages WHERE owner_id=$1 AND id=7", wf.Ada).Scan(&shared)
	if shared <= 118 {
		t.Fatalf("new shared_id %d collides with imported ids", shared)
	}
}
