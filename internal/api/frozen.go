package api

import (
	"context"
	"log/slog"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"

	"github.com/Greengrame/greengram-server/internal/account"
	"github.com/Greengrame/greengram-server/internal/bots"
	"github.com/Greengrame/greengram-server/internal/builtin"
	"github.com/Greengrame/greengram-server/internal/edge"
	"github.com/Greengrame/greengram-server/internal/messages"
)

// What Telegram answers a frozen account; the apps show their "account
// frozen" sheet on it.
var errFrozen = tgerr.New(420, "FROZEN_METHOD_INVALID")

// frozenOK is what a frozen account may still call: reading, signing in
// and out, keeping its sessions alive. Anything not listed is refused, so
// a new method stays closed until someone decides it is a read.
var frozenOK = map[tlprofile.SemanticID]bool{}

func init() {
	for _, m := range []tlprofile.SemanticID{
		tlprofile.SemanticMethodAccountGetAccountTTL,
		tlprofile.SemanticMethodAccountGetAuthorizations,
		tlprofile.SemanticMethodAccountGetAutoDownloadSettings,
		tlprofile.SemanticMethodAccountGetConnectedBots,
		tlprofile.SemanticMethodAccountGetContactSignUpNotification,
		tlprofile.SemanticMethodAccountGetDefaultEmojiStatuses,
		tlprofile.SemanticMethodAccountGetDefaultProfilePhotoEmojis,
		tlprofile.SemanticMethodAccountGetGlobalPrivacySettings,
		tlprofile.SemanticMethodAccountGetNotifyExceptions,
		tlprofile.SemanticMethodAccountGetNotifySettings,
		tlprofile.SemanticMethodAccountGetPassword,
		tlprofile.SemanticMethodAccountGetPrivacy,
		tlprofile.SemanticMethodAccountGetReactionsNotifySettings,
		tlprofile.SemanticMethodAccountGetRecentEmojiStatuses,
		tlprofile.SemanticMethodAccountGetSavedRingtones,
		tlprofile.SemanticMethodAccountGetThemes,
		tlprofile.SemanticMethodAccountGetWebBrowserSettings,
		tlprofile.SemanticMethodAccountRegisterDevice,
		tlprofile.SemanticMethodAccountResetAuthorization,
		tlprofile.SemanticMethodAccountUpdateStatus,

		tlprofile.SemanticMethodAuthExportLoginToken,
		tlprofile.SemanticMethodAuthImportAuthorization,
		tlprofile.SemanticMethodAuthLogOut,
		tlprofile.SemanticMethodAuthResetAuthorizations,
		tlprofile.SemanticMethodAuthSendCode,

		tlprofile.SemanticMethodBotsGetBotCommands,
		tlprofile.SemanticMethodBotsGetBotInfo,
		tlprofile.SemanticMethodBotsGetBotMenuButton,
		tlprofile.SemanticMethodBotsGetBotRecommendations,

		tlprofile.SemanticMethodChannelsCheckUsername,
		tlprofile.SemanticMethodChannelsGetAdminedPublicChannels,
		tlprofile.SemanticMethodChannelsGetChannelRecommendations,
		tlprofile.SemanticMethodChannelsGetChannels,
		tlprofile.SemanticMethodChannelsGetFullChannel,
		tlprofile.SemanticMethodChannelsGetGroupsForDiscussion,
		tlprofile.SemanticMethodChannelsGetMessages,
		tlprofile.SemanticMethodChannelsGetParticipant,
		tlprofile.SemanticMethodChannelsGetParticipants,
		tlprofile.SemanticMethodChannelsReadHistory,

		tlprofile.SemanticMethodContactsGetBirthdays,
		tlprofile.SemanticMethodContactsGetBlocked,
		tlprofile.SemanticMethodContactsGetContacts,
		tlprofile.SemanticMethodContactsGetStatuses,
		tlprofile.SemanticMethodContactsGetTopPeers,
		tlprofile.SemanticMethodContactsResolveUsername,
		tlprofile.SemanticMethodContactsSearch,

		tlprofile.SemanticMethodFragmentGetCollectibleInfo,

		tlprofile.SemanticMethodHelpGetAppConfig,
		tlprofile.SemanticMethodHelpGetCDNConfig,
		tlprofile.SemanticMethodHelpGetCountriesList,
		tlprofile.SemanticMethodHelpGetInviteText,
		tlprofile.SemanticMethodHelpGetPeerColors,
		tlprofile.SemanticMethodHelpGetPeerProfileColors,
		tlprofile.SemanticMethodHelpGetPremiumPromo,
		tlprofile.SemanticMethodHelpGetPromoData,
		tlprofile.SemanticMethodHelpGetTermsOfServiceUpdate,
		tlprofile.SemanticMethodHelpSaveAppLog,

		tlprofile.SemanticMethodMessagesCheckChatInvite,
		tlprofile.SemanticMethodMessagesGetAllDrafts,
		tlprofile.SemanticMethodMessagesGetAllStickers,
		tlprofile.SemanticMethodMessagesGetArchivedStickers,
		tlprofile.SemanticMethodMessagesGetAttachMenuBots,
		tlprofile.SemanticMethodMessagesGetAvailableEffects,
		tlprofile.SemanticMethodMessagesGetAvailableReactions,
		tlprofile.SemanticMethodMessagesGetBotApp,
		tlprofile.SemanticMethodMessagesGetCustomEmojiDocuments,
		tlprofile.SemanticMethodMessagesGetDefaultTagReactions,
		tlprofile.SemanticMethodMessagesGetDialogFilters,
		tlprofile.SemanticMethodMessagesGetDialogs,
		tlprofile.SemanticMethodMessagesGetDiscussionMessage,
		tlprofile.SemanticMethodMessagesGetEmojiStatusGroups,
		tlprofile.SemanticMethodMessagesGetEmojiStickers,
		tlprofile.SemanticMethodMessagesGetExportedChatInvites,
		tlprofile.SemanticMethodMessagesGetFavedStickers,
		tlprofile.SemanticMethodMessagesGetForumTopics,
		tlprofile.SemanticMethodMessagesGetForumTopicsByID,
		tlprofile.SemanticMethodMessagesGetHistory,
		tlprofile.SemanticMethodMessagesGetMessageReactionsList,
		tlprofile.SemanticMethodMessagesGetUnreadMentions,
		tlprofile.SemanticMethodMessagesGetUnreadReactions,
		tlprofile.SemanticMethodMessagesGetWebPage,
		tlprofile.SemanticMethodMessagesGetWebPagePreview,
		tlprofile.SemanticMethodMessagesGetMessages,
		tlprofile.SemanticMethodMessagesGetMessagesReactions,
		tlprofile.SemanticMethodMessagesGetPeerDialogs,
		tlprofile.SemanticMethodMessagesGetPeerSettings,
		tlprofile.SemanticMethodMessagesGetPinnedDialogs,
		tlprofile.SemanticMethodMessagesGetPollResults,
		tlprofile.SemanticMethodMessagesGetPollVotes,
		tlprofile.SemanticMethodMessagesGetQuickReplies,
		tlprofile.SemanticMethodMessagesGetRecentReactions,
		tlprofile.SemanticMethodMessagesGetRecentStickers,
		tlprofile.SemanticMethodMessagesGetReplies,
		tlprofile.SemanticMethodMessagesGetSavedDialogs,
		tlprofile.SemanticMethodMessagesGetSavedGifs,
		tlprofile.SemanticMethodMessagesGetSavedHistory,
		tlprofile.SemanticMethodMessagesGetSavedReactionTags,
		tlprofile.SemanticMethodMessagesGetScheduledHistory,
		tlprofile.SemanticMethodMessagesGetScheduledMessages,
		tlprofile.SemanticMethodMessagesGetSearchCounters,
		tlprofile.SemanticMethodMessagesGetSearchResultsPositions,
		tlprofile.SemanticMethodMessagesGetStickerSet,
		tlprofile.SemanticMethodMessagesGetStickers,
		tlprofile.SemanticMethodMessagesGetSuggestedDialogFilters,
		tlprofile.SemanticMethodMessagesGetTopReactions,
		tlprofile.SemanticMethodMessagesReadDiscussion,
		tlprofile.SemanticMethodMessagesGetUnreadPollVotes,
		tlprofile.SemanticMethodMessagesReadHistory,
		tlprofile.SemanticMethodMessagesReportReadMetrics,
		tlprofile.SemanticMethodMessagesSearch,
		tlprofile.SemanticMethodMessagesSearchGlobal,

		tlprofile.SemanticMethodPaymentsCheckCanSendGift,
		tlprofile.SemanticMethodPaymentsCheckGiftCode,
		tlprofile.SemanticMethodPaymentsGetGiveawayInfo,
		tlprofile.SemanticMethodPaymentsGetPremiumGiftCodeOptions,
		tlprofile.SemanticMethodPaymentsGetSavedStarGift,
		tlprofile.SemanticMethodPaymentsGetSavedStarGifts,
		tlprofile.SemanticMethodPaymentsGetStarGiftCollections,
		tlprofile.SemanticMethodPaymentsGetStarGifts,
		tlprofile.SemanticMethodPaymentsGetStarGiftUpgradeAttributes,
		tlprofile.SemanticMethodPaymentsGetStarGiftUpgradePreview,
		tlprofile.SemanticMethodPaymentsGetUniqueStarGift,
		tlprofile.SemanticMethodPaymentsGetStarsStatus,
		tlprofile.SemanticMethodPaymentsGetStarsTopupOptions,
		tlprofile.SemanticMethodPaymentsGetStarsTransactions,

		tlprofile.SemanticMethodPhotosGetUserPhotos,

		tlprofile.SemanticMethodPremiumGetBoostsList,
		tlprofile.SemanticMethodPremiumGetBoostsStatus,
		tlprofile.SemanticMethodPremiumGetMyBoosts,

		tlprofile.SemanticMethodStoriesGetAlbums,
		tlprofile.SemanticMethodStoriesGetAllReadPeerStories,
		tlprofile.SemanticMethodStoriesGetAllStories,
		tlprofile.SemanticMethodStoriesGetPeerMaxIDs,
		tlprofile.SemanticMethodStoriesGetPinnedStories,
		tlprofile.SemanticMethodStoriesGetPeerStories,
		tlprofile.SemanticMethodStoriesGetStoriesByID,
		tlprofile.SemanticMethodStoriesGetStoriesArchive,
		tlprofile.SemanticMethodStoriesGetStoriesViews,
		tlprofile.SemanticMethodStoriesGetStoryViewsList,

		tlprofile.SemanticMethodUpdatesGetChannelDifference,
		tlprofile.SemanticMethodUpdatesGetDifference,
		tlprofile.SemanticMethodUpdatesGetState,

		tlprofile.SemanticMethodUploadGetFile,
		tlprofile.SemanticMethodUploadGetFileHashes,

		tlprofile.SemanticMethodUsersGetFullUser,
		tlprofile.SemanticMethodUsersGetUsers,
	} {
		frozenOK[m] = true
	}
}

