package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamxvbaba/td/telegram/downloader"
	"github.com/iamxvbaba/td/telegram/uploader"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

func testPNG(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.NRGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// sent is the message a send call made, from its own answer.
func sent(t *testing.T, u tg.UpdatesClass) *tg.Message {
	t.Helper()
	ups, ok := u.(*tg.Updates)
	if !ok {
		t.Fatalf("updates %T", u)
	}
	for _, x := range ups.Updates {
		if m := updateMessage(x); m != nil {
			return m
		}
	}
	t.Fatalf("no message in %+v", ups)
	return nil
}

func updateMessage(u tg.UpdateClass) *tg.Message {
	var msg tg.MessageClass
	switch v := u.(type) {
	case *tg.UpdateNewMessage:
		msg = v.Message
	case *tg.UpdateNewChannelMessage:
		msg = v.Message
	}
	m, _ := msg.(*tg.Message)
	return m
}

func photoOf(t *testing.T, m *tg.Message) *tg.Photo {
	t.Helper()
	mm, ok := m.Media.(*tg.MessageMediaPhoto)
	if !ok {
		t.Fatalf("message %d media %T", m.ID, m.Media)
	}
	p, _ := mm.GetPhoto()
	return p.(*tg.Photo)
}

func documentOf(t *testing.T, m *tg.Message) *tg.Document {
	t.Helper()
	mm, ok := m.Media.(*tg.MessageMediaDocument)
	if !ok {
		t.Fatalf("message %d media %T", m.ID, m.Media)
	}
	d, _ := mm.GetDocument()
	return d.(*tg.Document)
}

func download(ctx context.Context, c *tg.Client, loc tg.InputFileLocationClass) ([]byte, error) {
	var buf bytes.Buffer
	_, err := downloader.NewDownloader().Download(c, loc).Stream(ctx, &buf)
	return buf.Bytes(), err
}

func docLocation(d *tg.Document) *tg.InputDocumentFileLocation {
	return &tg.InputDocumentFileLocation{ID: d.ID, AccessHash: d.AccessHash, FileReference: d.FileReference}
}

func TestMediaMessages(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	bobPushes := make(chan tg.UpdatesClass, 64)
	bobLive := s.live(t, bob, bobPushes)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pic := testPNG(1200, 800)
	file := randomBytes(700 << 10)
	big := randomBytes(4096*2 + 100)
	var photo *tg.Photo
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		up := uploader.NewUploader(c)
		in, err := up.FromBytes(ctx, "pic.png", pic)
		if err != nil {
			return err
		}
		res, err := c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: userPeer(bob.id), Message: "look", RandomID: 10,
			Media: &tg.InputMediaUploadedPhoto{File: in}})
		if err != nil {
			return err
		}
		photo = photoOf(t, sent(t, res))

		in, err = up.FromBytes(ctx, "blob.bin", file)
		if err != nil {
			return err
		}
		if _, err := c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: userPeer(bob.id), RandomID: 11,
			Media: &tg.InputMediaUploadedDocument{File: in, MimeType: "application/octet-stream"}}); err != nil {
			return err
		}

		// saveBigFilePart by hand: two full parts and a short last one
		for i, part := range [][]byte{big[:4096], big[4096:8192], big[8192:]} {
			if _, err := c.UploadSaveBigFilePart(ctx, &tg.UploadSaveBigFilePartRequest{FileID: 99, FilePart: i, FileTotalParts: 3, Bytes: part}); err != nil {
				return err
			}
		}
		if _, err := c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: userPeer(bob.id), Message: "big one", RandomID: 12,
			Media: &tg.InputMediaUploadedDocument{File: &tg.InputFileBig{ID: 99, Parts: 3, Name: "big.dat"}, MimeType: "application/x-test",
				Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "big.dat"}}}}); err != nil {
			return err
		}
		_, err = c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: userPeer(bob.id), RandomID: 13,
			Media: &tg.InputMediaUploadedDocument{File: &tg.InputFile{ID: 12345, Parts: 2, Name: "x"}}})
		if !tgerr.Is(err, "FILE_PART_MISSING") {
			return fmt.Errorf("never uploaded file: %v", err)
		}

		// an album of the same photo twice, as a re-send
		ip := &tg.InputMediaPhoto{ID: &tg.InputPhoto{ID: photo.ID, AccessHash: photo.AccessHash, FileReference: photo.FileReference}}
		_, err = c.MessagesSendMultiMedia(ctx, &tg.MessagesSendMultiMediaRequest{Peer: userPeer(bob.id), MultiMedia: []tg.InputSingleMedia{
			{Media: ip, RandomID: 20, Message: "one"}, {Media: ip, RandomID: 21, Message: "two"}}})
		return err
	})

	got := waitNew(t, bobPushes, "look")
	p := photoOf(t, got)
	if p.ID != photo.ID || len(p.Sizes) != 4 {
		t.Fatalf("bob's photo %+v", p)
	}
	largest := p.Sizes[len(p.Sizes)-1].(*tg.PhotoSize)
	if largest.Type != "y" || largest.W != 1200 || largest.H != 800 {
		t.Fatalf("largest size %+v", largest)
	}
	raw, err := download(ctx, bobLive, &tg.InputPhotoFileLocation{ID: p.ID, AccessHash: p.AccessHash, FileReference: p.FileReference, ThumbSize: "y"})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != largest.Size {
		t.Fatalf("downloaded %d bytes, size says %d", len(raw), largest.Size)
	}
	if cfg, err := jpeg.DecodeConfig(bytes.NewReader(raw)); err != nil || cfg.Width != 1200 || cfg.Height != 800 {
		t.Fatalf("photo jpeg %v %+v", err, cfg)
	}
	if _, err := download(ctx, bobLive, &tg.InputPhotoFileLocation{ID: p.ID, AccessHash: p.AccessHash + 1, ThumbSize: "y"}); !tgerr.Is(err, "LOCATION_INVALID") {
		t.Fatalf("wrong access hash: %v", err)
	}

	msgs, err := history(ctx, bobLive, alice.id)
	if err != nil {
		t.Fatal(err)
	}
	// newest first: two, one, big one, blob, look
	if len(msgs) != 5 {
		t.Fatalf("bob has %d messages", len(msgs))
	}
	if msgs[0].GroupedID == 0 || msgs[0].GroupedID != msgs[1].GroupedID || photoOf(t, msgs[0]).ID != photo.ID {
		t.Fatalf("album %+v %+v", msgs[0], msgs[1])
	}
	bigDoc, fileDoc := documentOf(t, msgs[2]), documentOf(t, msgs[3])
	if fileDoc.Size != int64(len(file)) || fileDoc.MimeType != "application/octet-stream" {
		t.Fatalf("document %+v", fileDoc)
	}
	var name string
	for _, a := range fileDoc.Attributes {
		if f, ok := a.(*tg.DocumentAttributeFilename); ok {
			name = f.FileName
		}
	}
	if name != "blob.bin" {
		t.Fatalf("file name %q from the upload, attributes %+v", name, fileDoc.Attributes)
	}
	for _, c := range []struct {
		doc  *tg.Document
		want []byte
	}{{fileDoc, file}, {bigDoc, big}} {
		raw, err := download(ctx, bobLive, docLocation(c.doc))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw, c.want) {
			t.Fatalf("document %d: %d bytes back, not the %d sent", c.doc.ID, len(raw), len(c.want))
		}
	}

	// ranged reads
	loc := docLocation(fileDoc)
	part, err := bobLive.UploadGetFile(ctx, &tg.UploadGetFileRequest{Location: loc, Offset: 8192, Limit: 4096})
	if err != nil || !bytes.Equal(part.(*tg.UploadFile).Bytes, file[8192:12288]) {
		t.Fatalf("range 8192+4096: %v", err)
	}
	part, err = bobLive.UploadGetFile(ctx, &tg.UploadGetFileRequest{Location: loc, Precise: true, Offset: 1024, Limit: 3072})
	if err != nil || !bytes.Equal(part.(*tg.UploadFile).Bytes, file[1024:4096]) {
		t.Fatalf("precise range: %v", err)
	}
	part, err = bobLive.UploadGetFile(ctx, &tg.UploadGetFileRequest{Location: loc, Offset: 1 << 20, Limit: 4096})
	if err != nil || len(part.(*tg.UploadFile).Bytes) != 0 {
		t.Fatalf("past the end: %v", err)
	}
	if _, err := bobLive.UploadGetFile(ctx, &tg.UploadGetFileRequest{Location: loc, Offset: 0, Limit: 1000}); !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("limit 1000: %v", err)
	}
	if _, err := bobLive.UploadGetFile(ctx, &tg.UploadGetFileRequest{Location: loc, Offset: 1024, Limit: 4096}); !tgerr.Is(err, "OFFSET_INVALID") {
		t.Fatalf("offset 1024: %v", err)
	}
	hashes, err := bobLive.UploadGetFileHashes(ctx, &tg.UploadGetFileHashesRequest{Location: loc, Offset: 0})
	if err != nil || len(hashes) != 6 {
		t.Fatalf("hashes %v %d", err, len(hashes))
	}
	if sum := sha256.Sum256(file[:128<<10]); !bytes.Equal(hashes[0].Hash, sum[:]) || hashes[5].Limit != len(file)-5*(128<<10) {
		t.Fatalf("hash pieces %+v", hashes[5])
	}

	// shared media
	for _, c := range []struct {
		filter tg.MessagesFilterClass
		want   int
	}{{&tg.InputMessagesFilterPhotos{}, 3}, {&tg.InputMessagesFilterDocument{}, 2}, {&tg.InputMessagesFilterVoice{}, 0}} {
		found, err := bobLive.MessagesSearch(ctx, &tg.MessagesSearchRequest{Peer: userPeer(alice.id), Filter: c.filter, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if n := len(found.(*tg.MessagesMessagesSlice).Messages); n != c.want {
			t.Fatalf("%T: %d messages, want %d", c.filter, n, c.want)
		}
	}
	counters, err := bobLive.MessagesGetSearchCounters(ctx, &tg.MessagesGetSearchCountersRequest{Peer: userPeer(alice.id),
		Filters: []tg.MessagesFilterClass{&tg.InputMessagesFilterPhotoVideo{}, &tg.InputMessagesFilterDocument{}}})
	if err != nil || counters[0].Count != 3 || counters[1].Count != 2 {
		t.Fatalf("counters %v %+v", err, counters)
	}

	// a forward carries the media; the album stays one
	if _, err := bobLive.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{FromPeer: userPeer(alice.id), ToPeer: &tg.InputPeerSelf{},
		ID: []int{msgs[1].ID, msgs[0].ID, msgs[4].ID}, RandomID: []int64{30, 31, 32}}); err != nil {
		t.Fatal(err)
	}
	saved, err := history(ctx, bobLive, bob.id)
	if err != nil || len(saved) != 3 {
		t.Fatalf("saved %v %d", err, len(saved))
	}
	if photoOf(t, saved[0]).ID != photo.ID || saved[0].FwdFrom.FromID == nil || saved[1].GroupedID == 0 || saved[1].GroupedID != saved[2].GroupedID ||
		saved[1].GroupedID == msgs[0].GroupedID {
		t.Fatalf("forwarded %+v", saved)
	}

	// a caption can go, the media stays
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		mine, err := history(ctx, c, bob.id)
		if err != nil {
			return err
		}
		// an empty message only goes out with its flag set by hand, like the apps do
		req := &tg.MessagesEditMessageRequest{Peer: userPeer(bob.id), ID: mine[4].ID}
		req.SetMessage("")
		_, err = c.MessagesEditMessage(ctx, req)
		return err
	})
	msgs, _ = history(ctx, bobLive, alice.id)
	if msgs[4].Message != "" || photoOf(t, msgs[4]).ID != photo.ID {
		t.Fatalf("after caption edit %+v", msgs[4])
	}
}

