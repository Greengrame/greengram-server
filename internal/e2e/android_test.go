package e2e

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/mtproto"
	"github.com/iamxvbaba/td/session"
	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/telegram/dcs"
	"github.com/iamxvbaba/td/telegram/uploader"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"github.com/iamxvbaba/td/transport"

	"github.com/Greengrame/greengram-server/internal/stars"
)

// rawObj is a request already in wire form, as the Android app writes it.
type rawObj []byte

func (r rawObj) Encode(b *bin.Buffer) error { b.Put(r); return nil }
func (r rawObj) Decode(*bin.Buffer) error   { return errors.New("raw request") }

type rawReply struct{ buf []byte }

func (r *rawReply) Decode(b *bin.Buffer) error {
	r.buf = append([]byte(nil), b.Buf...)
	b.Skip(b.Len())
	return nil
}

func withLayer(layer int, body []byte) []byte {
	var b bin.Buffer
	b.PutID(tg.InvokeWithLayerRequestTypeID)
	b.PutInt(layer)
	b.Put(body)
	return b.Buf
}

// droid talks like the Android app: every call says layer 231, answers are
// read at 231. It sits on a bare MTProto connection with the person's key,
// since td's client wraps every call in its own invokeWithLayer.
type droid struct {
	ctx context.Context
	c   interface {
		Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error
	}
}

type quietHandler struct{}

func (quietHandler) OnMessage(*bin.Buffer) error       { return nil }
func (quietHandler) OnSession(mtproto.Session) error { return nil }

func (d droid) send(body []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(d.ctx, 15*time.Second)
	defer cancel()
	var out rawReply
	err := d.c.Invoke(ctx, rawObj(body), &out)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("no answer to % x: %w", body[:min(len(body), 16)], err)
	}
	return out.buf, err
}

func (d droid) raw(body []byte) ([]byte, error) { return d.send(withLayer(231, body)) }

func decode231(raw []byte) (bin.Object, error) {
	return tlprofile.DecodeObject(tlprofile.Profile231, &bin.Buffer{Buf: raw}, tlprofile.Limits{})
}

func (d droid) obj(body []byte) (bin.Object, error) {
	raw, err := d.raw(body)
	if err != nil {
		return nil, err
	}
	return decode231(raw)
}

// call sends a request the way a layer 231 client encodes it.
func (d droid) call(req bin.Object) (bin.Object, error) {
	var b bin.Buffer
	if err := tlprofile.EncodeObject(tlprofile.Profile231, req, &b); err != nil {
		return nil, err
	}
	return d.obj(b.Buf)
}

func (s *stack) android(t *testing.T, p person, f func(d droid) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	data, err := (&session.Loader{Storage: p.storage}).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var key crypto.AuthKey
	copy(key.Value[:], data.AuthKey)
	copy(key.ID[:], data.AuthKeyID)
	res := dcs.Plain(dcs.PlainOptions{Protocol: transport.Intermediate})
	list := dcs.List{Options: []tg.DCOption{{ID: dc, IPAddress: "127.0.0.1", Port: s.tcp}}}
	conn := mtproto.New(func(ctx context.Context) (transport.Conn, error) { return res.Primary(ctx, dc, list) }, mtproto.Options{
		DC: dc, PublicKeys: []exchange.PublicKey{{RSA: &s.key.PublicKey}}, Key: key, Salt: data.Salt, Handler: quietHandler{},
	})
	// a bare connection runs until its context ends, not when f returns
	runCtx, stop := context.WithCancel(ctx)
	var ferr error
	done := false
	err = conn.Run(runCtx, func(ctx context.Context) error {
		ferr = f(droid{ctx: ctx, c: conn})
		done = true
		stop()
		return nil
	})
	if ferr != nil {
		t.Fatal(ferr)
	}
	if !done {
		t.Fatal(err)
	}
}

func runBoth(t *testing.T, s *stack, p person, f func(context.Context, *telegram.Client, *tg.Client) error) {
	t.Helper()
	c := s.client(p.storage)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := c.Run(ctx, func(ctx context.Context) error { return f(ctx, c, c.API()) }); err != nil {
		t.Fatal(err)
	}
}

func wantRPCErr(err error, kind string) error {
	if !tgerr.Is(err, kind) {
		return fmt.Errorf("want %s, got %v", kind, err)
	}
	return nil
}

func body(id uint32, f func(b *bin.Buffer)) []byte {
	var b bin.Buffer
	b.PutID(id)
	if f != nil {
		f(&b)
	}
	return b.Buf
}

func putObj(b *bin.Buffer, o bin.Encoder) {
	if err := o.Encode(b); err != nil {
		panic(err)
	}
}

func intVector(b *bin.Buffer, ids ...int) {
	b.PutVectorHeader(len(ids))
	for _, id := range ids {
		b.PutInt(id)
	}
}

func messageTexts(o bin.Object) []string {
	var list []tg.MessageClass
	switch v := o.(type) {
	case *tg.MessagesMessages:
		list = v.Messages
	case *tg.MessagesMessagesSlice:
		list = v.Messages
	case *tg.MessagesChannelMessages:
		list = v.Messages
	case *tg.UpdatesDifference:
		list = v.NewMessages
	case *tg.UpdatesDifferenceSlice:
		list = v.NewMessages
	}
	var out []string
	for _, m := range list {
		if m, ok := m.(*tg.Message); ok {
			out = append(out, m.Message)
		}
	}
	return out
}

