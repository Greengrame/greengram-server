package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/telegram/uploader"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

// waitFor returns the first pushed update match accepts.
func waitFor(t *testing.T, got <-chan tg.UpdatesClass, what string, match func(tg.UpdateClass) bool) tg.UpdateClass {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case u := <-got:
			if ups, ok := u.(*tg.Updates); ok {
				for _, x := range ups.Updates {
					if match(x) {
						return x
					}
				}
			}
		case <-deadline:
			t.Fatalf("no push: %s", what)
			return nil
		}
	}
}

func pollIn(t *testing.T, m *tg.Message) *tg.MessageMediaPoll {
	t.Helper()
	p, ok := m.Media.(*tg.MessageMediaPoll)
	if !ok {
		t.Fatalf("media %T", m.Media)
	}
	return p
}

func asPoll(m *tg.Message) *tg.MessageMediaPoll {
	if m == nil {
		return nil
	}
	p, _ := m.Media.(*tg.MessageMediaPoll)
	return p
}

func pollUpdateIn(t *testing.T, u tg.UpdatesClass) *tg.UpdateMessagePoll {
	t.Helper()
	for _, x := range u.(*tg.Updates).Updates {
		if v, ok := x.(*tg.UpdateMessagePoll); ok {
			return v
		}
	}
	t.Fatalf("no updateMessagePoll in %+v", u)
	return nil
}

func answer(text, opt string) tg.PollAnswerClass {
	return &tg.PollAnswer{Text: tg.TextWithEntities{Text: text}, Option: []byte(opt)}
}

// voters is option -> (count, chosen, correct).
func voters(r tg.PollResults) map[string][3]int {
	out := map[string][3]int{}
	b := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	for _, v := range r.Results {
		out[string(v.Option)] = [3]int{v.Voters, b(v.Chosen), b(v.Correct)}
	}
	return out
}