func TestProfileAndChatPhotos(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")

	var photoID int64
	var ch *tg.Channel
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		in, err := uploader.NewUploader(c).FromBytes(ctx, "me.png", testPNG(640, 640))
		if err != nil {
			return err
		}
		res, err := c.PhotosUploadProfilePhoto(ctx, &tg.PhotosUploadProfilePhotoRequest{File: in})
		if err != nil {
			return err
		}
		photoID = res.Photo.(*tg.Photo).ID
		if me := res.Users[0].(*tg.User); me.Photo.(*tg.UserProfilePhoto).PhotoID != photoID {
			return fmt.Errorf("own user after upload %+v", me.Photo)
		}

		created, err := c.ChannelsCreateChannel(ctx, &tg.ChannelsCreateChannelRequest{Title: "pics", Broadcast: true})
		if err != nil {
			return err
		}
		ch = channelOf(t, created)
		in, err = uploader.NewUploader(c).FromBytes(ctx, "chan.png", testPNG(300, 300))
		if err != nil {
			return err
		}
		edited, err := c.ChannelsEditPhoto(ctx, &tg.ChannelsEditPhotoRequest{Channel: inCh(ch), Photo: &tg.InputChatUploadedPhoto{File: in}})
		if err != nil {
			return err
		}
		ch = channelOf(t, edited)
		if _, ok := ch.Photo.(*tg.ChatPhoto); !ok {
			return fmt.Errorf("channel photo %T", ch.Photo)
		}
		full, err := c.ChannelsGetFullChannel(ctx, inCh(ch))
		if err != nil {
			return err
		}
		if p, ok := full.FullChat.(*tg.ChannelFull).ChatPhoto.(*tg.Photo); !ok || p.ID != ch.Photo.(*tg.ChatPhoto).PhotoID {
			return fmt.Errorf("full channel photo %+v", full.FullChat.(*tg.ChannelFull).ChatPhoto)
		}
		hist, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: peerCh(ch), Limit: 10})
		if err != nil {
			return err
		}
		svc, ok := hist.(*tg.MessagesChannelMessages).Messages[0].(*tg.MessageService)
		if !ok {
			return fmt.Errorf("no service message for the photo")
		}
		if _, ok := svc.Action.(*tg.MessageActionChatEditPhoto); !ok {
			return fmt.Errorf("action %T", svc.Action)
		}

		// a file in the channel, found by the documents filter
		in, err = uploader.NewUploader(c).FromBytes(ctx, "notes.txt", []byte("channel file"))
		if err != nil {
			return err
		}
		if _, err := c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: peerCh(ch), RandomID: 1, Message: "notes",
			Media: &tg.InputMediaUploadedDocument{File: in, MimeType: "text/plain"}}); err != nil {
			return err
		}
		found, err := c.MessagesSearch(ctx, &tg.MessagesSearchRequest{Peer: peerCh(ch), Filter: &tg.InputMessagesFilterDocument{}, Limit: 10})
		if err != nil {
			return err
		}
		if n := len(found.(*tg.MessagesChannelMessages).Messages); n != 1 {
			return fmt.Errorf("channel documents %d", n)
		}
		_, err = c.ChannelsUpdateUsername(ctx, &tg.ChannelsUpdateUsernameRequest{Channel: inCh(ch), Username: "picschan"})
		return err
	})

	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		users, err := c.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUser{UserID: alice.id}})
		if err != nil {
			return err
		}
		pp, ok := users[0].(*tg.User).Photo.(*tg.UserProfilePhoto)
		if !ok || pp.PhotoID != photoID || pp.DCID != dc {
			return fmt.Errorf("alice as bob sees her: %+v", users[0].(*tg.User).Photo)
		}
		list, err := c.PhotosGetUserPhotos(ctx, &tg.PhotosGetUserPhotosRequest{UserID: &tg.InputUser{UserID: alice.id}, Limit: 10})
		if err != nil {
			return err
		}
		if ps := list.(*tg.PhotosPhotos).Photos; len(ps) != 1 || ps[0].(*tg.Photo).ID != photoID {
			return fmt.Errorf("getUserPhotos %+v", ps)
		}
		for _, big := range []bool{false, true} {
			raw, err := download(ctx, c, &tg.InputPeerPhotoFileLocation{Big: big, Peer: userPeer(alice.id), PhotoID: photoID})
			if err != nil {
				return err
			}
			cfg, err := jpeg.DecodeConfig(bytes.NewReader(raw))
			if err != nil || big && cfg.Width != 640 || !big && cfg.Width != 320 {
				return fmt.Errorf("peer photo big=%v: %v %dx%d", big, err, cfg.Width, cfg.Height)
			}
		}
		if _, err := download(ctx, c, &tg.InputPeerPhotoFileLocation{Peer: userPeer(alice.id), PhotoID: photoID + 1}); !tgerr.Is(err, "LOCATION_INVALID") {
			return fmt.Errorf("someone else's photo id: %v", err)
		}
		// the channel is public now, so bob can see its picture
		raw, err := download(ctx, c, &tg.InputPeerPhotoFileLocation{Peer: peerCh(ch), PhotoID: ch.Photo.(*tg.ChatPhoto).PhotoID})
		if err != nil || len(raw) == 0 {
			return fmt.Errorf("channel photo download: %v", err)
		}
		return nil
	})

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		gone, err := c.PhotosDeletePhotos(ctx, []tg.InputPhotoClass{&tg.InputPhoto{ID: photoID}})
		if err != nil || len(gone) != 1 {
			return fmt.Errorf("delete %v %v", err, gone)
		}
		users, err := c.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
		if err != nil {
			return err
		}
		if p := users[0].(*tg.User).Photo; p != nil {
			if _, empty := p.(*tg.UserProfilePhotoEmpty); !empty {
				return fmt.Errorf("photo after delete %+v", p)
			}
		}
		return nil
	})
}