// newsChannel makes alice's broadcast channel with one post.
func newsChannel(ctx context.Context, c *tg.Client, title string) (*tg.Channel, int, error) {
	res, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: true, Title: title})
	if err != nil {
		return nil, 0, err
	}
	var ch *tg.Channel
	for _, x := range res.(*tg.Updates).Chats {
		if v, ok := x.(*tg.Channel); ok {
			ch = v
		}
	}
	if ch == nil {
		return nil, 0, fmt.Errorf("no channel in %+v", res)
	}
	up, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(ch), Message: "first post", RandomID: time.Now().UnixNano()})
	if err != nil {
		return nil, 0, err
	}
	for _, u := range up.(*tg.Updates).Updates {
		if n, ok := u.(*tg.UpdateNewChannelMessage); ok {
			return ch, n.Message.GetID(), nil
		}
	}
	return nil, 0, fmt.Errorf("no post in %+v", up)
}

func newID(up tg.UpdatesClass) int {
	if u, ok := up.(*tg.Updates); ok {
		for _, x := range u.Updates {
			switch v := x.(type) {
			case *tg.UpdateNewMessage:
				return v.Message.GetID()
			case *tg.UpdateNewChannelMessage:
				return v.Message.GetID()
			}
		}
	}
	return 0
}

// The app's private ids for getDifference, getMessages, joinChannel... and
// the old ids it still sends reach their handlers, or a clean refusal.
func TestAndroidPrivateMethodIDs(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	var ch *tg.Channel
	var post, hello int
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		up, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "hello", RandomID: 1})
		if err != nil {
			return err
		}
		hello = newID(up)
		if ch, post, err = newsChannel(ctx, c, "News"); err != nil {
			return err
		}
		_, err = c.ChannelsUpdateUsername(ctx, &tg.ChannelsUpdateUsernameRequest{Channel: inCh(ch), Username: "newsroom"})
		return err
	})

	s.android(t, alice, func(d droid) error {
		// updates.getDifference#25939651 flags:# pts:int pts_total_limit:flags.0?int date:int qts:int
		diff, err := d.obj(body(0x25939651, func(b *bin.Buffer) { b.PutInt(0); b.PutInt(1); b.PutInt(1); b.PutInt(0) }))
		if err != nil {
			return fmt.Errorf("getDifference: %w", err)
		}
		switch diff.(type) {
		case *tg.UpdatesDifference, *tg.UpdatesDifferenceSlice, *tg.UpdatesDifferenceEmpty:
		default:
			return fmt.Errorf("getDifference gave %T", diff)
		}
		// messages.getMessages#4222fa74 id:Vector<int>
		got, err := d.obj(body(0x4222fa74, func(b *bin.Buffer) { intVector(b, hello) }))
		if err != nil {
			return fmt.Errorf("getMessages: %w", err)
		}
		if texts := messageTexts(got); len(texts) != 1 || texts[0] != "hello" {
			return fmt.Errorf("getMessages %v", texts)
		}
		// channels.getMessages#93d7b347 channel:InputChannel id:Vector<int>
		got, err = d.obj(body(0x93d7b347, func(b *bin.Buffer) { putObj(b, inCh(ch)); intVector(b, post) }))
		if err != nil {
			return fmt.Errorf("channels.getMessages: %w", err)
		}
		if texts := messageTexts(got); len(texts) != 1 || texts[0] != "first post" {
			return fmt.Errorf("channels.getMessages %v", texts)
		}
		// langpack.getLanguages#800fd57d
		if _, err := d.raw(body(0x800fd57d, nil)); err != nil {
			return fmt.Errorf("langpack.getLanguages: %w", err)
		}
		// the app's first call on a connection: invokeAfterMsg(invokeWithLayer(initConnection(x)))
		ic := &tg.InitConnectionRequest{APIID: 4, DeviceModel: "Pixel 7", SystemVersion: "SDK 34", AppVersion: "12.10.12", SystemLangCode: "en",
			LangPack: "android", LangCode: "en", Query: rawObj(body(0x4222fa74, func(b *bin.Buffer) { intVector(b, hello) }))}
		var wrapped bin.Buffer
		wrapped.PutID(tg.InvokeAfterMsgRequestTypeID)
		wrapped.PutLong(0)
		var inner bin.Buffer
		putObj(&inner, ic)
		wrapped.Put(withLayer(231, inner.Buf))
		raw, err := d.send(wrapped.Buf)
		if err != nil {
			return fmt.Errorf("wrapped getMessages: %w", err)
		}
		if o, err := decode231(raw); err != nil || len(messageTexts(o)) != 1 {
			return fmt.Errorf("wrapped getMessages %T %v", o, err)
		}

		// channels.getFutureCreatorAfterLeave#a00918af: nobody takes over here
		who, err := d.obj(body(0xa00918af, func(b *bin.Buffer) { putObj(b, inCh(ch)) }))
		if err != nil {
			return fmt.Errorf("future creator: %w", err)
		}
		if _, ok := who.(*tg.UserEmpty); !ok {
			return fmt.Errorf("future creator %T", who)
		}
		// messages.addChatUser#f24753e3: there are no basic groups
		_, err = d.raw(body(0xf24753e3, func(b *bin.Buffer) { b.PutLong(5); putObj(b, &tg.InputUser{UserID: bob.id}); b.PutInt(0) }))
		if err := wantRPCErr(err, "CHAT_ID_INVALID"); err != nil {
			return fmt.Errorf("addChatUser: %w", err)
		}
		// messages.getStatsURL#812c2ae6 flags:# peer:InputPeer params:string
		_, err = d.raw(body(0x812c2ae6, func(b *bin.Buffer) { b.PutInt(0); putObj(b, &tg.InputPeerSelf{}); b.PutString("") }))
		if err := wantRPCErr(err, "METHOD_NOT_IMPLEMENTED"); err != nil {
			return fmt.Errorf("getStatsURL: %w", err)
		}
		// account.deleteAccount#418d4e0b reason:string: read into today's
		// request; without the password check over HTTPS first it refuses
		_, err = d.raw(body(0x418d4e0b, func(b *bin.Buffer) { b.PutString("Decline ToS update") }))
		if err := wantRPCErr(err, "PASSWORD_REQUIRED"); err != nil {
			return fmt.Errorf("deleteAccount: %w", err)
		}
		// account.uploadWallPaper#dd853661 file mime_type settings
		_, err = d.raw(body(0xdd853661, func(b *bin.Buffer) {
			putObj(b, &tg.InputFile{ID: 7, Parts: 1, Name: "w.jpg"})
			b.PutString("image/jpeg")
			putObj(b, &tg.WallPaperSettings{Blur: true})
		}))
		if err := wantRPCErr(err, "METHOD_NOT_IMPLEMENTED"); err != nil {
			return fmt.Errorf("uploadWallPaper: %w", err)
		}
		// messages.reportReaction#61422a48 peer id user_id: read into today's
		// request, whatever the reports handler then says
		_, err = d.raw(body(0x61422a48, func(b *bin.Buffer) { putObj(b, userPeer(bob.id)); b.PutInt(hello); putObj(b, &tg.InputUserSelf{}) }))
		if tgerr.Is(err, "INPUT_METHOD_INVALID") || tgerr.Is(err, "INPUT_REQUEST_INVALID") {
			return fmt.Errorf("reportReaction: %w", err)
		}
		// and an id nobody knows still is
		_, err = d.raw(body(0x0badf00d, func(b *bin.Buffer) { b.PutInt(1) }))
		return wantRPCErr(err, "INPUT_METHOD_INVALID")
	})

	s.android(t, bob, func(d droid) error {
		// channels.joinChannel#24b524c5 channel:InputChannel
		if _, err := d.obj(body(0x24b524c5, func(b *bin.Buffer) { putObj(b, inCh(ch)) })); err != nil {
			return fmt.Errorf("joinChannel: %w", err)
		}
		got, err := d.obj(body(0x93d7b347, func(b *bin.Buffer) { putObj(b, inCh(ch)); intVector(b, post) }))
		if err != nil || len(messageTexts(got)) != 1 {
			return fmt.Errorf("after join: %T %v", got, err)
		}
		return nil
	})
}