func TestPolls(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	alicePushes := make(chan tg.UpdatesClass, 64)
	s.live(t, alice, alicePushes)
	bobPushes := make(chan tg.UpdatesClass, 64)
	bobLive := s.live(t, bob, bobPushes)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var pollMsg int
	var pollID int64
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: userPeer(bob.id), RandomID: 1, Media: &tg.InputMediaPoll{
			Poll: tg.Poll{ID: 777, PublicVoters: true, Question: tg.TextWithEntities{Text: "lunch?"}, Answers: []tg.PollAnswerClass{answer("pizza", "a"), answer("soup", "b")}}}})
		if err != nil {
			return err
		}
		m := sent(t, res)
		p := pollIn(t, m)
		if p.Poll.ID == 777 || !p.Poll.Creator || len(p.Poll.Answers) != 2 {
			return fmt.Errorf("sent poll %+v", p.Poll)
		}
		pollMsg, pollID = m.ID, p.Poll.ID

		_, err = c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: userPeer(bob.id), RandomID: 2, Media: &tg.InputMediaPoll{
			Poll: tg.Poll{Quiz: true, Question: tg.TextWithEntities{Text: "2+2?"}, Answers: []tg.PollAnswerClass{answer("3", "x"), answer("4", "y")}}}})
		if !tgerr.Is(err, "QUIZ_CORRECT_ANSWERS_EMPTY") {
			return fmt.Errorf("quiz without answer: %v", err)
		}
		in := &tg.InputMediaPoll{Poll: tg.Poll{Quiz: true, Question: tg.TextWithEntities{Text: "2+2?"}, Answers: []tg.PollAnswerClass{answer("3", "x"), answer("4", "y")}}}
		in.SetCorrectAnswers([]int{1})
		in.SetSolution("it's four")
		res, err = c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: userPeer(bob.id), RandomID: 3, Media: in})
		if err != nil {
			return err
		}
		if !pollIn(t, sent(t, res)).Poll.Quiz {
			return fmt.Errorf("not a quiz")
		}
		return nil
	})

	// bob's copy: his own message ids, nobody voted, not his poll
	got := waitFor(t, bobPushes, "poll", func(u tg.UpdateClass) bool {
		m := updateMessage(u)
		p := asPoll(m)
		return p != nil && p.Poll.Question.Text == "lunch?"
	})
	bobPoll := updateMessage(got)
	if p := pollIn(t, bobPoll); p.Poll.Creator || p.Poll.ID != pollID {
		t.Fatalf("bob's poll %+v", p.Poll)
	}
	quizGot := waitFor(t, bobPushes, "quiz", func(u tg.UpdateClass) bool {
		m := updateMessage(u)
		p := asPoll(m)
		return p != nil && p.Poll.Quiz
	})
	bobQuiz := updateMessage(quizGot)
	for _, v := range pollIn(t, bobQuiz).Results.Results {
		if v.Correct {
			t.Fatal("quiz answer shown before voting")
		}
	}

	res, err := bobLive.MessagesSendVote(ctx, &tg.MessagesSendVoteRequest{Peer: userPeer(alice.id), MsgID: bobPoll.ID, Options: [][]byte{[]byte("a")}})
	if err != nil {
		t.Fatal(err)
	}
	pu := pollUpdateIn(t, res)
	if v := voters(pu.Results); v["a"] != [3]int{1, 1, 0} || v["b"] != [3]int{0, 0, 0} || pu.Results.TotalVoters != 1 {
		t.Fatalf("bob's results %v", v)
	}
	if _, err := bobLive.MessagesSendVote(ctx, &tg.MessagesSendVoteRequest{Peer: userPeer(alice.id), MsgID: bobPoll.ID, Options: [][]byte{[]byte("a"), []byte("b")}}); !tgerr.Is(err, "OPTIONS_TOO_MUCH") {
		t.Fatalf("two answers to a single-choice poll: %v", err)
	}
	if _, err := bobLive.MessagesSendVote(ctx, &tg.MessagesSendVoteRequest{Peer: userPeer(alice.id), MsgID: bobPoll.ID, Options: [][]byte{[]byte("zz")}}); !tgerr.Is(err, "OPTION_INVALID") {
		t.Fatalf("unknown option: %v", err)
	}

	// alice hears about it, with her own view: one vote, not hers
	au := waitFor(t, alicePushes, "alice's poll update", func(u tg.UpdateClass) bool {
		v, ok := u.(*tg.UpdateMessagePoll)
		return ok && v.PollID == pollID && v.Results.TotalVoters == 1
	}).(*tg.UpdateMessagePoll)
	if v := voters(au.Results); v["a"] != [3]int{1, 0, 0} || au.MsgID != pollMsg || len(au.Results.RecentVoters) != 1 {
		t.Fatalf("alice's results %v msg %d recent %v", v, au.MsgID, au.Results.RecentVoters)
	}

	// quiz: wrong answer, then the right one shows with the explanation
	res, err = bobLive.MessagesSendVote(ctx, &tg.MessagesSendVoteRequest{Peer: userPeer(alice.id), MsgID: bobQuiz.ID, Options: [][]byte{[]byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	qu := pollUpdateIn(t, res)
	if v := voters(qu.Results); v["x"] != [3]int{1, 1, 0} || v["y"] != [3]int{0, 0, 1} || qu.Results.Solution != "it's four" {
		t.Fatalf("quiz results %v %q", v, qu.Results.Solution)
	}
	if _, err := bobLive.MessagesSendVote(ctx, &tg.MessagesSendVoteRequest{Peer: userPeer(alice.id), MsgID: bobQuiz.ID, Options: [][]byte{[]byte("y")}}); !tgerr.Is(err, "REVOTE_NOT_ALLOWED") {
		t.Fatalf("second quiz answer: %v", err)
	}

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		// history carries the live counts
		h, err := history(ctx, c, bob.id)
		if err != nil {
			return err
		}
		for _, m := range h {
			if m.ID == pollMsg {
				if v := voters(pollIn(t, m).Results); v["a"][0] != 1 {
					return fmt.Errorf("history poll %v", v)
				}
			}
		}
		votes, err := c.MessagesGetPollVotes(ctx, &tg.MessagesGetPollVotesRequest{Peer: userPeer(bob.id), ID: pollMsg, Limit: 10})
		if err != nil {
			return err
		}
		if votes.Count != 1 || len(votes.Votes) != 1 || len(votes.Users) != 1 {
			return fmt.Errorf("votes %+v", votes)
		}
		// only the sender stops it
		closed := &tg.InputMediaPoll{Poll: tg.Poll{ID: pollID, Closed: true, Question: tg.TextWithEntities{Text: "lunch?"}, Answers: []tg.PollAnswerClass{answer("pizza", "a"), answer("soup", "b")}}}
		up, err := c.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{Peer: userPeer(bob.id), ID: pollMsg, Media: closed})
		if err != nil {
			return err
		}
		if !pollUpdateIn(t, up).Poll.Closed {
			return fmt.Errorf("not closed")
		}
		return nil
	})
	if _, err := bobLive.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{Peer: userPeer(alice.id), ID: bobQuiz.ID,
		Media: &tg.InputMediaPoll{Poll: tg.Poll{Closed: true}}}); !tgerr.Is(err, "MESSAGE_AUTHOR_REQUIRED") {
		t.Fatalf("bob closing alice's quiz: %v", err)
	}
	waitFor(t, bobPushes, "closed poll", func(u tg.UpdateClass) bool {
		v, ok := u.(*tg.UpdateMessagePoll)
		return ok && v.PollID == pollID && v.Poll.Closed
	})
	if _, err := bobLive.MessagesSendVote(ctx, &tg.MessagesSendVoteRequest{Peer: userPeer(alice.id), MsgID: bobPoll.ID, Options: [][]byte{[]byte("b")}}); !tgerr.Is(err, "MESSAGE_POLL_CLOSED") {
		t.Fatalf("vote on a closed poll: %v", err)
	}

	// a group poll that closes by itself
	var group *tg.Channel
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		var err error
		if group, err = newGroup(ctx, c, t, "Polls", bob.id); err != nil {
			return err
		}
		_, err = c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: peerCh(group), RandomID: 4, Media: &tg.InputMediaPoll{
			Poll: tg.Poll{ClosePeriod: 5, MultipleChoice: true, Question: tg.TextWithEntities{Text: "colours"}, Answers: []tg.PollAnswerClass{answer("red", "r"), answer("blue", "b"), answer("green", "g")}}}})
		return err
	})
	gm := updateMessage(waitFor(t, bobPushes, "group poll", func(u tg.UpdateClass) bool {
		m := updateMessage(u)
		p := asPoll(m)
		return p != nil && p.Poll.Question.Text == "colours"
	}))
	res, err = bobLive.MessagesSendVote(ctx, &tg.MessagesSendVoteRequest{Peer: peerCh(group), MsgID: gm.ID, Options: [][]byte{[]byte("r"), []byte("g")}})
	if err != nil {
		t.Fatal(err)
	}
	if v := voters(pollUpdateIn(t, res).Results); v["r"] != [3]int{1, 1, 0} || v["g"] != [3]int{1, 1, 0} {
		t.Fatalf("group results %v", v)
	}
	waitFor(t, alicePushes, "group vote", func(u tg.UpdateClass) bool {
		v, ok := u.(*tg.UpdateMessagePoll)
		return ok && v.Results.TotalVoters == 1 && voters(v.Results)["g"][0] == 1
	})
	waitFor(t, bobPushes, "group poll closing on time", func(u tg.UpdateClass) bool {
		v, ok := u.(*tg.UpdateMessagePoll)
		return ok && v.Poll.Closed && v.Poll.Question.Text == "colours"
	})
}

