package botapi

import (
	"strconv"

	"github.com/Greengrame/greengram-server/internal/wallet"
	"github.com/iamxvbaba/td/tg"
)

func (s *Server) getChatShareThemeBackdrops(c *call) (any, error) {
	var backdrops []tg.ShareThemeBackdropOption
	var backdrop tg.ShareThemeBackdropOption
	var out = []map[string]any{}
	var err error
	if s.Stars == nil {
		return nil, bad("gift service is unavailable")
	}
	backdrops, err = s.Stars.ShareThemeBackdrops(c.ctx)
	if err != nil {
		return nil, err
	}
	for _, backdrop = range backdrops {
		out = append(out, map[string]any{"id": backdrop.BackdropID, "name": backdrop.Name, "colors": map[string]any{"center_color": backdrop.CenterColor, "edge_color": backdrop.EdgeColor, "symbol_color": backdrop.PatternColor, "text_color": backdrop.TextColor}})
	}
	return map[string]any{"backdrops": out}, nil
}

func (s *Server) chatExtensions(c *call, to chat, out map[string]any) (any, error) {
	var card *wallet.LocalWalletCard
	var theme tg.ShareThemeClass
	var emoji *tg.ShareThemeEmojiBackdrop
	var gift *tg.ShareThemeGift
	var unique *tg.StarGiftUnique
	var reaction tg.ReactionClass
	var emojis = []map[string]any{}
	var value map[string]any
	var rendered any
	var err error
	if to.channel == 0 && s.Wallet != nil {
		card, err = s.Wallet.PublicLocalCard(c.ctx, to.user)
		if err != nil {
			return nil, err
		}
		if card != nil {
			out["ton_wallet_card"] = card
		}
	}
	theme, err = s.Bots.ShareTheme(c.ctx, max(to.user, to.channel), to.channel != 0)
	if err != nil {
		return nil, err
	}
	switch theme.(type) {
	case *tg.ShareThemeEmojiBackdrop:
		emoji = theme.(*tg.ShareThemeEmojiBackdrop)
		for _, reaction = range emoji.Emojis {
			switch reaction.(type) {
			case *tg.ReactionEmoji:
				emojis = append(emojis, map[string]any{"type": "emoji", "emoji": reaction.(*tg.ReactionEmoji).Emoticon})
			case *tg.ReactionCustomEmoji:
				emojis = append(emojis, map[string]any{"type": "custom_emoji", "custom_emoji_id": strconv.FormatInt(reaction.(*tg.ReactionCustomEmoji).DocumentID, 10)})
			}
		}
		value = map[string]any{"type": "emoji", "emojis": emojis}
		if emoji.BackdropID != 0 {
			value["background_backdrop_id"] = emoji.BackdropID
		}
		out["share_theme"] = value
	case *tg.ShareThemeGift:
		gift = theme.(*tg.ShareThemeGift)
		unique, _ = gift.Stargift.(*tg.StarGiftUnique)
		if unique == nil {
			return nil, bad("invalid share theme gift")
		}
		rendered, err = s.getUniqueGift(&call{ctx: c.ctx, bot: c.bot, p: params{"name": unique.Slug}})
		if err != nil {
			return nil, err
		}
		out["share_theme"] = map[string]any{"type": "unique_gift", "gift": rendered}
	}
	return out, nil
}