// The hook only sees ids a client's layer doesn't know, so what Web A and
// the desktop apps send is served as before.
func TestAndroidOverlayLeavesOtherClientsAlone(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	var ch *tg.Channel
	var post int
	runBoth(t, s, alice, func(ctx context.Context, tc *telegram.Client, c *tg.Client) error {
		up, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "hello", RandomID: 1})
		if err != nil {
			return err
		}
		hello := newID(up)
		if ch, post, err = newsChannel(ctx, c, "News"); err != nil {
			return err
		}
		if _, err := c.ChannelsUpdateUsername(ctx, &tg.ChannelsUpdateUsernameRequest{Channel: inCh(ch), Username: "newsroom"}); err != nil {
			return err
		}
		// Web A's calls, layer 232
		msgs, err := c.MessagesGetMessages(ctx, []tg.InputMessageClass{&tg.InputMessageID{ID: hello}})
		if err != nil || len(messageTexts(msgs.(bin.Object))) != 1 {
			return fmt.Errorf("getMessages %v", err)
		}
		cm, err := c.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{Channel: inCh(ch), ID: []tg.InputMessageClass{&tg.InputMessageID{ID: post}}})
		if err != nil || len(messageTexts(cm.(bin.Object))) != 1 {
			return fmt.Errorf("channels.getMessages %v", err)
		}
		st, err := c.UpdatesGetState(ctx)
		if err != nil {
			return err
		}
		if _, err := c.UpdatesGetDifference(ctx, &tg.UpdatesGetDifferenceRequest{Pts: st.Pts, Date: st.Date}); err != nil {
			return fmt.Errorf("getDifference %v", err)
		}
		if _, err := c.LangpackGetLanguages(ctx, "web"); err != nil {
			return fmt.Errorf("getLanguages %v", err)
		}
		// an unknown id at 232 is still INPUT_METHOD_INVALID
		var out rawReply
		err = tc.Invoke(ctx, rawObj(body(0x0badf00d, func(b *bin.Buffer) { b.PutInt(1) })), &out)
		if err := wantRPCErr(err, "INPUT_METHOD_INVALID"); err != nil {
			return err
		}
		// and Web A's own calls go on working after it
		_, err = c.UpdatesGetState(ctx)
		return err
	})
	// 0x24b524c5 is the plain joinChannel at layer 225: the ordinary path
	// takes it, not the overlay
	s.android(t, bob, func(d droid) error {
		raw, err := d.send(withLayer(225, body(0x24b524c5, func(b *bin.Buffer) { putObj(b, inCh(ch)) })))
		if err != nil {
			return fmt.Errorf("joinChannel at 225: %w", err)
		}
		if _, err := tlprofile.DecodeObject(tlprofile.Profile225, &bin.Buffer{Buf: raw}, tlprofile.Limits{}); err != nil {
			return fmt.Errorf("joinChannel at 225 answer: %w", err)
		}
		var row int
		if err := s.db.Pool.QueryRow(d.ctx, "SELECT count(*) FROM channel_members WHERE channel_id=$1 AND user_id=$2 AND status='member'", ch.ID, bob.id).Scan(&row); err != nil || row != 1 {
			return fmt.Errorf("bob not in the channel: %d %v", row, err)
		}
		return nil
	})
}