func TestScheduledMessages(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	alicePushes := make(chan tg.UpdatesClass, 64)
	s.live(t, alice, alicePushes)
	bobPushes := make(chan tg.UpdatesClass, 64)
	bobLive := s.live(t, bob, bobPushes)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var later, gone int
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		at := int(time.Now().Unix()) + 3
		res, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: "soon", RandomID: 1, ScheduleDate: at})
		if err != nil {
			return err
		}
		var nm *tg.UpdateNewScheduledMessage
		for _, u := range res.(*tg.Updates).Updates {
			switch v := u.(type) {
			case *tg.UpdateNewScheduledMessage:
				nm = v
			case *tg.UpdateNewMessage:
				return fmt.Errorf("scheduled message went out at once")
			}
		}
		if nm == nil || nm.Message.(*tg.Message).Date != at {
			return fmt.Errorf("schedule answer %+v", res)
		}
		for i, text := range []string{"later", "never"} {
			res, err := c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), Message: text, RandomID: int64(10 + i), ScheduleDate: at + 3600})
			if err != nil {
				return err
			}
			id := res.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID
			if i == 0 {
				later = id
			} else {
				gone = id
			}
		}
		h, err := c.MessagesGetScheduledHistory(ctx, &tg.MessagesGetScheduledHistoryRequest{Peer: userPeer(bob.id)})
		if err != nil {
			return err
		}
		if list, _ := msgList(t, h); len(list) != 3 {
			return fmt.Errorf("scheduled history %d", len(list))
		}
		// moving and rewording one that waits
		up, err := c.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{Peer: userPeer(bob.id), ID: later, Message: "later, edited", ScheduleDate: at + 7200})
		if err != nil {
			return err
		}
		if m := up.(*tg.Updates).Updates[0].(*tg.UpdateNewScheduledMessage).Message.(*tg.Message); m.Message != "later, edited" || m.Date != at+7200 {
			return fmt.Errorf("edited scheduled %+v", m)
		}
		if _, err := c.MessagesDeleteScheduledMessages(ctx, &tg.MessagesDeleteScheduledMessagesRequest{Peer: userPeer(bob.id), ID: []int{gone}}); err != nil {
			return err
		}
		h, err = c.MessagesGetScheduledHistory(ctx, &tg.MessagesGetScheduledHistoryRequest{Peer: userPeer(bob.id)})
		if err != nil {
			return err
		}
		if list, _ := msgList(t, h); len(list) != 2 {
			return fmt.Errorf("after delete %d", len(list))
		}
		return nil
	})
	// bob doesn't see it until it goes
	h, err := history(ctx, bobLive, alice.id)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 0 {
		t.Fatalf("bob sees %d messages early", len(h))
	}
	waitNew(t, bobPushes, "soon")
	del := waitFor(t, alicePushes, "scheduled message leaving the queue", func(u tg.UpdateClass) bool {
		v, ok := u.(*tg.UpdateDeleteScheduledMessages)
		return ok && len(v.SentMessages) == 1
	}).(*tg.UpdateDeleteScheduledMessages)
	if len(del.Messages) != 1 {
		t.Fatalf("delete %+v", del)
	}

	// send now
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		_, err := c.MessagesSendScheduledMessages(ctx, &tg.MessagesSendScheduledMessagesRequest{Peer: userPeer(bob.id), ID: []int{later}})
		if err != nil {
			return err
		}
		h, err := c.MessagesGetScheduledHistory(ctx, &tg.MessagesGetScheduledHistoryRequest{Peer: userPeer(bob.id)})
		if err != nil {
			return err
		}
		if list, _ := msgList(t, h); len(list) != 0 {
			return fmt.Errorf("left in queue %d", len(list))
		}
		return nil
	})
	waitNew(t, bobPushes, "later, edited")

	// a channel post on a timer
	var ch *tg.Channel
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Broadcast: true, Title: "News"})
		if err != nil {
			return err
		}
		ch = channelOf(t, res)
		if _, err := c.ChannelsInviteToChannel(ctx, &tg.ChannelsInviteToChannelRequest{Channel: inCh(ch), Users: []tg.InputUserClass{&tg.InputUser{UserID: bob.id}}}); err != nil {
			return err
		}
		res, err = c.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{Peer: peerCh(ch), Message: "post at noon", RandomID: 5, ScheduleDate: int(time.Now().Unix()) + 2})
		if err != nil {
			return err
		}
		for _, u := range res.(*tg.Updates).Updates {
			if v, ok := u.(*tg.UpdateNewScheduledMessage); ok && !v.Message.(*tg.Message).Post {
				return fmt.Errorf("scheduled post not a post")
			}
		}
		return nil
	})
	waitChannelText(t, bobPushes, "post at noon")
}

