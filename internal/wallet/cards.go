package wallet

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/Greengrame/greengram-server/internal/stars"
)

type AreaPosition struct {
	X        float64 `json:"x_percentage"`
	Y        float64 `json:"y_percentage"`
	Width    float64 `json:"width_percentage"`
	Height   float64 `json:"height_percentage"`
	Rotation float64 `json:"rotation_angle"`
	Radius   float64 `json:"corner_radius_percentage"`
}

type LocalNftAttribute struct {
	TraitType string `json:"trait_type"`
	Value     string `json:"value"`
}

type LocalNft struct {
	Address     string              `json:"address"`
	Collection  string              `json:"collection_address,omitempty"`
	Index       string              `json:"index"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Attributes  []LocalNftAttribute `json:"attributes,omitempty"`
	Gift        any                 `json:"gift,omitempty"`
	Owner       string              `json:"-"`
}

type LocalCardSticker struct {
	Position AreaPosition `json:"position"`
	Nft      LocalNft     `json:"nft"`
}

type InputLocalCardSticker struct {
	Position AreaPosition `json:"position"`
	Address  string       `json:"nft_address"`
}

func localNft(gift stars.Unique, owner string) LocalNft {
	return LocalNft{Address: "gram:nft:" + gift.Slug, Collection: "gram:collection:" + strconv.FormatInt(gift.SetID, 10), Index: strconv.Itoa(gift.Num), Name: gift.Title + " #" + strconv.Itoa(gift.Num), Description: "Greengram collectible " + gift.Slug, Owner: owner, Attributes: []LocalNftAttribute{{TraitType: "Model", Value: gift.Model.Name}, {TraitType: "Symbol", Value: gift.Pattern.Name}, {TraitType: "Backdrop", Value: gift.Backdrop.Name}}}
}

func (s *Service) LocalNfts(ctx context.Context, user int64) ([]LocalNft, error) {
	var result = []LocalNft{}
	var gifts []stars.Unique
	var gift stars.Unique
	var card LocalWalletCard
	var err error
	card, err = s.LocalCard(ctx, user, false)
	if err != nil {
		return nil, err
	}
	gifts, err = stars.New(s.db, s.settings).UniquesHeldBy(ctx, user)
	if err != nil {
		return nil, err
	}
	for _, gift = range gifts {
		result = append(result, localNft(gift, card.Address))
	}
	return result, nil
}

func (s *Service) fillCardStickers(ctx context.Context, user int64, card *LocalWalletCard) error {
	var raw []byte
	var stickers []InputLocalCardSticker
	var sticker InputLocalCardSticker
	var gift stars.Unique
	var err error
	var service = stars.New(s.db, s.settings)
	var background string
	err = s.db.QueryRow(ctx, "SELECT stickers,background FROM local_wallet_cards WHERE user_id=$1", user).Scan(&raw, &background)
	if err != nil {
		return err
	}
	err = json.Unmarshal(raw, &stickers)
	if err != nil {
		return err
	}
	for _, sticker = range stickers {
		gift, err = service.UniqueBySlug(ctx, strings.TrimPrefix(sticker.Address, "gram:nft:"))
		if err != nil || gift.Holder.User != user || gift.Holder.Channel != 0 {
			continue
		}
		card.Stickers = append(card.Stickers, LocalCardSticker{Position: sticker.Position, Nft: localNft(gift, card.Address)})
		if sticker.Address == background {
			card.Background = background
		}
	}
	return nil
}

func (s *Service) SetLocalCardStickers(ctx context.Context, user int64, stickers []InputLocalCardSticker, background string) (LocalWalletCard, error) {
	var card LocalWalletCard
	var sticker InputLocalCardSticker
	var gift stars.Unique
	var raw []byte
	var err error
	var found = background == ""
	var service = stars.New(s.db, s.settings)
	var value float64
	if len(stickers) > 10 {
		return card, fail(400, "Use at most 10 wallet card stickers.")
	}
	for _, sticker = range stickers {
		for _, value = range []float64{sticker.Position.X, sticker.Position.Y, sticker.Position.Width, sticker.Position.Height, sticker.Position.Rotation, sticker.Position.Radius} {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return card, fail(400, "Invalid sticker position.")
			}
		}
		if sticker.Position.X < 0 || sticker.Position.X > 100 || sticker.Position.Y < 0 || sticker.Position.Y > 100 || sticker.Position.Width <= 0 || sticker.Position.Width > 100 || sticker.Position.Height <= 0 || sticker.Position.Height > 100 || sticker.Position.Rotation < 0 || sticker.Position.Rotation > 360 || sticker.Position.Radius < 0 || sticker.Position.Radius > 50 {
			return card, fail(400, "Invalid sticker position.")
		}
		if !strings.HasPrefix(sticker.Address, "gram:nft:") {
			return card, fail(400, "Use a local collectible address.")
		}
		gift, err = service.UniqueBySlug(ctx, strings.TrimPrefix(sticker.Address, "gram:nft:"))
		if err != nil {
			return card, err
		}
		if gift.Holder.User != user || gift.Holder.Channel != 0 {
			return card, fail(403, "Collectible belongs to another account.")
		}
		if sticker.Address == background {
			found = true
		}
	}
	if !found {
		return card, fail(400, "Background must be one of the card stickers.")
	}
	card, err = s.LocalCard(ctx, user, false)
	if err != nil {
		return card, err
	}
	if stickers == nil {
		stickers = []InputLocalCardSticker{}
	}
	raw, err = json.Marshal(stickers)
	if err != nil {
		return card, err
	}
	_, err = s.db.Exec(ctx, "UPDATE local_wallet_cards SET stickers=$2,background=$3,public=true WHERE user_id=$1", user, raw, background)
	if err != nil {
		return card, err
	}
	return s.LocalCard(ctx, user, false)
}