func TestAndroidEditDataLinksAndProfile(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	var ch *tg.Channel
	var post, text, file, fromBob int
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		up, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(alice.id), Message: "from bob", RandomID: 1})
		fromBob = newID(up)
		return err
	})
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		up, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "text", RandomID: 2})
		if err != nil {
			return err
		}
		text = newID(up)
		in, err := uploader.NewUploader(c).FromBytes(ctx, "x.txt", []byte("hello"))
		if err != nil {
			return err
		}
		up, err = c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: userPeer(bob.id), RandomID: 3, Message: "a file",
			Media: &tg.InputMediaUploadedDocument{File: in, MimeType: "text/plain"}})
		if err != nil {
			return err
		}
		file = newID(up)
		ch, post, err = newsChannel(ctx, c, "News")
		return err
	})
	// alice's id as bob's box numbers it
	var fromBobInAlice int
	s.db.Pool.QueryRow(context.Background(), "SELECT id FROM messages WHERE owner_id=$1 AND peer_id=$2 AND NOT out", alice.id, bob.id).Scan(&fromBobInAlice)
	_ = fromBob

	s.android(t, alice, func(d droid) error {
		editData := func(peer tg.InputPeerClass, id int) (bool, error) {
			o, err := d.call(&tg.MessagesGetMessageEditDataRequest{Peer: peer, ID: id})
			if err != nil {
				return false, err
			}
			return o.(*tg.MessagesMessageEditData).Caption, nil
		}
		if c, err := editData(userPeer(bob.id), text); err != nil || c {
			return fmt.Errorf("text message: %v %v", c, err)
		}
		if c, err := editData(userPeer(bob.id), file); err != nil || !c {
			return fmt.Errorf("file message: %v %v", c, err)
		}
		if _, err := editData(userPeer(bob.id), fromBobInAlice); wantRPCErr(err, "MESSAGE_AUTHOR_REQUIRED") != nil {
			return fmt.Errorf("bob's message: %v", err)
		}
		if _, err := editData(userPeer(bob.id), 99999); wantRPCErr(err, "MESSAGE_ID_INVALID") != nil {
			return fmt.Errorf("no message: %v", err)
		}
		if c, err := editData(peerCh(ch), post); err != nil || c {
			return fmt.Errorf("channel post: %v %v", c, err)
		}

		link := func(id int) (string, error) {
			o, err := d.call(&tg.ChannelsExportMessageLinkRequest{Channel: inCh(ch), ID: id})
			if err != nil {
				return "", err
			}
			return o.(*tg.ExportedMessageLink).Link, nil
		}
		want := "http://greengram.test/u/c/" + strconv.FormatInt(ch.ID, 10) + "/" + strconv.Itoa(post)
		if l, err := link(post); err != nil || l != want {
			return fmt.Errorf("private link %q %v, want %q", l, err, want)
		}
		if _, err := link(post + 100); wantRPCErr(err, "MESSAGE_ID_INVALID") != nil {
			return fmt.Errorf("link to nothing: %v", err)
		}
		if _, err := d.call(&tg.ChannelsUpdateUsernameRequest{Channel: inCh(ch), Username: "newsroom"}); err != nil {
			return err
		}
		if l, err := link(post); err != nil || l != "http://greengram.test/u/newsroom/"+strconv.Itoa(post) {
			return fmt.Errorf("public link %q %v", l, err)
		}

		// personal channel: the picker lists it, the profile shows it
		o, err := d.call(&tg.ChannelsGetAdminedPublicChannelsRequest{ForPersonal: true})
		if err != nil {
			return err
		}
		if chats := o.(*tg.MessagesChats).Chats; len(chats) != 1 || chats[0].GetID() != ch.ID {
			return fmt.Errorf("admined %+v", chats)
		}
		if _, err := d.call(&tg.AccountUpdatePersonalChannelRequest{Channel: inCh(ch)}); err != nil {
			return err
		}
		gr, err := d.call(&tg.ChannelsCreateChannelRequest{Megagroup: true, Title: "Chat"})
		if err != nil {
			return err
		}
		var group *tg.Channel
		for _, c := range gr.(*tg.Updates).Chats {
			group, _ = c.(*tg.Channel)
		}
		if _, err := d.call(&tg.AccountUpdatePersonalChannelRequest{Channel: inCh(group)}); wantRPCErr(err, "CHANNEL_INVALID") != nil {
			return fmt.Errorf("a group as personal channel: %v", err)
		}

		// top peers off and on
		if _, err := d.call(&tg.ContactsToggleTopPeersRequest{Enabled: false}); err != nil {
			return err
		}
		if tp, err := d.call(&tg.ContactsGetTopPeersRequest{Correspondents: true, Limit: 10}); err != nil {
			return err
		} else if _, ok := tp.(*tg.ContactsTopPeersDisabled); !ok {
			return fmt.Errorf("top peers while off: %T", tp)
		}
		if _, err := d.call(&tg.ContactsToggleTopPeersRequest{Enabled: true}); err != nil {
			return err
		}
		if tp, err := d.call(&tg.ContactsGetTopPeersRequest{Correspondents: true, Limit: 10}); err != nil {
			return err
		} else if _, ok := tp.(*tg.ContactsTopPeers); !ok {
			return fmt.Errorf("top peers while on: %T", tp)
		}

		if _, err := d.call(&tg.PaymentsClearSavedInfoRequest{Credentials: true, Info: true}); err != nil {
			return err
		}
		subs, err := d.call(&tg.PaymentsGetStarsSubscriptionsRequest{Peer: &tg.InputPeerSelf{}})
		if err != nil {
			return err
		}
		if st := subs.(*tg.PaymentsStarsStatus); !st.Flags.Has(1) || len(st.Subscriptions) != 0 || st.Flags.Has(2) {
			return fmt.Errorf("subscriptions %+v", st)
		}
		ua, err := d.call(&tg.MessagesRequestURLAuthRequest{URL: "https://example.com/login"})
		if err != nil {
			return err
		}
		if _, ok := ua.(*tg.URLAuthResultDefault); !ok {
			return fmt.Errorf("url auth %T", ua)
		}
		return nil
	})

	s.android(t, bob, func(d droid) error {
		full, err := d.call(&tg.UsersGetFullUserRequest{ID: &tg.InputUser{UserID: alice.id}})
		if err != nil {
			return err
		}
		uf := full.(*tg.UsersUserFull)
		if id, ok := uf.FullUser.GetPersonalChannelID(); !ok || id != ch.ID {
			return fmt.Errorf("personal channel %d %v", id, ok)
		}
		if m, _ := uf.FullUser.GetPersonalChannelMessage(); m != post || len(uf.Chats) != 1 {
			return fmt.Errorf("personal channel post %d, chats %d", m, len(uf.Chats))
		}
		// bob may not edit alice's post, and his link is the same public one
		if _, err := d.call(&tg.ChannelsJoinChannelRequest{Channel: inCh(ch)}); err != nil {
			return err
		}
		if _, err := d.call(&tg.MessagesGetMessageEditDataRequest{Peer: peerCh(ch), ID: post}); wantRPCErr(err, "MESSAGE_AUTHOR_REQUIRED") != nil {
			return fmt.Errorf("bob editing alice's post: %v", err)
		}
		// the add-contact bar
		up, err := d.call(&tg.ContactsAcceptContactRequest{ID: &tg.InputUser{UserID: alice.id}})
		if err != nil {
			return err
		}
		var bar bool
		for _, u := range up.(*tg.Updates).Updates {
			_, bar = u.(*tg.UpdatePeerSettings)
		}
		if !bar {
			return fmt.Errorf("accept contact %+v", up)
		}
		cs, err := d.call(&tg.ContactsGetContactsRequest{})
		if err != nil {
			return err
		}
		if list := cs.(*tg.ContactsContacts).Contacts; len(list) != 1 || list[0].UserID != alice.id {
			return fmt.Errorf("contacts %+v", list)
		}
		return nil
	})

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.AccountUpdatePersonalChannel(ctx, &tg.InputChannelEmpty{}); err != nil {
			return err
		}
		return nil
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		full, err := c.UsersGetFullUser(ctx, &tg.InputUser{UserID: alice.id})
		if err != nil {
			return err
		}
		if _, ok := full.FullUser.GetPersonalChannelID(); ok {
			return fmt.Errorf("personal channel still shown")
		}
		return nil
	})
}