// bundle writes a pack bundle the way the old server lays it out.
func bundle(t *testing.T, packs []map[string]any, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	man, _ := json.Marshal(map[string]any{"version": 1, "packs": packs})
	sum := sha256.Sum256(man)
	build := hex.EncodeToString(sum[:])
	root := filepath.Join(dir, "builds", build)
	os.MkdirAll(filepath.Join(root, "assets"), 0o755)
	os.WriteFile(filepath.Join(root, "manifest.json"), man, 0o644)
	for name, b := range files {
		os.WriteFile(filepath.Join(root, name), b, 0o644)
	}
	act, _ := json.Marshal(map[string]any{"version": 1, "build": build})
	os.WriteFile(filepath.Join(dir, "active.json"), act, 0o644)
	return dir
}

func asset(b []byte, mime, ext, emoji, customID string) (map[string]any, string) {
	sum := sha256.Sum256(b)
	h := hex.EncodeToString(sum[:])
	it := map[string]any{"file": "assets/" + h + "." + ext, "sha256": h, "size": len(b), "mime": mime, "width": 512, "height": 512,
		"emoji": emoji, "sourceId": customID, "customEmojiId": nil}
	if customID != "" {
		it["customEmojiId"] = customID
	} else {
		it["sourceId"] = "local-" + h[:8]
	}
	return it, "assets/" + h + "." + ext
}