func TestSavedGifsAndStickers(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	alicePushes := make(chan tg.UpdatesClass, 64)
	aliceLive := s.live(t, alice, alicePushes)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	upload := func(name, mime string, attrs ...tg.DocumentAttributeClass) *tg.Document {
		in, err := uploader.NewUploader(aliceLive).FromBytes(ctx, name, randomBytes(2048))
		if err != nil {
			t.Fatal(err)
		}
		res, err := aliceLive.MessagesUploadMedia(ctx, &tg.MessagesUploadMediaRequest{Peer: userPeer(bob.id),
			Media: &tg.InputMediaUploadedDocument{File: in, MimeType: mime, Attributes: attrs}})
		if err != nil {
			t.Fatal(err)
		}
		return res.(*tg.MessageMediaDocument).Document.(*tg.Document)
	}
	input := func(d *tg.Document) *tg.InputDocument {
		return &tg.InputDocument{ID: d.ID, AccessHash: d.AccessHash, FileReference: d.FileReference}
	}
	gif := upload("cat.mp4", "video/mp4", &tg.DocumentAttributeVideo{W: 100, H: 100}, &tg.DocumentAttributeAnimated{})
	sticker := upload("s.webp", "image/webp", &tg.DocumentAttributeSticker{Alt: "😀", Stickerset: &tg.InputStickerSetEmpty{}})

	// sending a sticker puts it in the recent ones, and every session hears
	if _, err := aliceLive.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: userPeer(bob.id), RandomID: 1, Media: &tg.InputMediaDocument{ID: input(sticker)}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, alicePushes, "recent stickers", func(u tg.UpdateClass) bool { _, ok := u.(*tg.UpdateRecentStickers); return ok })
	rec, err := aliceLive.MessagesGetRecentStickers(ctx, &tg.MessagesGetRecentStickersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	r := rec.(*tg.MessagesRecentStickers)
	if len(r.Stickers) != 1 || len(r.Packs) != 1 || r.Packs[0].Emoticon != "😀" || len(r.Dates) != 1 {
		t.Fatalf("recent %+v", r)
	}
	if again, err := aliceLive.MessagesGetRecentStickers(ctx, &tg.MessagesGetRecentStickersRequest{Hash: r.Hash}); err != nil {
		t.Fatal(err)
	} else if _, ok := again.(*tg.MessagesRecentStickersNotModified); !ok {
		t.Fatalf("same hash: %T", again)
	}

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if _, err := c.MessagesSaveGif(ctx, &tg.MessagesSaveGifRequest{ID: input(sticker)}); !tgerr.Is(err, "GIF_ID_INVALID") {
			return fmt.Errorf("sticker as gif: %v", err)
		}
		if _, err := c.MessagesSaveGif(ctx, &tg.MessagesSaveGifRequest{ID: input(gif)}); err != nil {
			return err
		}
		if _, err := c.MessagesFaveSticker(ctx, &tg.MessagesFaveStickerRequest{ID: input(sticker)}); err != nil {
			return err
		}
		if _, err := c.MessagesClearRecentStickers(ctx, &tg.MessagesClearRecentStickersRequest{}); err != nil {
			return err
		}
		return nil
	})
	waitFor(t, alicePushes, "saved gifs", func(u tg.UpdateClass) bool { _, ok := u.(*tg.UpdateSavedGifs); return ok })
	waitFor(t, alicePushes, "faved stickers", func(u tg.UpdateClass) bool { _, ok := u.(*tg.UpdateFavedStickers); return ok })
	gifs, err := aliceLive.MessagesGetSavedGifs(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if g := gifs.(*tg.MessagesSavedGifs); len(g.Gifs) != 1 || g.Gifs[0].(*tg.Document).ID != gif.ID {
		t.Fatalf("gifs %+v", g)
	}
	faved, err := aliceLive.MessagesGetFavedStickers(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if f := faved.(*tg.MessagesFavedStickers); len(f.Stickers) != 1 || len(f.Packs) != 1 {
		t.Fatalf("faved %+v", f)
	}
	rec, err = aliceLive.MessagesGetRecentStickers(ctx, &tg.MessagesGetRecentStickersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.(*tg.MessagesRecentStickers).Stickers) != 0 {
		t.Fatal("recent not cleared")
	}
	if _, err := aliceLive.MessagesSaveGif(ctx, &tg.MessagesSaveGifRequest{ID: input(gif), Unsave: true}); err != nil {
		t.Fatal(err)
	}
	gifs, _ = aliceLive.MessagesGetSavedGifs(ctx, 0)
	if len(gifs.(*tg.MessagesSavedGifs).Gifs) != 0 {
		t.Fatal("gif not unsaved")
	}
}

func TestDiceGeoContacts(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	bobPushes := make(chan tg.UpdatesClass, 64)
	bobLive := s.live(t, bob, bobPushes)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var liveID int
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: userPeer(bob.id), RandomID: 1, Media: &tg.InputMediaDice{Emoticon: "🎲"}})
		if err != nil {
			return err
		}
		if d, ok := sent(t, res).Media.(*tg.MessageMediaDice); !ok || d.Value < 1 || d.Value > 6 || d.Emoticon != "🎲" {
			return fmt.Errorf("dice %+v", sent(t, res).Media)
		}
		if _, err := c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: userPeer(bob.id), RandomID: 2, Media: &tg.InputMediaDice{Emoticon: "🍕"}}); !tgerr.Is(err, "EMOTICON_INVALID") {
			return fmt.Errorf("pizza dice: %v", err)
		}
		pt := &tg.InputGeoPoint{Lat: 50.45, Long: 30.52}
		res, err = c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: userPeer(bob.id), RandomID: 3, Media: &tg.InputMediaGeoPoint{GeoPoint: pt}})
		if err != nil {
			return err
		}
		if g, ok := sent(t, res).Media.(*tg.MessageMediaGeo); !ok || g.Geo.(*tg.GeoPoint).Lat != 50.45 {
			return fmt.Errorf("geo %+v", sent(t, res).Media)
		}
		if _, err := c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: userPeer(bob.id), RandomID: 4, Media: &tg.InputMediaVenue{GeoPoint: pt,
			Title: "Maidan", Address: "Kyiv"}}); err != nil {
			return err
		}
		live := &tg.InputMediaGeoLive{GeoPoint: pt}
		live.SetPeriod(900)
		res, err = c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: userPeer(bob.id), RandomID: 5, Media: live})
		if err != nil {
			return err
		}
		liveID = sent(t, res).ID
		moved := &tg.InputMediaGeoLive{GeoPoint: &tg.InputGeoPoint{Lat: 50.46, Long: 30.53}}
		moved.SetHeading(90)
		up, err := c.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{Peer: userPeer(bob.id), ID: liveID, Media: moved})
		if err != nil {
			return err
		}
		var lm *tg.MessageMediaGeoLive
		for _, u := range up.(*tg.Updates).Updates {
			if e, ok := u.(*tg.UpdateEditMessage); ok {
				lm, _ = e.Message.(*tg.Message).Media.(*tg.MessageMediaGeoLive)
			}
		}
		if lm == nil || lm.Geo.(*tg.GeoPoint).Lat != 50.46 || lm.Heading != 90 || lm.Period != 900 {
			return fmt.Errorf("moved live %+v", lm)
		}
		stop := &tg.InputMediaGeoLive{Stopped: true, GeoPoint: &tg.InputGeoPointEmpty{}}
		if _, err := c.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{Peer: userPeer(bob.id), ID: liveID, Media: stop}); err != nil {
			return err
		}
		if _, err := c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: userPeer(bob.id), RandomID: 6, Media: &tg.InputMediaContact{
			PhoneNumber: "+15550100", FirstName: "Nobody"}}); err != nil {
			return err
		}
		return nil
	})
	waitFor(t, bobPushes, "venue", func(u tg.UpdateClass) bool {
		m := updateMessage(u)
		if m == nil {
			return false
		}
		ven, ok := m.Media.(*tg.MessageMediaVenue)
		return ok && ven.Title == "Maidan"
	})
	waitFor(t, bobPushes, "stopped live location", func(u tg.UpdateClass) bool {
		e, ok := u.(*tg.UpdateEditMessage)
		if !ok {
			return false
		}
		lm, ok := e.Message.(*tg.Message).Media.(*tg.MessageMediaGeoLive)
		return ok && lm.Period < 900
	})
	waitFor(t, bobPushes, "contact", func(u tg.UpdateClass) bool {
		m := updateMessage(u)
		if m == nil {
			return false
		}
		c, ok := m.Media.(*tg.MessageMediaContact)
		return ok && c.FirstName == "Nobody" && c.UserID == 0
	})
	// shared media tabs count them
	counters, err := bobLive.MessagesGetSearchCounters(ctx, &tg.MessagesGetSearchCountersRequest{Peer: userPeer(alice.id),
		Filters: []tg.MessagesFilterClass{&tg.InputMessagesFilterGeo{}, &tg.InputMessagesFilterContacts{}}})
	if err != nil {
		t.Fatal(err)
	}
	if counters[0].Count != 3 || counters[1].Count != 1 {
		t.Fatalf("counters %+v", counters)
	}
}