func TestAndroidStickers(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cat, dog, blob := randomBytes(1200), randomBytes(1300), randomBytes(1400)
	a1, f1 := asset(cat, "image/webp", "webp", "😺", "")
	a2, f2 := asset(dog, "image/webp", "webp", "🐶", "")
	a3, f3 := asset(blob, "application/x-tgsticker", "tgs", "🙂", "5226734466315067999")
	pack := func(name, title, kind string, it map[string]any) map[string]any {
		return map[string]any{"name": name, "title": title, "kind": kind, "sourceName": title, "sourceHash": hex.EncodeToString(make([]byte, 32)),
			"official": false, "verification": "unverified", "sources": []string{}, "premiumOnly": false, "textColor": false, "stickers": []any{it}}
	}
	dir := bundle(t, []map[string]any{pack("cats", "Cats Pack", "sticker", a1), pack("dogs", "Dogs", "sticker", a2), pack("blobs", "Blob Emoji", "emoji", a3)},
		map[string][]byte{f1: cat, f2: dog, f3: blob})
	if _, err := s.media.ImportPacks(ctx, dir); err != nil {
		t.Fatal(err)
	}

	names := func(sets []tg.StickerSetCoveredClass) string {
		var out []string
		for _, v := range sets {
			c := v.(*tg.StickerSetMultiCovered)
			if len(c.Covers) == 0 {
				out = append(out, c.Set.ShortName+"(no cover)")
				continue
			}
			out = append(out, c.Set.ShortName)
		}
		return strings.Join(out, ",")
	}
	s.android(t, alice, func(d droid) error {
		f, err := d.call(&tg.MessagesGetFeaturedStickersRequest{})
		if err != nil {
			return err
		}
		fs := f.(*tg.MessagesFeaturedStickers)
		if n := names(fs.Sets); fs.Count != 2 || (n != "cats,dogs" && n != "dogs,cats") || fs.Hash == 0 {
			return fmt.Errorf("featured %q %d", n, fs.Count)
		}
		if again, err := d.call(&tg.MessagesGetFeaturedStickersRequest{Hash: fs.Hash}); err != nil {
			return err
		} else if _, ok := again.(*tg.MessagesFeaturedStickersNotModified); !ok {
			return fmt.Errorf("featured again %T", again)
		}

		found := func(req bin.Object) (string, error) {
			o, err := d.call(req)
			if err != nil {
				return "", err
			}
			return names(o.(*tg.MessagesFoundStickerSets).Sets), nil
		}
		if n, err := found(&tg.MessagesSearchStickerSetsRequest{Q: "CAT"}); err != nil || n != "cats" {
			return fmt.Errorf("search cat: %q %v", n, err)
		}
		if n, err := found(&tg.MessagesSearchStickerSetsRequest{Q: "zzz"}); err != nil || n != "" {
			return fmt.Errorf("search zzz: %q %v", n, err)
		}
		if n, err := found(&tg.MessagesSearchEmojiStickerSetsRequest{Q: "blob"}); err != nil || n != "blobs" {
			return fmt.Errorf("search emoji: %q %v", n, err)
		}

		stickers := func(req *tg.MessagesSearchStickersRequest) (int, error) {
			o, err := d.call(req)
			if err != nil {
				return 0, err
			}
			return len(o.(*tg.MessagesFoundStickers).Stickers), nil
		}
		if n, err := stickers(&tg.MessagesSearchStickersRequest{Emoticon: "😺️👍", LangCode: []string{"en"}, Limit: 100}); err != nil || n != 1 {
			return fmt.Errorf("by emoji: %d %v", n, err)
		}
		if n, err := stickers(&tg.MessagesSearchStickersRequest{Q: "dogs", LangCode: []string{}, Limit: 100}); err != nil || n != 1 {
			return fmt.Errorf("by title: %d %v", n, err)
		}
		if n, err := stickers(&tg.MessagesSearchStickersRequest{Emojis: true, Emoticon: "🙂", LangCode: []string{}, Limit: 100}); err != nil || n != 1 {
			return fmt.Errorf("emoji by emoji: %d %v", n, err)
		}
		if n, err := stickers(&tg.MessagesSearchStickersRequest{Q: "nothing", LangCode: []string{}, Limit: 100}); err != nil || n != 0 {
			return fmt.Errorf("nothing: %d %v", n, err)
		}

		order := func() ([]int64, error) {
			o, err := d.call(&tg.MessagesGetAllStickersRequest{})
			if err != nil {
				return nil, err
			}
			var ids []int64
			for _, v := range o.(*tg.MessagesAllStickers).Sets {
				ids = append(ids, v.ID)
			}
			return ids, nil
		}
		before, err := order()
		if err != nil || len(before) != 2 {
			return fmt.Errorf("installed %v %v", before, err)
		}
		if _, err := d.call(&tg.MessagesReorderStickerSetsRequest{Order: []int64{before[1], before[0]}}); err != nil {
			return err
		}
		if after, err := order(); err != nil || len(after) != 2 || after[0] != before[1] {
			return fmt.Errorf("reordered %v -> %v %v", before, after, err)
		}
		first := &tg.InputStickerSetID{ID: before[0]}
		var hash int64
		s.db.Pool.QueryRow(ctx, "SELECT access_hash FROM sticker_sets WHERE id=$1", before[0]).Scan(&hash)
		first.AccessHash = hash
		if _, err := d.call(&tg.MessagesToggleStickerSetsRequest{Uninstall: true, Stickersets: []tg.InputStickerSetClass{first}}); err != nil {
			return err
		}
		if now, err := order(); err != nil || len(now) != 1 {
			return fmt.Errorf("after removing %v %v", now, err)
		}
		if _, err := d.call(&tg.MessagesToggleStickerSetsRequest{Unarchive: true, Stickersets: []tg.InputStickerSetClass{first}}); err != nil {
			return err
		}
		if now, err := order(); err != nil || len(now) != 2 {
			return fmt.Errorf("after unarchiving %v %v", now, err)
		}

		// messages.getAttachedStickers answers Vector<StickerSetCovered>
		var att bin.Buffer
		putObj(&att, &tg.MessagesGetAttachedStickersRequest{Media: &tg.InputStickeredMediaPhoto{ID: &tg.InputPhotoEmpty{}}})
		raw, err := d.raw(att.Buf)
		if err != nil {
			return err
		}
		if n, err := (&bin.Buffer{Buf: raw}).VectorHeader(); err != nil || n != 0 {
			return fmt.Errorf("attached stickers %d %v", n, err)
		}
		if bg, err := d.call(&tg.AccountGetDefaultBackgroundEmojisRequest{}); err != nil {
			return err
		} else if _, ok := bg.(*tg.EmojiList); !ok {
			return fmt.Errorf("background emoji %T", bg)
		}

		// an id we don't have comes back as documentEmpty, not as nothing
		var ce bin.Buffer
		putObj(&ce, &tg.MessagesGetCustomEmojiDocumentsRequest{DocumentID: []int64{5226734466315067999, 42}})
		if raw, err = d.raw(ce.Buf); err != nil {
			return err
		}
		b := &bin.Buffer{Buf: raw}
		n, err := b.VectorHeader()
		if err != nil || n != 2 {
			return fmt.Errorf("custom emoji vector %d %v", n, err)
		}
		var docs []tg.DocumentClass
		for range n {
			doc, err := tg.DecodeDocument(b)
			if err != nil {
				return err
			}
			docs = append(docs, doc)
		}
		emoji, ok := docs[0].(*tg.Document)
		if empty, isEmpty := docs[1].(*tg.DocumentEmpty); !ok || !isEmpty || empty.ID != 42 {
			return fmt.Errorf("custom emoji %+v", docs)
		}
		// and its file in the app's 32 KB parts
		loc := &tg.InputDocumentFileLocation{ID: emoji.ID, AccessHash: emoji.AccessHash, FileReference: emoji.FileReference}
		var got []byte
		for off := int64(0); ; off += 32 << 10 {
			o, err := d.call(&tg.UploadGetFileRequest{CDNSupported: true, Location: loc, Offset: off, Limit: 32 << 10})
			if err != nil {
				return fmt.Errorf("getFile at %d: %w", off, err)
			}
			part := o.(*tg.UploadFile).Bytes
			got = append(got, part...)
			if len(part) < 32<<10 {
				break
			}
		}
		if !bytes.Equal(got, blob) {
			return fmt.Errorf("emoji file %d bytes, want %d", len(got), len(blob))
		}
		return nil
	})
}

