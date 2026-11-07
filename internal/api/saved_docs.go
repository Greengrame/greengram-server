package api

import (
	"context"
	"log/slog"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/media"
)

// Saved GIFs, recently sent stickers and favourite stickers. Each change
// tells the user's other sessions with an empty update, and they reload
// the list.
func (a *API) registerSavedDocs() {
	handle(a, tlprofile.SemanticMethodMessagesGetSavedGifs, a.getSavedGifs)
	handle(a, tlprofile.SemanticMethodMessagesSaveGif, a.saveGif)
	handle(a, tlprofile.SemanticMethodMessagesGetRecentStickers, a.getRecentStickers)
	handle(a, tlprofile.SemanticMethodMessagesSaveRecentSticker, a.saveRecentSticker)
	handle(a, tlprofile.SemanticMethodMessagesClearRecentStickers, a.clearRecentStickers)
	handle(a, tlprofile.SemanticMethodMessagesGetFavedStickers, a.getFavedStickers)
	handle(a, tlprofile.SemanticMethodMessagesFaveSticker, a.faveSticker)
}

func (a *API) docs(list []media.Saved) []tg.DocumentClass {
	out := make([]tg.DocumentClass, 0, len(list))
	for _, v := range list {
		out = append(out, a.media.DocumentTL(v.Doc))
	}
	return out
}

// listChanged tells the user's sessions but skip that a list changed.
func (a *API) listChanged(user int64, list string, skip edge.Session) {
	var u tg.UpdateClass
	switch list {
	case media.ListGIFs:
		u = &tg.UpdateSavedGifs{}
	case media.ListRecent:
		u = &tg.UpdateRecentStickers{}
	case media.ListFaved:
		u = &tg.UpdateFavedStickers{}
	default:
		return
	}
	up := &tg.Updates{Updates: []tg.UpdateClass{u}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: now()}
	a.out.add(user, func(ctx context.Context) { a.push(ctx, user, up, skip) })
}

func (a *API) getSavedGifs(ctx context.Context, r *tg.MessagesGetSavedGifsRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	list, err := a.media.SavedDocs(ctx, me.ID, media.ListGIFs)
	if err != nil {
		return nil, err
	}
	h := media.SavedHash(list)
	if r.Hash != 0 && r.Hash == h {
		return &tg.MessagesSavedGifsNotModified{}, nil
	}
	return &tg.MessagesSavedGifs{Hash: h, Gifs: a.docs(list)}, nil
}

// saveDoc is the shared part of saveGif, saveRecentSticker and faveSticker.
func (a *API) saveDoc(ctx context.Context, me account.User, list string, in tg.InputDocumentClass, on bool, kinds ...string) error {
	d, err := a.media.InputDocument(ctx, in)
	if err != nil {
		return err
	}
	ok := false
	for _, k := range kinds {
		ok = ok || d.Kind == k
	}
	if !ok {
		if list == media.ListGIFs {
			return tgerr.New(400, "GIF_ID_INVALID")
		}
		return tgerr.New(400, "STICKER_ID_INVALID")
	}
	limit := 0
	// premium doubles the favourites, as in Telegram
	if list == media.ListFaved && me.Premium(nowTime()) {
		limit = 10
	}
	changed, err := a.media.Save(ctx, me.ID, list, d.ID, on, limit)
	if err != nil {
		return err
	}
	if changed {
		a.listChanged(me.ID, list, except(ctx))
	}
	return nil
}

func (a *API) saveGif(ctx context.Context, r *tg.MessagesSaveGifRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	return true, a.saveDoc(ctx, me, media.ListGIFs, r.ID, !r.Unsave, media.KindGIF)
}

func (a *API) getRecentStickers(ctx context.Context, r *tg.MessagesGetRecentStickersRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	var list []media.Saved
	// stickers attached to photos aren't a thing here
	if !r.Attached {
		if list, err = a.media.SavedDocs(ctx, me.ID, media.ListRecent); err != nil {
			return nil, err
		}
	}
	h := media.SavedHash(list)
	if r.Hash != 0 && r.Hash == h {
		return &tg.MessagesRecentStickersNotModified{}, nil
	}
	dates := make([]int, 0, len(list))
	for _, v := range list {
		dates = append(dates, v.Date)
	}
	return &tg.MessagesRecentStickers{Hash: h, Packs: media.StickerPacks(list), Stickers: a.docs(list), Dates: dates}, nil
}

func (a *API) saveRecentSticker(ctx context.Context, r *tg.MessagesSaveRecentStickerRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if r.Attached {
		return true, nil
	}
	return true, a.saveDoc(ctx, me, media.ListRecent, r.ID, !r.Unsave, media.KindSticker)
}

func (a *API) clearRecentStickers(ctx context.Context, r *tg.MessagesClearRecentStickersRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	if r.Attached {
		return true, nil
	}
	if err := a.media.ClearSaved(ctx, me.ID, media.ListRecent); err != nil {
		return nil, err
	}
	a.listChanged(me.ID, media.ListRecent, except(ctx))
	return true, nil
}

func (a *API) getFavedStickers(ctx context.Context, r *tg.MessagesGetFavedStickersRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	list, err := a.media.SavedDocs(ctx, me.ID, media.ListFaved)
	if err != nil {
		return nil, err
	}
	h := media.SavedHash(list)
	if r.Hash != 0 && r.Hash == h {
		return &tg.MessagesFavedStickersNotModified{}, nil
	}
	return &tg.MessagesFavedStickers{Hash: h, Packs: media.StickerPacks(list), Stickers: a.docs(list)}, nil
}

func (a *API) faveSticker(ctx context.Context, r *tg.MessagesFaveStickerRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	return true, a.saveDoc(ctx, me, media.ListFaved, r.ID, !r.Unfave, media.KindSticker)
}

// noteSent is what Telegram does when you send a sticker or a GIF: the
// sticker goes to the front of your recent ones, the GIF to your saved
// GIFs. Every session reloads, the sending one too: it doesn't add the
// item by itself.
func (a *API) noteSent(ctx context.Context, me account.User, ref media.Ref) {
	var list string
	switch ref.Kind {
	case media.KindSticker:
		list = media.ListRecent
		if err := a.media.NoteStickerUse(ctx, ref.ID); err != nil {
			slog.Warn("api: sticker use", "err", err)
		}
	case media.KindGIF:
		list = media.ListGIFs
	default:
		return
	}
	changed, err := a.media.Save(ctx, me.ID, list, ref.ID, true, 0)
	if err != nil {
		slog.Warn("api: note sent", "err", err)
		return
	}
	if changed {
		a.listChanged(me.ID, list, edge.Session{})
	}
}
