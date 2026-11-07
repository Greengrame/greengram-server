package e2e

import (
	"context"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

func TestEmojiStatus(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pack := func(name string, stickers ...any) map[string]any {
		return map[string]any{"name": name, "title": name, "kind": "emoji", "sourceName": name, "sourceHash": hex.EncodeToString(make([]byte, 32)),
			"official": false, "verification": "unverified", "sources": []string{"GetEmojiStickers"}, "premiumOnly": true, "textColor": false,
			"stickers": stickers}
	}
	a, b, u := randomBytes(1500), randomBytes(1400), randomBytes(1300)
	e1, f1 := asset(a, "application/x-tgsticker", "tgs", "🔤", "5226734466315067436")
	e2, f2 := asset(b, "application/x-tgsticker", "tgs", "🦆", "5366316836101038579")
	e3, f3 := asset(u, "application/x-tgsticker", "tgs", "🤩", "5334523697174683404")
	dir := bundle(t, []map[string]any{pack("abcemoji", e1, e2), pack("EmojiUzbek", e3)}, map[string][]byte{f1: a, f2: b, f3: u})
	res, err := s.media.ImportPacks(ctx, dir)
	if err != nil || res.Packs != 1 {
		t.Fatalf("import %v %+v", err, res)
	}

	const first, second = int64(5226734466315067436), int64(5366316836101038579)
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		// a default pack counts as installed with no row of its own
		emoji, err := c.MessagesGetEmojiStickers(ctx, 0)
		if err != nil {
			return err
		}
		sets := emoji.(*tg.MessagesAllStickers).Sets
		if len(sets) != 1 || sets[0].ShortName != "abcemoji" {
			return fmt.Errorf("installed %+v", sets)
		}
		if _, err := c.MessagesGetStickerSet(ctx, &tg.MessagesGetStickerSetRequest{Stickerset: &tg.InputStickerSetID{ID: 1, AccessHash: 1}}); !tgerr.Is(err, "STICKERSET_INVALID") {
			return fmt.Errorf("unknown set: %v", err)
		}

		defaults, err := c.MessagesGetStickerSet(ctx, &tg.MessagesGetStickerSetRequest{Stickerset: &tg.InputStickerSetEmojiDefaultStatuses{}})
		if err != nil {
			return err
		}
		docs := defaults.(*tg.MessagesStickerSet).Documents
		if len(docs) != 1 || docs[0].(*tg.Document).ID != first {
			return fmt.Errorf("default set %+v", docs)
		}
		list, err := c.AccountGetDefaultEmojiStatuses(ctx, 0)
		if err != nil {
			return err
		}
		all := list.(*tg.AccountEmojiStatuses)
		if len(all.Statuses) != 1 || all.Statuses[0].(*tg.EmojiStatus).DocumentID != first {
			return fmt.Errorf("default statuses %+v", all)
		}
		if again, err := c.AccountGetDefaultEmojiStatuses(ctx, all.Hash); err != nil {
			return err
		} else if _, ok := again.(*tg.AccountEmojiStatusesNotModified); !ok {
			return fmt.Errorf("same hash gave %T", again)
		}

		set := func(doc int64) error {
			_, err := c.AccountUpdateEmojiStatus(ctx, &tg.EmojiStatus{DocumentID: doc})
			return err
		}
		if err := set(first); !tgerr.Is(err, "PREMIUM_ACCOUNT_REQUIRED") {
			return fmt.Errorf("free account: %v", err)
		}
		if _, err := s.db.Pool.Exec(ctx, "UPDATE users SET premium_until=extract(epoch FROM now())::bigint + 3600 WHERE username='alice'"); err != nil {
			return err
		}
		if err := set(42); !tgerr.Is(err, "DOCUMENT_INVALID") {
			return fmt.Errorf("not an emoji: %v", err)
		}
		for _, d := range []int64{first, second} {
			if err := set(d); err != nil {
				return err
			}
		}
		recent := func() ([]tg.EmojiStatusClass, error) {
			out, err := c.AccountGetRecentEmojiStatuses(ctx, 0)
			if err != nil {
				return nil, err
			}
			return out.(*tg.AccountEmojiStatuses).Statuses, nil
		}
		got, err := recent()
		if err != nil {
			return err
		}
		if len(got) != 2 || got[0].(*tg.EmojiStatus).DocumentID != second || got[1].(*tg.EmojiStatus).DocumentID != first {
			return fmt.Errorf("recent %+v", got)
		}
		me, err := c.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
		if err != nil {
			return err
		}
		if st, ok := me[0].(*tg.User).GetEmojiStatus(); !ok || st.(*tg.EmojiStatus).DocumentID != second {
			return fmt.Errorf("own status %+v", me[0])
		}
		if _, err := c.AccountUpdateEmojiStatus(ctx, &tg.EmojiStatusEmpty{}); err != nil {
			return err
		}
		if me, err = c.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}}); err != nil {
			return err
		}
		if _, ok := me[0].(*tg.User).GetEmojiStatus(); ok {
			return fmt.Errorf("status not cleared: %+v", me[0])
		}
		if got, err = recent(); err != nil || len(got) != 2 {
			return fmt.Errorf("recent after clearing the status %v %+v", err, got)
		}
		if _, err := c.AccountClearRecentEmojiStatuses(ctx); err != nil {
			return err
		}
		if got, err = recent(); err != nil || len(got) != 0 {
			return fmt.Errorf("recent after clearing %v %+v", err, got)
		}

		// a removed default pack stays removed
		abc := &tg.InputStickerSetID{ID: sets[0].ID, AccessHash: sets[0].AccessHash}
		if _, err := c.MessagesUninstallStickerSet(ctx, abc); err != nil {
			return err
		}
		if _, err := s.media.ImportPacks(ctx, dir); err != nil {
			return err
		}
		emoji, err = c.MessagesGetEmojiStickers(ctx, 0)
		if err != nil {
			return err
		}
		if n := len(emoji.(*tg.MessagesAllStickers).Sets); n != 0 {
			return fmt.Errorf("uninstalled pack came back: %d sets", n)
		}
		return nil
	})
}
