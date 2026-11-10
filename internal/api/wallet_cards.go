package api

import (
	"context"
	"strconv"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/wallet"
)

func (a *API) setWalletCardStickers(ctx context.Context, r *tg.WalletSetCardStickersRequest) (any, error) {
	var me account.User
	var service = a.topup.Load()
	var stickers = []wallet.InputLocalCardSticker{}
	var sticker tg.InputWalletCardSticker
	var err error
	var result tg.WalletCard
	me, err = a.me(ctx)
	if err != nil {
		return nil, err
	}
	if service == nil {
		return nil, tgerr.New(503, "WALLET_UNAVAILABLE")
	}
	for _, sticker = range r.Stickers {
		stickers = append(stickers, wallet.InputLocalCardSticker{Address: sticker.NftAddress, Position: wallet.AreaPosition{X: sticker.Coordinates.X, Y: sticker.Coordinates.Y, Width: sticker.Coordinates.W, Height: sticker.Coordinates.H, Rotation: sticker.Coordinates.Rotation, Radius: sticker.Coordinates.Radius}})
	}
	_, err = service.SetLocalCardStickers(ctx, me.ID, stickers, r.BgNftAddress)
	if err != nil {
		return nil, err
	}
	a.pushUserChanged(me.ID)
	result = a.localWalletCard(ctx, me.ID)
	return &result, nil
}

func (a *API) getWalletNfts(ctx context.Context, r *tg.WalletGetNftsRequest) (any, error) {
	var me account.User
	var service = a.topup.Load()
	var items []wallet.LocalNft
	var result = tg.WalletNftItems{Items: []tg.WalletNftItem{}}
	var start, end, limit int
	var i int
	var err error
	me, err = a.me(ctx)
	if err != nil {
		return nil, err
	}
	if service == nil {
		return nil, tgerr.New(503, "WALLET_UNAVAILABLE")
	}
	if r.Offset != "" {
		start, err = strconv.Atoi(r.Offset)
		if err != nil || start < 0 {
			return nil, tgerr.New(400, "OFFSET_INVALID")
		}
	}
	items, err = service.LocalNfts(ctx, me.ID)
	if err != nil {
		return nil, err
	}
	limit = r.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	start = min(start, len(items))
	end = min(start+limit, len(items))
	for i = start; i < end; i++ {
		result.Items = append(result.Items, localNftTL(items[i]))
	}
	if end < len(items) {
		result.NextOffset = strconv.Itoa(end)
	}
	return &result, nil
}

func localNftTL(item wallet.LocalNft) tg.WalletNftItem {
	var result = tg.WalletNftItem{Address: item.Address, CollectionAddress: item.Collection, OwnerAddress: item.Owner, Index: item.Index, Name: item.Name, Description: item.Description}
	var attribute wallet.LocalNftAttribute
	for _, attribute = range item.Attributes {
		result.Attributes = append(result.Attributes, tg.WalletNftAttribute{TraitType: attribute.TraitType, Value: attribute.Value})
	}
	return result
}