func TestAndroidMiniAppWrites(t *testing.T) {
	s := start(t)
	ada := s.signUp(t, "ada")
	pushes := make(chan tg.UpdatesClass, 256)
	c := s.live(t, ada, pushes)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	bf := resolve(ctx, t, c, "botfather")
	say(ctx, t, c, bf, "/newbot")
	waitFrom(t, pushes, bf.ID, "Alright, a new bot.")
	say(ctx, t, c, bf, "Shop")
	waitFrom(t, pushes, bf.ID, "Good. Now let's choose a username")
	say(ctx, t, c, bf, "shop_test_bot")
	done := waitFrom(t, pushes, bf.ID, "Done! Congratulations")
	var token string
	for _, e := range done.Entities {
		if code, ok := e.(*tg.MessageEntityCode); ok {
			token = string([]rune(done.Message)[code.Offset : code.Offset+code.Length])
		}
	}
	shop := resolve(ctx, t, c, "shop_test_bot")
	bot := &tg.InputUser{UserID: shop.ID, AccessHash: shop.AccessHash}

	s.android(t, ada, func(d droid) error {
		can, err := d.call(&tg.BotsCanSendMessageRequest{Bot: bot})
		if err != nil {
			return err
		}
		if _, no := can.(*tg.BoolFalse); !no {
			return fmt.Errorf("a new bot may write: %T", can)
		}
		up, err := d.call(&tg.BotsAllowSendMessageRequest{Bot: bot})
		if err != nil {
			return err
		}
		var allowed bool
		for _, u := range up.(*tg.Updates).Updates {
			if n, ok := u.(*tg.UpdateNewMessage); ok {
				if m, ok := n.Message.(*tg.MessageService); ok {
					_, allowed = m.Action.(*tg.MessageActionBotAllowed)
				}
			}
		}
		if !allowed {
			return fmt.Errorf("allow %+v", up)
		}
		if can, err = d.call(&tg.BotsCanSendMessageRequest{Bot: bot}); err != nil {
			return err
		} else if _, yes := can.(*tg.BoolTrue); !yes {
			return fmt.Errorf("after allowing %T", can)
		}
		if again, err := d.call(&tg.BotsAllowSendMessageRequest{Bot: bot}); err != nil || len(again.(*tg.Updates).Updates) != 0 {
			return fmt.Errorf("allowing twice %v", err)
		}

		up, err = d.call(&tg.MessagesSendWebViewDataRequest{Bot: bot, RandomID: 77, ButtonText: "Order", Data: `{"item":3}`})
		if err != nil {
			return err
		}
		var sentNote bool
		for _, u := range up.(*tg.Updates).Updates {
			if n, ok := u.(*tg.UpdateNewMessage); ok {
				if m, ok := n.Message.(*tg.MessageService); ok {
					a, ok := m.Action.(*tg.MessageActionWebViewDataSent)
					sentNote = ok && a.Text == "Order"
				}
			}
		}
		if !sentNote {
			return fmt.Errorf("web view data %+v", up)
		}
		_, err = d.call(&tg.MessagesSendWebViewDataRequest{Bot: bot, RandomID: 78, ButtonText: "Order", Data: ""})
		return wantRPCErr(err, "DATA_INVALID")
	})

	// the bot may write now, and got the data; the allow note is no update
	s.bot(t, token, "sendMessage", map[string]any{"chat_id": ada.id, "text": "thanks"})
	ups := s.bot(t, token, "getUpdates", map[string]any{"timeout": 5}).([]any)
	if len(ups) != 1 {
		t.Fatalf("bot updates %v", ups)
	}
	msg := ups[0].(map[string]any)["message"].(map[string]any)
	wd, _ := msg["web_app_data"].(map[string]any)
	if wd["data"] != `{"item":3}` || wd["button_text"] != "Order" {
		t.Fatalf("web_app_data %v", msg)
	}
}