type meKey struct{}

type signedIn struct {
	u     account.User
	hash  string
	epoch uint64 // read before u, see hub.join
}

// unfrozen refuses a frozen caller, unless it's talking to @SpamBot. The
// account it looked up rides along in ctx so the handler's me() doesn't ask
// again.
func (a *API) unfrozen(ctx context.Context, o bin.Object) (context.Context, error) {
	c := edge.CallFrom(ctx)
	if c == nil {
		return ctx, nil
	}
	epoch := c.Session().Epoch()
	u, hash, ok, err := a.accounts.KeyUser(ctx, c.PermKeyID)
	if err != nil {
		slog.Error("api: key user", "err", err)
		return ctx, tgerr.New(500, "INTERNAL")
	}
	if !ok {
		return ctx, nil
	}
	if u.Frozen() && !a.toSpamBot(ctx, u, o) {
		return ctx, errFrozen
	}
	return context.WithValue(ctx, meKey{}, signedIn{u, hash, epoch}), nil
}

// toSpamBot is whether a frozen account's write is one it may still make:
// the few it takes to chat with @SpamBot. That's a check on the peer, not an
// entry in frozenOK, which would open these methods to every chat.
func (a *API) toSpamBot(ctx context.Context, me account.User, o bin.Object) bool {
	if a.bots == nil {
		return false
	}
	var id int64
	var err error
	switch r := o.(type) {
	case *tg.MessagesSendMessageRequest:
		id, err = a.peerUser(ctx, me, r.Peer)
	case *tg.MessagesStartBotRequest:
		id, err = a.inputUser(ctx, me, r.Bot)
	case *tg.MessagesGetBotCallbackAnswerRequest:
		id, err = a.peerUser(ctx, me, r.Peer)
	case *tg.MessagesSetTypingRequest:
		id, err = a.peerUser(ctx, me, r.Peer)
	case *tg.MessagesSaveDraftRequest:
		id, err = a.peerUser(ctx, me, r.Peer)
	default:
		return false
	}
	if err != nil {
		return false
	}
	b, ok, err := a.bots.ByKind(ctx, bots.SpamBot)
	return err == nil && ok && b.UserID == id
}

