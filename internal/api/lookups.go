package api

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/channels"
	"github.com/Greengrame/greengram-server/internal/media"
	"github.com/Greengrame/greengram-server/internal/stars"
)

// Lookups the clients make at start and while browsing, for things this
// server has nothing for yet (stickers, stories, reactions, stars...). An
// error makes several of them retry in a loop, so each gets the empty
// answer Telegram gives an account that has none of it. Later phases
// replace these one by one.
func (a *API) registerLookups() {
	handle(a, tlprofile.SemanticMethodMessagesGetArchivedStickers, func(context.Context, *tg.MessagesGetArchivedStickersRequest) (any, error) {
		return &tg.MessagesArchivedStickers{Sets: []tg.StickerSetCoveredClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodMessagesGetStickers, func(context.Context, *tg.MessagesGetStickersRequest) (any, error) {
		return &tg.MessagesStickers{Stickers: []tg.DocumentClass{}}, nil
	})
	// nobody here posts as a channel
	handle(a, tlprofile.SemanticMethodChannelsGetSendAs, func(context.Context, *tg.ChannelsGetSendAsRequest) (any, error) {
		return &tg.ChannelsSendAsPeers{Peers: []tg.SendAsPeer{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}, nil
	})
	// Web A asks for these whenever the emoji picker opens and retries on an error
	handle(a, tlprofile.SemanticMethodMessagesGetEmojiGroups, func(context.Context, *tg.MessagesGetEmojiGroupsRequest) (any, error) {
		return &tg.MessagesEmojiGroups{Groups: []tg.EmojiGroupClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodMessagesGetFeaturedEmojiStickers, func(context.Context, *tg.MessagesGetFeaturedEmojiStickersRequest) (any, error) {
		return &tg.MessagesFeaturedStickers{Sets: []tg.StickerSetCoveredClass{}, Unread: []int64{}}, nil
	})
	handle(a, tlprofile.SemanticMethodMessagesGetEmojiKeywords, func(_ context.Context, r *tg.MessagesGetEmojiKeywordsRequest) (any, error) {
		return &tg.EmojiKeywordsDifference{LangCode: r.LangCode, Version: 1, Keywords: []tg.EmojiKeywordClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodMessagesGetEmojiKeywordsDifference, func(_ context.Context, r *tg.MessagesGetEmojiKeywordsDifferenceRequest) (any, error) {
		return &tg.EmojiKeywordsDifference{LangCode: r.LangCode, FromVersion: r.FromVersion, Version: r.FromVersion, Keywords: []tg.EmojiKeywordClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodMessagesGetEmojiStatusGroups, func(context.Context, *tg.MessagesGetEmojiStatusGroupsRequest) (any, error) {
		return &tg.MessagesEmojiGroups{Groups: []tg.EmojiGroupClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodAccountGetDefaultProfilePhotoEmojis, func(context.Context, *tg.AccountGetDefaultProfilePhotoEmojisRequest) (any, error) {
		return &tg.EmojiList{DocumentID: []int64{}}, nil
	})
	handle(a, tlprofile.SemanticMethodMessagesGetTopReactions, func(context.Context, *tg.MessagesGetTopReactionsRequest) (any, error) {
		return &tg.MessagesReactions{Reactions: []tg.ReactionClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodMessagesGetRecentReactions, func(context.Context, *tg.MessagesGetRecentReactionsRequest) (any, error) {
		return &tg.MessagesReactions{Reactions: []tg.ReactionClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodMessagesGetDefaultTagReactions, func(context.Context, *tg.MessagesGetDefaultTagReactionsRequest) (any, error) {
		return &tg.MessagesReactions{Reactions: []tg.ReactionClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodMessagesGetSavedReactionTags, func(context.Context, *tg.MessagesGetSavedReactionTagsRequest) (any, error) {
		return &tg.MessagesSavedReactionTags{Tags: []tg.SavedReactionTag{}}, nil
	})
	handle(a, tlprofile.SemanticMethodMessagesGetAvailableEffects, func(context.Context, *tg.MessagesGetAvailableEffectsRequest) (any, error) {
		return &tg.MessagesAvailableEffects{Effects: []tg.AvailableEffect{}, Documents: []tg.DocumentClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodStoriesGetAlbums, func(context.Context, *tg.StoriesGetAlbumsRequest) (any, error) {
		return &tg.StoriesAlbums{Albums: []tg.StoryAlbum{}}, nil
	})
	handle(a, tlprofile.SemanticMethodPaymentsGetStarGiftCollections, func(context.Context, *tg.PaymentsGetStarGiftCollectionsRequest) (any, error) {
		return &tg.PaymentsStarGiftCollections{Collections: []tg.StarGiftCollection{}}, nil
	})
	handle(a, tlprofile.SemanticMethodMessagesGetSearchResultsPositions, func(context.Context, *tg.MessagesGetSearchResultsPositionsRequest) (any, error) {
		return &tg.MessagesSearchResultsPositions{Positions: []tg.SearchResultPosition{}}, nil
	})
	handle(a, tlprofile.SemanticMethodMessagesGetQuickReplies, func(context.Context, *tg.MessagesGetQuickRepliesRequest) (any, error) {
		return &tg.MessagesQuickReplies{QuickReplies: []tg.QuickReply{}, Messages: []tg.MessageClass{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodMessagesReportReadMetrics, func(context.Context, *tg.MessagesReportReadMetricsRequest) (any, error) { return true, nil })
	handle(a, tlprofile.SemanticMethodMessagesGetAttachMenuBots, func(context.Context, *tg.MessagesGetAttachMenuBotsRequest) (any, error) {
		return &tg.AttachMenuBots{Bots: []tg.AttachMenuBot{}, Users: []tg.UserClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodChannelsGetChannelRecommendations, func(context.Context, *tg.ChannelsGetChannelRecommendationsRequest) (any, error) {
		return &tg.MessagesChats{Chats: []tg.ChatClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodBotsGetBotRecommendations, func(context.Context, *tg.BotsGetBotRecommendationsRequest) (any, error) {
		return &tg.UsersUsers{Users: []tg.UserClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodAccountGetReactionsNotifySettings, func(context.Context, *tg.AccountGetReactionsNotifySettingsRequest) (any, error) {
		return &tg.ReactionsNotifySettings{Sound: &tg.NotificationSoundDefault{}}, nil
	})
	handle(a, tlprofile.SemanticMethodAccountGetSavedRingtones, func(context.Context, *tg.AccountGetSavedRingtonesRequest) (any, error) {
		return &tg.AccountSavedRingtones{Ringtones: []tg.DocumentClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodAccountGetThemes, func(context.Context, *tg.AccountGetThemesRequest) (any, error) {
		return &tg.AccountThemes{Themes: []tg.Theme{}}, nil
	})
	handle(a, tlprofile.SemanticMethodAccountGetConnectedBots, func(context.Context, *tg.AccountGetConnectedBotsRequest) (any, error) {
		return &tg.AccountConnectedBots{ConnectedBots: []tg.ConnectedBot{}, Users: []tg.UserClass{}}, nil
	})
	handle(a, tlprofile.SemanticMethodAccountGetAutoDownloadSettings, func(context.Context, *tg.AccountGetAutoDownloadSettingsRequest) (any, error) {
		return &tg.AccountAutoDownloadSettings{Low: autoDownload(1 << 20), Medium: autoDownload(10 << 20), High: autoDownload(100 << 20)}, nil
	})
	handle(a, tlprofile.SemanticMethodAccountGetPassword, func(context.Context, *tg.AccountGetPasswordRequest) (any, error) {
		return &tg.AccountPassword{NewAlgo: &tg.PasswordKdfAlgoUnknown{}, NewSecureAlgo: &tg.SecurePasswordKdfAlgoUnknown{}, SecureRandom: randomBytes(256)}, nil
	})
	handle(a, tlprofile.SemanticMethodAccountGetWebBrowserSettings, func(context.Context, *tg.AccountGetWebBrowserSettingsRequest) (any, error) {
		return &tg.AccountWebBrowserSettingsNotModified{}, nil
	})
	handle(a, tlprofile.SemanticMethodAccountRegisterDevice, func(context.Context, *tg.AccountRegisterDeviceRequest) (any, error) { return true, nil })
	handle(a, tlprofile.SemanticMethodHelpSaveAppLog, func(context.Context, *tg.HelpSaveAppLogRequest) (any, error) { return true, nil })
	handle(a, tlprofile.SemanticMethodHelpGetPeerColors, func(_ context.Context, r *tg.HelpGetPeerColorsRequest) (any, error) {
		return peerColorsFor(peerColors, r.Hash), nil
	})
	handle(a, tlprofile.SemanticMethodHelpGetPeerProfileColors, func(_ context.Context, r *tg.HelpGetPeerProfileColorsRequest) (any, error) {
		return peerColorsFor(peerProfileColors, r.Hash), nil
	})
	handle(a, tlprofile.SemanticMethodHelpGetAppConfig, func(ctx context.Context, _ *tg.HelpGetAppConfigRequest) (any, error) {
		// no passkeys here: Web A defaults the settings entry to on
		v := []tg.JSONObjectValue{
			{Key: "settings_display_passkeys", Value: &tg.JSONBool{}},
			// Web A's default hides "Get boosts via gifts" until the server says it can sell them
			{Key: "giveaway_gifts_purchase_available", Value: &tg.JSONBool{Value: true}},
			{Key: "giveaway_boosts_per_premium", Value: &tg.JSONNumber{Value: 4}},
			// and the Stars prize in that sheet
			{Key: "stars_gifts_enabled", Value: &tg.JSONBool{Value: true}},
			{Key: "boosts_per_sent_gift", Value: &tg.JSONNumber{Value: 3}},
			// Android hides Stars everywhere, the settings row too, unless this
			// is an explicit false. Web A doesn't read it
			{Key: "stars_purchase_blocked", Value: &tg.JSONBool{}},
			// and the gift sheet's whole Stars gift catalog: the app's default is blocked
			{Key: "stargifts_blocked", Value: &tg.JSONBool{}},
			{Key: "stargifts_pinned_to_top_limit", Value: &tg.JSONNumber{Value: stars.PinLimit}},
		}
		// the apps turn a message that is just one of these into a roll
		var dice []tg.JSONValueClass
		for _, d := range media.DiceEmoji {
			dice = append(dice, &tg.JSONString{Value: d.Emoji})
		}
		v = append(v, tg.JSONObjectValue{Key: "emojies_send_dice", Value: &tg.JSONArray{Value: dice}})
		v = append(v,
			tg.JSONObjectValue{Key: "chat_read_mark_size_threshold", Value: &tg.JSONNumber{Value: channels.SeenByMaxMembers}},
			tg.JSONObjectValue{Key: "chat_read_mark_expire_period", Value: &tg.JSONNumber{Value: channels.SeenByExpire}},
			tg.JSONObjectValue{Key: "stars_paid_reaction_amount_max", Value: &tg.JSONNumber{Value: maxPaidReaction}},
			tg.JSONObjectValue{Key: "todo_items_max", Value: &tg.JSONNumber{Value: media.TodoItemsMax}},
			tg.JSONObjectValue{Key: "todo_title_length_max", Value: &tg.JSONNumber{Value: media.TodoTitleMax}},
			tg.JSONObjectValue{Key: "todo_item_length_max", Value: &tg.JSONNumber{Value: media.TodoItemMax}})
		v = append(v, storiesAppConfig()...)
		v = append(v, a.marketAppConfig(ctx)...)
		cfg := &tg.HelpAppConfig{Hash: 9}
		if f := a.freezeConfig(ctx); f != nil {
			cfg.Hash, v = 10, append(v, f...)
		}
		cfg.Config = &tg.JSONObject{Value: v}
		return cfg, nil
	})
	handle(a, tlprofile.SemanticMethodHelpGetTermsOfServiceUpdate, func(context.Context, *tg.HelpGetTermsOfServiceUpdateRequest) (any, error) {
		return &tg.HelpTermsOfServiceUpdateEmpty{Expires: int(time.Now().Add(24 * time.Hour).Unix())}, nil
	})
	handle(a, tlprofile.SemanticMethodHelpGetPromoData, func(context.Context, *tg.HelpGetPromoDataRequest) (any, error) {
		return &tg.HelpPromoDataEmpty{Expires: int(time.Now().Add(24 * time.Hour).Unix())}, nil
	})

	handle(a, tlprofile.SemanticMethodHelpGetInviteText, func(context.Context, *tg.HelpGetInviteTextRequest) (any, error) {
		return &tg.HelpInviteText{Message: "Join me on Greengram: " + a.in.BaseURL}, nil
	})
}

func emptyUpdates() *tg.Updates {
	return &tg.Updates{Updates: []tg.UpdateClass{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: int(time.Now().Unix())}
}

func autoDownload(size int64) tg.AutoDownloadSettings {
	return tg.AutoDownloadSettings{PhotoSizeMax: 1 << 20, VideoSizeMax: size, FileSizeMax: size, VideoUploadMaxbitrate: 100,
		SmallQueueActiveOperationsMax: 2, LargeQueueActiveOperationsMax: 1}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}
