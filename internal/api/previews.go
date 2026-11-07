package api

import (
	"context"
	"log/slog"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/webpage"
)

// previewWait is how long getWebPagePreview waits for a fetch; the app
// asks again as the user keeps typing.
const previewWait = 6 * time.Second

// RegisterPreviews turns on link previews. Needs RegisterMedia first, the
// previews' pictures are photos.
func (a *API) RegisterPreviews(w *webpage.Service) {
	a.pages = w
	handle(a, tlprofile.SemanticMethodMessagesGetWebPagePreview, a.webPagePreview)
	handle(a, tlprofile.SemanticMethodMessagesGetWebPage, a.getWebPage)
}

// PreviewPhoto is the photo hook webpage.New wants.
func (a *API) PreviewPhoto(ctx context.Context, raw []byte) (int64, error) {
	p, err := a.media.NewPhoto(ctx, 0, raw)
	return p.ID, err
}

func (a *API) webPageTL(ctx context.Context, p webpage.Preview) tg.WebPageClass {
	w := &tg.WebPage{ID: p.ID, URL: p.URL, DisplayURL: p.DisplayURL, Hash: p.Hash}
	w.SetType(p.Type)
	if p.SiteName != "" {
		w.SetSiteName(p.SiteName)
	}
	if p.Title != "" {
		w.SetTitle(p.Title)
	}
	if p.Description != "" {
		w.SetDescription(p.Description)
	}
	if p.PhotoID != 0 {
		if ph, ok, err := a.media.Photo(ctx, p.PhotoID); err == nil && ok {
			w.SetPhoto(a.media.PhotoTL(ph))
		}
	}
	return w
}

func (a *API) previewRef(ctx context.Context, p webpage.Preview, large, small bool) media.Ref {
	m := &tg.MessageMediaWebPage{Webpage: a.webPageTL(ctx, p), ForceLargeMedia: large, ForceSmallMedia: small}
	return media.Ref{Kind: media.KindWebPage, ID: p.ID, TL: m}
}

func (a *API) webPagePreview(ctx context.Context, r *tg.MessagesGetWebPagePreviewRequest) (any, error) {
	if _, err := a.me(ctx); err != nil {
		return nil, err
	}
	out := &tg.MessagesWebPagePreview{Media: &tg.MessageMediaEmpty{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
	u := webpage.FirstURL(r.Message, r.Entities)
	if u == "" {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, previewWait)
	defer cancel()
	p, ok, err := a.pages.Resolve(ctx, u)
	if err != nil {
		return nil, err
	}
	if ok {
		out.Media = a.previewRef(ctx, p, false, false).TL
	}
	return out, nil
}

func (a *API) getWebPage(ctx context.Context, r *tg.MessagesGetWebPageRequest) (any, error) {
	if _, err := a.me(ctx); err != nil {
		return nil, err
	}
	out := &tg.MessagesWebPage{Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
	u := webpage.Normalize(r.URL)
	if u == "" {
		out.Webpage = &tg.WebPageEmpty{URL: r.URL}
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, previewWait)
	defer cancel()
	p, ok, err := a.pages.Resolve(ctx, u)
	if err != nil {
		return nil, err
	}
	if !ok {
		out.Webpage = &tg.WebPageEmpty{ID: webpage.ID(u), URL: u}
		return out, nil
	}
	if r.Hash != 0 && r.Hash == p.Hash {
		out.Webpage = &tg.WebPageNotModified{}
		return out, nil
	}
	out.Webpage = a.webPageTL(ctx, p)
	return out, nil
}

// cachedPreview is a preview ready to go on a message being sent; the
// link to fetch in the background when there's none yet.
func (a *API) cachedPreview(ctx context.Context, o outgoing) (media.Ref, string) {
	if a.pages == nil || o.noWebpage || !o.media.Empty() || !o.fwd.Empty() {
		return media.Ref{}, ""
	}
	u := webpage.FirstURL(o.text, o.entities)
	if u == "" {
		return media.Ref{}, ""
	}
	p, ok, fresh, err := a.pages.Cached(ctx, u)
	if err != nil {
		slog.Warn("api: cached preview", "err", err)
		return media.Ref{}, ""
	}
	if ok {
		// an old one beats none; the next message gets a fresh one
		return a.previewRef(ctx, p, false, false), ""
	}
	if fresh {
		return media.Ref{}, "" // known to have no preview
	}
	return media.Ref{}, u
}

// pendingPreview fetches a link after its message went out and puts the
// preview on as an edit, for everyone including the sender's sessions.
type pendingPreview struct {
	url     string
	user    int64 // whose box the id is in, for private chats
	channel int64
	id      int
}

func (a *API) attachLater(list []pendingPreview) {
	if len(list) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*previewWait)
		defer cancel()
		for _, pp := range list {
			p, ok, err := a.pages.Resolve(ctx, pp.url)
			if err != nil || !ok {
				continue
			}
			ref := a.previewRef(ctx, p, false, false)
			if pp.channel != 0 {
				events, err := a.chans.AttachPreview(ctx, pp.channel, pp.id, ref)
				if err != nil {
					slog.Warn("api: attach preview", "err", err)
					continue
				}
				a.deliverChannel(a.oneEach(events), edge.Session{})
				continue
			}
			events, err := a.msgs.AttachPreview(ctx, pp.user, pp.id, ref)
			if err != nil {
				slog.Warn("api: attach preview", "err", err)
				continue
			}
			a.deliver(events, edge.Session{})
		}
	}()
}

// sendWebPage is a message whose link preview the sender chose (large or
// small picture); the text carries the link.
func (a *API) sendWebPage(ctx context.Context, me account.User, to peer, r *tg.MessagesSendMediaRequest, wp *tg.InputMediaWebPage) (any, error) {
	o := outgoing{text: r.Message, entities: r.Entities, replyTo: replyID(r.ReplyTo), randomID: r.RandomID, silent: r.Silent, noWebpage: true}
	if o.text == "" {
		o.text = wp.URL
	}
	if u := webpage.Normalize(wp.URL); u != "" {
		wctx, cancel := context.WithTimeout(ctx, previewWait)
		p, ok, err := a.pages.Resolve(wctx, u)
		cancel()
		if err != nil {
			return nil, err
		}
		if ok {
			o.media = a.previewRef(ctx, p, wp.ForceLargeMedia, wp.ForceSmallMedia)
		}
	}
	a.presence.set(me.ID, true)
	out, err := a.sendTo(ctx, me, to, []outgoing{o})
	if err == nil && r.ClearDraft {
		a.dropSentDraft(ctx, me, to, r.ReplyTo)
	}
	return out, err
}