func TestStickerPacks(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	tgs := randomBytes(1500)
	webp := randomBytes(900)
	icon := randomBytes(700)
	e1, f1 := asset(tgs, "application/x-tgsticker", "tgs", "🔤", "5226734466315067436")
	s1, f2 := asset(webp, "image/webp", "webp", "😀", "")
	t1, f3 := asset(icon, "application/x-tgsticker", "tgs", "📰", "5420331611830886484")
	dir := bundle(t, []map[string]any{
		{"name": "abcemoji", "title": "ABC Emoji", "kind": "emoji", "sourceName": "ABCEmoji", "sourceHash": hex.EncodeToString(make([]byte, 32)),
			"official": false, "verification": "unverified", "sources": []string{"GetEmojiStickers"}, "premiumOnly": true, "textColor": false,
			"stickers": []any{e1}},
		{"name": "smiles", "title": "Smiles", "kind": "sticker", "sourceName": "Smiles", "sourceHash": hex.EncodeToString(make([]byte, 32)),
			"official": false, "verification": "unverified", "sources": []string{}, "premiumOnly": true, "textColor": false,
			"stickers": []any{s1}},
		{"name": "topics", "title": "Topic Icons", "kind": "emoji", "sourceName": "Topics", "sourceHash": hex.EncodeToString(make([]byte, 32)),
			"official": true, "verification": "mtproto-official-flag", "sources": []string{"builtin:InputStickerSetEmojiDefaultTopicIcons"},
			"premiumOnly": true, "textColor": true, "stickers": []any{t1}},
	}, map[string][]byte{f1: tgs, f2: webp, f3: icon})
	for range 2 {
		res, err := s.media.ImportPacks(ctx, dir)
		if err != nil || res.Packs != 3 || res.Stickers != 3 {
			t.Fatalf("import %v %+v", err, res)
		}
	}

	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		emoji, err := c.MessagesGetEmojiStickers(ctx, 0)
		if err != nil {
			return err
		}
		all := emoji.(*tg.MessagesAllStickers)
		if len(all.Sets) != 1 || all.Sets[0].ShortName != "abcemoji" || !all.Sets[0].Emojis {
			return fmt.Errorf("emoji sets %+v", all.Sets)
		}
		if again, err := c.MessagesGetEmojiStickers(ctx, all.Hash); err != nil {
			return err
		} else if _, ok := again.(*tg.MessagesAllStickersNotModified); !ok {
			return fmt.Errorf("same hash gave %T", again)
		}
		set, err := c.MessagesGetStickerSet(ctx, &tg.MessagesGetStickerSetRequest{Stickerset: &tg.InputStickerSetShortName{ShortName: "ABCEMOJI"}})
		if err != nil {
			return err
		}
		ss := set.(*tg.MessagesStickerSet)
		if len(ss.Documents) != 1 || len(ss.Packs) != 1 || ss.Packs[0].Emoticon != "🔤" || ss.Documents[0].(*tg.Document).ID != 5226734466315067436 {
			return fmt.Errorf("sticker set %+v", ss)
		}
		doc := ss.Documents[0].(*tg.Document)
		raw, err := download(ctx, c, docLocation(doc))
		if err != nil || !bytes.Equal(raw, tgs) {
			return fmt.Errorf("emoji bytes: %v", err)
		}
		docs, err := c.MessagesGetCustomEmojiDocuments(ctx, []int64{doc.ID, 42})
		if err != nil || len(docs) != 2 {
			return fmt.Errorf("custom emoji documents %v %d", err, len(docs))
		}
		if e, ok := docs[1].(*tg.DocumentEmpty); !ok || e.ID != 42 {
			return fmt.Errorf("unknown custom emoji %+v", docs[1])
		}
		topics, err := c.MessagesGetStickerSet(ctx, &tg.MessagesGetStickerSetRequest{Stickerset: &tg.InputStickerSetEmojiDefaultTopicIcons{}})
		if err != nil || len(topics.(*tg.MessagesStickerSet).Documents) != 1 {
			return fmt.Errorf("topic icons %v", err)
		}
		if _, err := c.MessagesGetStickerSet(ctx, &tg.MessagesGetStickerSetRequest{Stickerset: &tg.InputStickerSetID{ID: 1, AccessHash: 2}}); !tgerr.Is(err, "STICKERSET_INVALID") {
			return fmt.Errorf("unknown set id: %v", err)
		}
		// a built-in set nobody seeded is an empty set, not an error
		if dice, err := c.MessagesGetStickerSet(ctx, &tg.MessagesGetStickerSetRequest{Stickerset: &tg.InputStickerSetDice{Emoticon: "🎲"}}); err != nil {
			return err
		} else if len(dice.(*tg.MessagesStickerSet).Documents) != 0 {
			return fmt.Errorf("dice set %+v", dice)
		}

		stickers, err := c.MessagesGetAllStickers(ctx, 0)
		if err != nil {
			return err
		}
		sets := stickers.(*tg.MessagesAllStickers).Sets
		if len(sets) != 1 || sets[0].ShortName != "smiles" {
			return fmt.Errorf("sticker sets %+v", sets)
		}
		smiles := &tg.InputStickerSetID{ID: sets[0].ID, AccessHash: sets[0].AccessHash}
		if _, err := c.MessagesUninstallStickerSet(ctx, smiles); err != nil {
			return err
		}
		if stickers, err = c.MessagesGetAllStickers(ctx, 0); err != nil || len(stickers.(*tg.MessagesAllStickers).Sets) != 0 {
			return fmt.Errorf("after uninstall %v %+v", err, stickers)
		}
		if _, err := c.MessagesInstallStickerSet(ctx, &tg.MessagesInstallStickerSetRequest{Stickerset: smiles}); err != nil {
			return err
		}
		if stickers, err = c.MessagesGetAllStickers(ctx, 0); err != nil || len(stickers.(*tg.MessagesAllStickers).Sets) != 1 {
			return fmt.Errorf("after install %v %+v", err, stickers)
		}

		// sending a sticker is a document re-send
		sticker := &tg.InputDocument{ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference}
		res, err := c.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{Peer: &tg.InputPeerSelf{}, RandomID: 5, Media: &tg.InputMediaDocument{ID: sticker}})
		if err != nil {
			return err
		}
		if d := documentOf(t, sent(t, res)); d.ID != doc.ID {
			return fmt.Errorf("sent sticker %d", d.ID)
		}
		return nil
	})
}
