package e2e

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

// TestArticles: rich messages go through to the other side as sent; only
// Premium writes them, and blocks that aren't safe to show are refused.
func TestArticles(t *testing.T) {
	s := start(t)
	alice := s.signUp(t, "alice")
	bob := s.signUp(t, "bob")
	article := &tg.InputRichMessage{Blocks: []tg.PageBlockClass{
		&tg.PageBlockHeading1{Text: &tg.TextPlain{Text: "Field notes"}},
		&tg.PageBlockParagraph{Text: &tg.TextConcat{Texts: []tg.RichTextClass{&tg.TextBold{Text: &tg.TextPlain{Text: "bold "}}, &tg.TextURL{Text: &tg.TextPlain{Text: "link"}, URL: "https://example.com/"}}}},
		&tg.PageBlockDivider{},
	}}
	send := func(ctx context.Context, c *tg.Client, id int64, in tg.InputRichMessageClass) error {
		r := &tg.MessagesSendMessageRequest{Peer: userPeer(bob.id), RandomID: id}
		r.SetRichMessage(in)
		_, err := c.MessagesSendMessage(ctx, r)
		return err
	}
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		if err := send(ctx, c, 1, article); !tgerr.Is(err, "PREMIUM_ACCOUNT_REQUIRED") {
			return fmt.Errorf("without Premium: %v", err)
		}
		if _, err := s.db.Pool.Exec(ctx, "UPDATE users SET premium_until=extract(epoch FROM now())::bigint + 3600 WHERE username='alice'"); err != nil {
			return err
		}
		nested := tg.RichTextClass(&tg.TextPlain{Text: "deep"})
		for range 100 {
			nested = &tg.TextBold{Text: nested}
		}
		for name, c2 := range map[string]struct {
			in   tg.PageBlockClass
			want string
		}{
			"embed":      {&tg.PageBlockEmbed{HTML: "<script>alert(1)</script>", Caption: tg.PageCaption{Text: &tg.TextEmpty{}, Credit: &tg.TextEmpty{}}}, "RICH_MESSAGE_INVALID"},
			"javascript": {&tg.PageBlockParagraph{Text: &tg.TextURL{Text: &tg.TextPlain{Text: "x"}, URL: "javascript:alert(1)"}}, "RICH_MESSAGE_INVALID"},
			"too long":   {&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: strings.Repeat("a", 32769)}}, "MESSAGE_TOO_LONG"},
		} {
			if err := send(ctx, c, 2, &tg.InputRichMessage{Blocks: []tg.PageBlockClass{c2.in}}); !tgerr.Is(err, c2.want) {
				return fmt.Errorf("%s: %v", name, err)
			}
		}
		// the edge's decoder may refuse a tree this deep before the check does
		if err := send(ctx, c, 2, &tg.InputRichMessage{Blocks: []tg.PageBlockClass{&tg.PageBlockParagraph{Text: nested}}}); !tgerr.Is(err, "RICH_MESSAGE_TOO_COMPLEX", "INPUT_REQUEST_INVALID") {
			return fmt.Errorf("too deep: %v", err)
		}
		if err := send(ctx, c, 3, &tg.InputRichMessageMarkdown{Markdown: "# hi"}); !tgerr.Is(err, "RICH_MESSAGE_FORMAT_UNSUPPORTED") {
			return fmt.Errorf("markdown: %v", err)
		}
		if err := send(ctx, c, 4, article); err != nil {
			return fmt.Errorf("article: %w", err)
		}
		// past the 4096 of ordinary messages, as articles may be
		long := &tg.InputRichMessage{Blocks: []tg.PageBlockClass{&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: strings.Repeat("b", 5000)}}}}
		return send(ctx, c, 5, long)
	})
	var bobsID int
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: userPeer(alice.id), Limit: 10})
		if err != nil {
			return err
		}
		list := res.(*tg.MessagesMessagesSlice).Messages
		if len(list) != 2 {
			return fmt.Errorf("history: %d", len(list))
		}
		m := list[1].(*tg.Message)
		rich, ok := m.GetRichMessage()
		if !ok || len(rich.Blocks) != 3 {
			return fmt.Errorf("bob's copy: %+v", m)
		}
		if _, ok := rich.Blocks[0].(*tg.PageBlockHeading1); !ok {
			return fmt.Errorf("first block: %T", rich.Blocks[0])
		}
		if !strings.Contains(m.Message, "Field notes") || !strings.Contains(m.Message, "bold link") {
			return fmt.Errorf("plain words: %q", m.Message)
		}
		bobsID = m.ID
		// anyone can pass an article on, Premium or not
		if _, err := c.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{FromPeer: userPeer(alice.id), ToPeer: &tg.InputPeerSelf{}, ID: []int{bobsID}, RandomID: []int64{7}}); err != nil {
			return fmt.Errorf("forward: %w", err)
		}
		saved, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: &tg.InputPeerSelf{}, Limit: 1})
		if err != nil {
			return err
		}
		if _, ok := saved.(*tg.MessagesMessagesSlice).Messages[0].(*tg.Message).GetRichMessage(); !ok {
			return fmt.Errorf("the forward lost the article")
		}
		return nil
	})
	run(t, s.client(alice.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: userPeer(bob.id), Limit: 10})
		if err != nil {
			return err
		}
		id := res.(*tg.MessagesMessagesSlice).Messages[1].GetID()
		r := &tg.MessagesEditMessageRequest{Peer: userPeer(bob.id), ID: id}
		r.SetRichMessage(&tg.InputRichMessage{Blocks: []tg.PageBlockClass{&tg.PageBlockHeading2{Text: &tg.TextPlain{Text: "Edited"}}}})
		if _, err := c.MessagesEditMessage(ctx, r); err != nil {
			return fmt.Errorf("edit: %w", err)
		}
		plain := &tg.MessagesEditMessageRequest{Peer: userPeer(bob.id), ID: id}
		plain.SetMessage("just words now")
		if _, err := c.MessagesEditMessage(ctx, plain); err != nil {
			return fmt.Errorf("edit to plain: %w", err)
		}
		return nil
	})
	run(t, s.client(bob.storage), func(ctx context.Context, c *tg.Client) error {
		res, err := c.MessagesGetMessages(ctx, []tg.InputMessageClass{&tg.InputMessageID{ID: bobsID}})
		if err != nil {
			return err
		}
		m := res.(*tg.MessagesMessages).Messages[0].(*tg.Message)
		if _, ok := m.GetRichMessage(); ok || m.Message != "just words now" {
			return fmt.Errorf("after the edit: %+v", m)
		}
		return nil
	})
}
