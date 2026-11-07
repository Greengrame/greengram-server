package api

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/botapi"
	"github.com/Greengrame/greengram-server/internal/bots"
)

// chatTypeOf is chat_type in an inline query and a direct link's launch data:
// "sender" is the person's private chat with the bot itself.
func (a *API) chatTypeOf(ctx context.Context, me account.User, bot int64, to peer) (string, error) {
	if to.channel == 0 {
		if to.user == bot {
			return "sender", nil
		}
		return "private", nil
	}
	c, _, err := a.chans.Access(ctx, to.channel, me.ID)
	if err != nil {
		return "", err
	}
	if c.Megagroup {
		return "supergroup", nil
	}
	return "channel", nil
}

// getInlineBotResults is "@bot query" in the composer: the bot gets an
// inline_query and the person waits for answerInlineQuery, unless an earlier
// answer is still within its cache_time.
func (a *API) getInlineBotResults(ctx context.Context, r *tg.MessagesGetInlineBotResultsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	b, err := a.bot(ctx, me, r.Bot)
	if err != nil {
		return nil, err
	}
	if !bool(b.State.Settings.Inline) || !b.Usable(me.ID) {
		return nil, tgerr.New(400, "BOT_INLINE_DISABLED")
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	chatType, err := a.chatTypeOf(ctx, me, b.UserID, to)
	if err != nil {
		return nil, err
	}
	if len(r.Query) > 512 {
		return nil, tgerr.New(400, "QUERY_TOO_LONG")
	}
	q, cached, err := a.bots.CachedInline(ctx, b.UserID, me.ID, r.Query, r.Offset)
	if err != nil {
		return nil, err
	}
	if !cached {
		id, err := a.bots.AskInline(ctx, bots.InlineQuery{Bot: b.UserID, User: me.ID, Query: r.Query, Offset: r.Offset, ChatType: chatType})
		if err != nil {
			return nil, err
		}
		var answered bool
		q, answered, err = a.bots.WaitInline(ctx, id, bots.InlineTimeout)
		if err != nil {
			return nil, err
		}
		if !answered {
			return nil, tgerr.New(400, "BOT_RESPONSE_TIMEOUT")
		}
	}
	out := &tg.MessagesBotResults{QueryID: q.ID, CacheTime: max(int(time.Until(q.Cache).Seconds()), 0), Results: []tg.BotInlineResultClass{}}
	for _, res := range q.Answer.Results {
		t, err := botapi.InlineResultTL(res)
		if err != nil {
			// answerInlineQuery checked it; don't let one bad row hide the rest
			slog.Warn("api: inline result", "bot", b.UserID, "err", err)
			continue
		}
		out.Results = append(out.Results, t)
	}
	if q.Answer.NextOffset != "" {
		out.SetNextOffset(q.Answer.NextOffset)
	}
	if q.Answer.ButtonText != "" {
		if q.Answer.ButtonApp != "" {
			out.SetSwitchWebview(tg.InlineBotWebView{Text: q.Answer.ButtonText, URL: q.Answer.ButtonApp})
		} else {
			out.SetSwitchPm(tg.InlineBotSwitchPM{Text: q.Answer.ButtonText, StartParam: q.Answer.ButtonStart})
		}
	}
	if out.Users, err = a.usersFor(ctx, me.ID, []int64{b.UserID}); err != nil {
		return nil, err
	}
	return out, nil
}

// sendInlineBotResult sends a result as the person, marked "via @bot".
func (a *API) sendInlineBotResult(ctx context.Context, r *tg.MessagesSendInlineBotResultRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	to, err := a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	// the scheduled table keeps neither the bot nor its keyboard
	if r.ScheduleDate > now() {
		return nil, tgerr.New(400, "SCHEDULE_BOT_NOT_ALLOWED")
	}
	q, ok, err := a.bots.InlineQueryByID(ctx, r.QueryID)
	if err != nil {
		return nil, err
	}
	if !ok || !q.Answered || q.Personal && q.User != me.ID {
		return nil, tgerr.New(400, "QUERY_ID_INVALID")
	}
	var res *bots.InlineResult
	for i := range q.Answer.Results {
		if q.Answer.Results[i].ID == r.ID {
			res = &q.Answer.Results[i]
		}
	}
	if res == nil {
		return nil, tgerr.New(400, "RESULT_ID_INVALID")
	}
	b, ok, err := a.bots.Get(ctx, q.Bot)
	if err != nil {
		return nil, err
	}
	if !ok || b.Disabled || !bool(b.State.Settings.Inline) || !b.Usable(me.ID) {
		return nil, tgerr.New(400, "BOT_INLINE_DISABLED")
	}
	if to.channel != 0 {
		can, err := a.chans.CanSendInline(ctx, me.ID, to.channel)
		if err != nil {
			return nil, err
		}
		if !can {
			return nil, tgerr.New(403, "CHAT_SEND_INLINE_FORBIDDEN")
		}
	}
	text, ents, markup, err := botapi.InlineMessage(*res)
	if err != nil {
		return nil, tgerr.New(400, "RESULT_ID_INVALID")
	}
	reply, topic := 0, 0
	if r.ReplyTo != nil {
		reply, topic = replyID(r.ReplyTo), replyTopic(r.ReplyTo)
	}
	a.presence.set(me.ID, true)
	o := outgoing{text: text, entities: ents, replyTo: reply, topic: topic, randomID: r.RandomID, silent: r.Silent, noWebpage: res.NoWebpage, via: b.UserID, markup: markup}
	out, err := a.sendTo(ctx, me, to, []outgoing{o})
	if err != nil {
		return nil, err
	}
	if r.ClearDraft {
		a.dropSentDraft(ctx, me, to, r.ReplyTo)
	}
	if n := b.State.FeedbackEvery(); n > 0 && rand.N(n) == 0 {
		a.tellChosen(ctx, b, me, q, *res, markup != nil, to, out)
	}
	return out, nil
}

// tellChosen queues chosen_inline_result. inline_message_id comes along only when
// the message has a keyboard, as on Telegram: without one there's nothing to edit.
func (a *API) tellChosen(ctx context.Context, b bots.Bot, me account.User, q bots.InlineQuery, res bots.InlineResult, keyboard bool, to peer, sent *tg.Updates) {
	c := bots.Chosen{ResultID: res.ID, User: me.ID, Query: q.Query}
	if keyboard {
		for _, u := range sent.Updates {
			if m, ok := u.(*tg.UpdateMessageID); ok {
				if to.channel != 0 {
					c.InlineID = bots.ChannelInlineID(to.channel, m.ID)
				} else if list, err := a.msgs.Get(ctx, me.ID, []int{m.ID}); err == nil && len(list) == 1 {
					c.InlineID = bots.PrivateInlineID(list[0].Shared)
				}
			}
		}
	}
	if err := a.bots.QueueChosen(ctx, b.UserID, c); err != nil {
		slog.Warn("api: chosen inline result", "bot", b.UserID, "err", err)
	}
}
