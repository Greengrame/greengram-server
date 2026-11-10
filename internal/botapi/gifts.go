package botapi

import (
	"errors"
	"strconv"
	"strings"

	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/stars"
)

func (s *Server) getUniqueGift(c *call) (any, error) {
	var gift stars.Unique
	var documents []media.Document
	var err error
	var stickers = map[int64]map[string]any{}
	var document media.Document
	var sticker map[string]any
	var v = s.view(c.ctx, c.bot)
	var name = strings.TrimSpace(c.p.str("name"))
	if name == "" {
		return nil, bad("gift name is empty")
	}
	if s.Stars == nil || s.Media == nil {
		return nil, bad("gift service is unavailable")
	}
	gift, err = s.Stars.UniqueBySlug(c.ctx, name)
	if errors.Is(err, stars.ErrUniqueNotFound) {
		return nil, bad("unique gift not found")
	}
	if err != nil {
		return nil, err
	}
	documents, err = s.Media.Documents(c.ctx, []int64{gift.Model.Document, gift.Pattern.Document})
	if err != nil {
		return nil, err
	}
	for _, document = range documents {
		_, sticker = v.document(s.Media.DocumentTL(document))
		sticker["type"] = "regular"
		sticker["is_animated"], sticker["is_video"] = document.Mime == "application/x-tgsticker", document.Mime == "video/webm"
		stickers[document.ID] = sticker
	}
	if stickers[gift.Model.Document] == nil || stickers[gift.Pattern.Document] == nil {
		return nil, bad("gift artwork is unavailable")
	}
	return map[string]any{
		"gift_id": strconv.FormatInt(gift.GiftID, 10), "base_name": gift.Title, "name": gift.Slug, "number": gift.Num,
		"model":  map[string]any{"name": gift.Model.Name, "sticker": stickers[gift.Model.Document], "rarity_per_mille": gift.Model.Permille},
		"symbol": map[string]any{"name": gift.Pattern.Name, "sticker": stickers[gift.Pattern.Document], "rarity_per_mille": gift.Pattern.Permille},
		"backdrop": map[string]any{"name": gift.Backdrop.Name, "rarity_per_mille": gift.Backdrop.Permille,
			"colors": map[string]any{"center_color": gift.Backdrop.Center, "edge_color": gift.Backdrop.Edge, "symbol_color": gift.Backdrop.Pattern, "text_color": gift.Backdrop.Text}},
		"is_premium": gift.PremiumOnly,
	}, nil
}