// freezeConfig is the appConfig part that tells the apps the account is
// read-only (freeze_until_date set is what they check).
func (a *API) freezeConfig(ctx context.Context) []tg.JSONObjectValue {
	u, err := a.me(ctx)
	if err != nil || !u.Frozen() {
		return nil
	}
	since, err := a.accounts.FrozenSince(ctx, u.ID)
	if err != nil {
		slog.Warn("api: frozen since", "user", u.ID, "err", err)
	}
	// no end date: an admin unfreezes by hand
	out := []tg.JSONObjectValue{
		{Key: "freeze_since_date", Value: &tg.JSONNumber{Value: float64(since)}},
		{Key: "freeze_until_date", Value: &tg.JSONNumber{Value: float64(frozenForever)}},
	}
	// t.me links open in the apps on any instance
	if a.bots != nil {
		if b, ok, err := a.bots.ByKind(ctx, bots.SpamBot); err == nil && ok {
			out = append(out, tg.JSONObjectValue{Key: "freeze_appeal_url", Value: &tg.JSONString{Value: "https://t.me/" + b.Username}})
		}
	}
	return out
}

// AccessChanged tells someone's apps their account state changed: appConfig
// is what they read it from, and it's otherwise only refetched every few hours.
func (a *API) AccessChanged(user int64) {
	a.deliver([]messages.Event{{UserID: user, Update: &tg.UpdateConfig{}}}, edge.Session{})
}

// AnswerAppeal has @SpamBot tell someone an admin's decision.
func (a *API) AnswerAppeal(ctx context.Context, user int64, approved bool, reason string) error {
	return builtin.AnswerAppeal(ctx, a.bots, a.botAPI, user, approved, reason)
}

// the last second of 9999, like unlimited Premium
const frozenForever = 253402300799
