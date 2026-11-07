package api

import (
	"context"
	"hash/crc32"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/media"
)

func (a *API) registerStickers() {
	handle(a, tlprofile.SemanticMethodMessagesGetStickerSet, a.getStickerSet)
	handle(a, tlprofile.SemanticMethodMessagesGetAllStickers, func(ctx context.Context, r *tg.MessagesGetAllStickersRequest) (any, error) {
		return a.installedSets(ctx, media.SetStickers, r.Hash)
	})
	handle(a, tlprofile.SemanticMethodMessagesGetEmojiStickers, func(ctx context.Context, r *tg.MessagesGetEmojiStickersRequest) (any, error) {
		return a.installedSets(ctx, media.SetEmoji, r.Hash)
	})
	handle(a, tlprofile.SemanticMethodMessagesInstallStickerSet, a.installStickerSet)
	handle(a, tlprofile.SemanticMethodMessagesUninstallStickerSet, a.uninstallStickerSet)
	handle(a, tlprofile.SemanticMethodMessagesGetCustomEmojiDocuments, a.customEmojiDocuments)
}

// emptySet is what a built-in set nobody seeded looks like. The apps
// retry STICKERSET_INVALID for their built-in sets (animated emoji, dice,
// premium gifts...) several times a second; an empty set they can cache
// ends that.
func emptySet(in tg.InputStickerSetClass) *tg.MessagesStickerSet {
	name := in.TypeName()
	if n, ok := in.(*tg.InputStickerSetShortName); ok {
		name = n.ShortName
	}
	id := accessHash(int64(len(name))<<32 ^ int64(crc32.ChecksumIEEE([]byte(name))))
	return &tg.MessagesStickerSet{
		Set:       tg.StickerSet{ID: id, AccessHash: id, Title: name, ShortName: name},
		Packs:     []tg.StickerPack{},
		Keywords:  []tg.StickerKeyword{},
		Documents: []tg.DocumentClass{},
	}
}

func (a *API) getStickerSet(ctx context.Context, r *tg.MessagesGetStickerSetRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	set, items, ok, err := a.media.StickerSet(ctx, me.ID, r.Stickerset)
	if err != nil {
		return nil, err
	}
	if _, statuses := r.Stickerset.(*tg.InputStickerSetEmojiDefaultStatuses); statuses && !ok {
		if set, items, err = a.media.DefaultStatusSet(ctx); err != nil {
			return nil, err
		}
		ok = len(items) > 0
	}
	if _, gifts := r.Stickerset.(*tg.InputStickerSetPremiumGifts); gifts && !ok {
		if set, items, err = a.media.PremiumGiftSet(ctx); err != nil {
			return nil, err
		}
		ok = len(items) > 0
	}
	if _, gifts := r.Stickerset.(*tg.InputStickerSetTonGifts); gifts && !ok {
		if set, items, err = a.media.TonGiftSet(ctx); err != nil {
			return nil, err
		}
		ok = len(items) > 0
	}
	if !ok {
		if _, byID := r.Stickerset.(*tg.InputStickerSetID); byID {
			return nil, media.ErrSetInvalid
		}
		return emptySet(r.Stickerset), nil
	}
	if r.Hash != 0 && r.Hash == set.Hash {
		return &tg.MessagesStickerSetNotModified{}, nil
	}
	return a.media.StickerSetTL(set, items), nil
}

func (a *API) installedSets(ctx context.Context, kind string, hash int64) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	sets, err := a.media.Installed(ctx, me.ID, kind)
	if err != nil {
		return nil, err
	}
	hashes := make([]int64, len(sets))
	out := make([]tg.StickerSet, len(sets))
	for i, s := range sets {
		hashes[i] = int64(s.Hash)
		out[i] = a.media.SetTL(s)
	}
	h := media.ListHash(hashes)
	if hash != 0 && hash == h {
		return &tg.MessagesAllStickersNotModified{}, nil
	}
	return &tg.MessagesAllStickers{Hash: h, Sets: out}, nil
}

func (a *API) installStickerSet(ctx context.Context, r *tg.MessagesInstallStickerSetRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	set, _, ok, err := a.media.StickerSet(ctx, me.ID, r.Stickerset)
	if err != nil {
		return nil, err
	}
	if !ok || set.Builtin != "" {
		return nil, media.ErrSetInvalid
	}
	if err := a.media.Install(ctx, me.ID, set.ID, true); err != nil {
		return nil, err
	}
	return &tg.MessagesStickerSetInstallResultSuccess{}, nil
}

func (a *API) uninstallStickerSet(ctx context.Context, r *tg.MessagesUninstallStickerSetRequest) (any, error) {
	me, err := a.me(ctx)
	if err != nil {
		return nil, err
	}
	set, _, ok, err := a.media.StickerSet(ctx, me.ID, r.Stickerset)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, media.ErrSetInvalid
	}
	return true, a.media.Install(ctx, me.ID, set.ID, false)
}

func (a *API) customEmojiDocuments(ctx context.Context, r *tg.MessagesGetCustomEmojiDocumentsRequest) (any, error) {
	if _, err := a.me(ctx); err != nil {
		return nil, err
	}
	docs, err := a.media.CustomEmoji(ctx, r.DocumentID)
	if err != nil {
		return nil, err
	}
	out := make([]tg.DocumentClass, 0, len(r.DocumentID))
	found := make(map[int64]bool, len(docs))
	for _, d := range docs {
		out = append(out, a.media.DocumentTL(d))
		found[d.ID] = true
	}
	// an id we don't have comes back as documentEmpty: the Android app
	// asks again at once, forever, for every id missing from the answer
	for _, id := range r.DocumentID {
		if !found[id] {
			out = append(out, &tg.DocumentEmpty{ID: id})
			found[id] = true
		}
	}
	return out, nil
}