// What the co-owner's phone hit: a send must come back with the message in
// it, gifts must go through at 231, and Stars top-ups have no path.
func TestAndroidSendGiftAndTopUpAt231(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, _, err := s.stars.Adjust(ctx, alice.id, alice.id, 1000, "starter", "android-gift-1"); err != nil {
		t.Fatal(err)
	}
	gift, err := s.stars.AddGift(ctx, stars.Gift{Title: "Rose", Emoji: "🌹", Stars: 100, ConvertStars: 80})
	if err != nil {
		t.Fatal(err)
	}
	var ch *tg.Channel
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		ch, _, err = newsChannel(ctx, c, "News")
		return err
	})

	s.android(t, alice, func(d droid) error {
		// sendMessage as the app chains it on a fresh connection
		send := &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "hi from android", RandomID: 4242}
		var req bin.Buffer
		if err := tlprofile.EncodeObject(tlprofile.Profile231, send, &req); err != nil {
			return err
		}
		ic := &tg.InitConnectionRequest{APIID: 4, DeviceModel: "Pixel 7", SystemVersion: "SDK 34", AppVersion: "12.10.12", SystemLangCode: "en",
			LangPack: "android", LangCode: "en", Query: rawObj(req.Buf)}
		var inner, wrapped bin.Buffer
		putObj(&inner, ic)
		wrapped.PutID(tg.InvokeAfterMsgRequestTypeID)
		wrapped.PutLong(0)
		wrapped.Put(withLayer(231, inner.Buf))
		start := time.Now()
		raw, err := d.send(wrapped.Buf)
		if err != nil {
			return fmt.Errorf("send: %w", err)
		}
		if took := time.Since(start); took > 5*time.Second {
			return fmt.Errorf("send took %s", took)
		}
		o, err := decode231(raw)
		if err != nil {
			return fmt.Errorf("send answer: %w", err)
		}
		var gotID, gotNew bool
		for _, u := range o.(*tg.Updates).Updates {
			switch v := u.(type) {
			case *tg.UpdateMessageID:
				gotID = v.RandomID == 4242
			case *tg.UpdateNewMessage:
				m, ok := v.Message.(*tg.Message)
				gotNew = ok && m.Message == "hi from android" && v.Pts > 0 && v.PtsCount == 1
			}
		}
		if !gotID || !gotNew {
			return fmt.Errorf("send answer %+v", o)
		}
		// the pts it carries follows the state, so the app has no gap
		st, err := d.call(&tg.UpdatesGetStateRequest{})
		if err != nil {
			return err
		}
		for _, u := range o.(*tg.Updates).Updates {
			if v, ok := u.(*tg.UpdateNewMessage); ok && v.Pts != st.(*tg.UpdatesState).Pts {
				return fmt.Errorf("send pts %d, state %d", v.Pts, st.(*tg.UpdatesState).Pts)
			}
		}
		// and a channel post
		cp, err := d.call(&tg.MessagesSendMessageRequest{Peer: peerCh(ch), Message: "post from android", RandomID: 4343})
		if err != nil {
			return fmt.Errorf("channel send: %w", err)
		}
		var post bool
		for _, u := range cp.(*tg.Updates).Updates {
			_, ok := u.(*tg.UpdateNewChannelMessage)
			post = post || ok
		}
		if !post {
			return fmt.Errorf("channel send answer %+v", cp)
		}

		// the gift sheet shows the Stars catalog only on an explicit stargifts_blocked=false
		cfg, err := d.call(&tg.HelpGetAppConfigRequest{})
		if err != nil {
			return err
		}
		blocked := true
		for _, v := range cfg.(*tg.HelpAppConfig).Config.(*tg.JSONObject).Value {
			if b, ok := v.Value.(*tg.JSONBool); ok && v.Key == "stargifts_blocked" {
				blocked = b.Value
			}
		}
		if blocked {
			return fmt.Errorf("app config leaves stargifts_blocked to the app's default")
		}
		// the gift sheet: catalog, form, payment
		if _, err := d.call(&tg.UsersGetFullUserRequest{ID: &tg.InputUser{UserID: bob.id}}); err != nil {
			return fmt.Errorf("getFullUser: %w", err)
		}
		gs, err := d.call(&tg.PaymentsGetStarGiftsRequest{})
		if err != nil {
			return fmt.Errorf("getStarGifts: %w", err)
		}
		if _, ok := gs.(*tg.PaymentsStarGifts); !ok {
			return fmt.Errorf("getStarGifts %T", gs)
		}
		if _, err := d.call(&tg.PaymentsGetStarsStatusRequest{Peer: &tg.InputPeerSelf{}}); err != nil {
			return fmt.Errorf("getStarsStatus: %w", err)
		}
		inv := &tg.InputInvoiceStarGift{Peer: userPeer(bob.id), GiftID: gift.ID, Message: tg.TextWithEntities{Text: "for you", Entities: []tg.MessageEntityClass{}}}
		inv.SetFlags()
		form, err := d.call(&tg.PaymentsGetPaymentFormRequest{Invoice: inv, ThemeParams: tg.DataJSON{Data: `{"bg_color":"#ffffff"}`}})
		if err != nil {
			return fmt.Errorf("getPaymentForm: %w", err)
		}
		f, ok := form.(*tg.PaymentsPaymentFormStarGift)
		if !ok {
			return fmt.Errorf("gift form %T", form)
		}
		res, err := d.call(&tg.PaymentsSendStarsFormRequest{FormID: f.FormID, Invoice: inv})
		if err != nil {
			return fmt.Errorf("sendStarsForm: %w", err)
		}
		if _, ok := res.(*tg.PaymentsPaymentResult); !ok {
			return fmt.Errorf("gift payment %T", res)
		}

		// Stars top-up: no options, and the invoice the app sends is refused
		var top bin.Buffer
		putObj(&top, &tg.PaymentsGetStarsTopupOptionsRequest{})
		raw, err = d.raw(top.Buf)
		if err != nil {
			return err
		}
		if n, err := (&bin.Buffer{Buf: raw}).VectorHeader(); err != nil || n != 0 {
			return fmt.Errorf("top-up options %d %v", n, err)
		}
		_, err = d.call(&tg.PaymentsGetPaymentFormRequest{Invoice: &tg.InputInvoiceStars{Purpose: &tg.InputStorePaymentStarsTopup{Stars: 100, Currency: "USD", Amount: 199}}})
		return wantRPCErr(err, "INVOICE_INVALID")
	})
}
