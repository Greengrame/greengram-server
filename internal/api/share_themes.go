package api

import (
	"context"
	"math/big"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/stars"
	"github.com/Greengrame/greengram-server/internal/wallet"
)

func (a *API) registerBotExtensions() {
	handle(a, tlprofile.SemanticMethodBotsCreateBot, a.createManagedBot)
	handle(a, tlprofile.SemanticMethodMessagesSendBotRequestedPeer, a.shareManagedBot)
	handle(a, tlprofile.SemanticMethodWalletSetCardStickers, a.setWalletCardStickers)
	handle(a, tlprofile.SemanticMethodWalletGetNfts, a.getWalletNfts)
	handle(a, tlprofile.SemanticMethodMessagesSetShareTheme, a.setShareTheme)
	handle(a, tlprofile.SemanticMethodAccountUpdateConnectedBot, a.updateConnectedBot)
	handle(a, tlprofile.SemanticMethodMessagesGetShareThemeBackdrops, a.getShareThemeBackdrops)
}

func (a *API) peerShareTheme(ctx context.Context, peer int64, channel bool) tg.ShareThemeClass {
	var theme tg.ShareThemeClass
	var err error
	if a.bots == nil {
		return nil
	}
	theme, err = a.bots.ShareTheme(ctx, peer, channel)
	if err != nil {
		return nil
	}
	return theme
}

func (a *API) getShareThemeBackdrops(ctx context.Context, r *tg.MessagesGetShareThemeBackdropsRequest) (any, error) {
	var err error
	var backdrops []tg.ShareThemeBackdropOption
	_, err = a.me(ctx)
	if err != nil {
		return nil, err
	}
	backdrops, err = a.stars.ShareThemeBackdrops(ctx)
	if err != nil {
		return nil, err
	}
	return &tg.ShareThemeBackdrops{Backdrops: backdrops}, nil
}

func (a *API) setShareTheme(ctx context.Context, r *tg.MessagesSetShareThemeRequest) (any, error) {
	var me account.User
	var target peer
	var member channels.Membership
	var theme = r.Theme
	var emoji *tg.ShareThemeEmojiBackdrop
	var gift *tg.InputShareThemeGift
	var reaction tg.ReactionClass
	var backdrops []tg.ShareThemeBackdropOption
	var backdrop tg.ShareThemeBackdropOption
	var receipt stars.Receipt
	var docs map[int64]tg.DocumentClass
	var found bool
	var err error
	me, err = a.me(ctx)
	if err != nil {
		return nil, err
	}
	target, err = a.peer(ctx, me, r.Peer)
	if err != nil {
		return nil, err
	}
	if target.channel == 0 && target.user != me.ID {
		return nil, tgerr.New(403, "USER_ID_INVALID")
	}
	if target.channel != 0 {
		_, member, err = a.chans.Access(ctx, target.channel, me.ID)
		if err != nil {
			return nil, err
		}
		if member.Status != channels.Creator && !(member.Status == channels.Admin && member.AdminRights.ChangeInfo) {
			return nil, tgerr.New(403, "CHAT_ADMIN_REQUIRED")
		}
	}
	switch theme.(type) {
	case *tg.ShareThemeDefault:
	case *tg.ShareThemeEmojiBackdrop:
		emoji = theme.(*tg.ShareThemeEmojiBackdrop)
		if len(emoji.Emojis) > 6 {
			return nil, tgerr.New(400, "REACTION_INVALID")
		}
		for _, reaction = range emoji.Emojis {
			switch reaction.(type) {
			case *tg.ReactionEmoji:
				if utf8.RuneCountInString(reaction.(*tg.ReactionEmoji).Emoticon) > 32 || reaction.(*tg.ReactionEmoji).Emoticon == "" {
					return nil, tgerr.New(400, "REACTION_INVALID")
				}
			case *tg.ReactionCustomEmoji:
				found, err = a.media.IsStatusEmoji(ctx, reaction.(*tg.ReactionCustomEmoji).DocumentID)
				if err != nil {
					return nil, err
				}
				if !found {
					return nil, tgerr.New(400, "DOCUMENT_INVALID")
				}
			default:
				return nil, tgerr.New(400, "REACTION_INVALID")
			}
		}
		if emoji.BackdropID != 0 {
			backdrops, err = a.stars.ShareThemeBackdrops(ctx)
			if err != nil {
				return nil, err
			}
			found = false
			for _, backdrop = range backdrops {
				if backdrop.BackdropID == emoji.BackdropID {
					found = true
				}
			}
			if !found {
				return nil, tgerr.New(400, "BACKDROP_INVALID")
			}
		}
	case *tg.InputShareThemeGift:
		gift = theme.(*tg.InputShareThemeGift)
		receipt, err = a.ownedGift(ctx, me, gift.Stargift)
		if err != nil {
			return nil, err
		}
		if receipt.Unique == nil {
			return nil, tgerr.New(400, "STARGIFT_NOT_UNIQUE")
		}
		if target.channel != 0 && receipt.OwnerChannel != target.channel || target.channel == 0 && receipt.Owner != me.ID {
			return nil, tgerr.New(400, "STARGIFT_NOT_FOUND")
		}
		docs, err = a.docsFor(ctx, uniqueDocIDs(receipt.Unique))
		if err != nil {
			return nil, err
		}
		theme = &tg.ShareThemeGift{Stargift: uniqueTL(*receipt.Unique, docs)}
	default:
		return nil, tgerr.New(400, "SHARE_THEME_INVALID")
	}
	err = a.bots.SetShareTheme(ctx, max(target.user, target.channel), target.channel != 0, theme)
	if err != nil {
		return nil, err
	}
	if target.channel == 0 {
		a.pushUserChanged(me.ID)
	}
	return true, nil
}

func (a *API) peerVerification(ctx context.Context, peer int64, channel bool) tg.BotVerification {
	if a.bots == nil {
		return tg.BotVerification{}
	}
	return a.bots.PeerVerification(ctx, peer, channel)
}

func (a *API) localWalletCard(ctx context.Context, user int64) tg.WalletCard {
	var service = a.topup.Load()
	var card *wallet.LocalWalletCard
	var amount *big.Int
	var result tg.WalletCard
	var sticker wallet.LocalCardSticker
	var native tg.WalletCardSticker
	var ok bool
	var err error
	if service == nil {
		return tg.WalletCard{}
	}
	card, err = service.PublicLocalCard(ctx, user)
	if err != nil || card == nil {
		return tg.WalletCard{}
	}
	amount, ok = new(big.Int).SetString(card.Amount, 10)
	if !ok || !amount.IsInt64() {
		return tg.WalletCard{}
	}
	result = tg.WalletCard{Address: card.Address, Balance: amount.Int64(), Stickers: []tg.WalletCardSticker{}}
	for _, sticker = range card.Stickers {
		native = tg.WalletCardSticker{Coordinates: tg.MediaAreaCoordinates{X: sticker.Position.X, Y: sticker.Position.Y, W: sticker.Position.Width, H: sticker.Position.Height, Rotation: sticker.Position.Rotation, Radius: sticker.Position.Radius}, Nft: localNftTL(sticker.Nft)}
		result.Stickers = append(result.Stickers, native)
		if sticker.Nft.Address == card.Background {
			result.SetBgNftItem(native.Nft)
		}
	}
	return result
}
