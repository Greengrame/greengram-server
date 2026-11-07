# Gaps against Telegram (Web A)

What Telegram Web A (telegram-tt 12.0.48, layer 232) can ask for that this
server doesn't answer, or answers with an empty stub. Built 2026-11-01 from
`origin/main` (c10c894) by grepping `GramJs.<ns>.<Method>` in
`telegram-tt/src` plus `Api.<ns>.<Method>` in its GramJS client, against the
`handle(a, tlprofile.SemanticMethod...)` registrations in `internal/`.

Numbers at the time: Web A references 499 methods, the server registers 182,
**346 are missing**, 41 of the registered ones are empty stubs in
`internal/api/lookups.go`. An unregistered method answers
`400 METHOD_NOT_IMPLEMENTED` and logs `edge: method not implemented`.

Columns: **who** = which branch/PR owns it (blank = nobody yet);
**data** = `real` needs stored data/logic, `empty` = an empty or fixed answer
is the correct one for us, `n/a` = doesn't apply to this server;
**size** = S (< 1h), M (half a day), L (days).

Owner tags: `#17` chat basics (reactions, drafts, pins, mentions, link
previews, notify settings), `#16` folders, archive, forum topics, `#13`
blocking, destroy_auth_key, idle sweep, `#14` SpamBot/frozen appeals, `#19`
emoji status, `mini` = feat-miniapps-port (stickers/wallet/fragment mini
apps), `media` = feat-messages-media (polls, scheduled, saved GIFs, recent/faved
stickers, dice, contact/geo), `stories` = feat-stories (stories, close
friends), `later` = calls and translation (need outside services),
`gap` = this branch (feat-gap-audit).

Items marked **done (gap)** are what this branch is working on; they turn into **done** as they land.

## Top user-visible gaps

Ordered by how soon a normal user trips over them.

1. **Edit profile does nothing**: `account.updateProfile` (name, bio),
   `account.updateUsername`/`checkUsername`, `account.updateBirthday` are
   missing; Settings > Edit profile spins and fails. **done (gap)**
2. **Delete chat / clear history** in private chats: `messages.deleteHistory`
   missing (groups: `channels.deleteHistory`). The menu item silently fails.
3. **Online status never updates live**: the server never pushes
   `updateUserStatus`; "online"/"last seen" only changes after a reload.
   **done (gap)**
4. **Saved Messages** has no per-chat view: `getSavedDialogs` is an empty
   stub, `getPinnedSavedDialogs`/`toggleSavedDialogPin`/`deleteSavedHistory`
   missing. **done (gap)**
5. **Global search** finds only usernames (no names, no groups or public
   channels) and messages only in private chats; the Links tab is always
   empty. **done (gap)**
6. **Chat background** picker is empty: `account.getWallPapers` missing.
7. **Language** can't be changed: `langpack.*` missing (Web A falls back to
   its bundled English, logs errors each start). **done (gap)**
8. **Copy message link** in groups/channels: `channels.exportMessageLink`
   missing.
9. **Group settings toggles** (history for new members, sign messages, join
   requests, join-to-send, hide members): `channels.toggle*` missing.
10. **Privacy settings that don't stick**: global privacy (archive new
    chats, read time), content settings, auto-delete TTL, account TTL,
    session TTL, contact sign-up toggle. **done (gap)** except auto-delete.

Then: chat-list "mark as unread" (`messages.markDialogUnread`), emoji
suggestions while typing (`getEmojiKeywordsDifference`), timezones in
business hours (`help.getTimezonesList`, **done (gap)**), common groups in a
profile (`messages.getCommonChats`, **done (gap)**), message read time in
private chats (`messages.getOutboxReadDate`), read receipts in small groups
(`messages.getMessageReadParticipants`), view counters in channels
(`messages.getMessagesViews`).

## Missing methods by area

### Account, privacy, sessions

| method | user notices | who | data | size |
|---|---|---|---|---|
| account.updateProfile | name/bio edits fail | gap, **done** | real | S |
| account.checkUsername / updateUsername | can't change @username | gap, **done** | real | S |
| account.updateBirthday | birthday field fails | gap, **done** | real | S |
| account.setGlobalPrivacySettings | archive/read-time toggles revert | gap, **done** | real | S |
| account.getContentSettings / setContentSettings | "show sensitive content" toggle errors | gap, **done** | real | S |
| account.setAccountTTL | "delete my account if away" reverts | gap, **done** | real (no sweeper yet) | S |
| account.setAuthorizationTTL / changeAuthorizationSettings | "terminate old sessions" reverts | gap, **done** | real (no sweeper yet) | S |
| account.setContactSignUpNotification | toggle reverts | gap, **done** | real | S |
| account.getWebAuthorizations / resetWebAuthorization(s) | "Logged in with Telegram" list errors | gap, **done** | empty | S |
| account.unregisterDevice | nothing (push off) | gap, **done** | empty | S |
| account.getCollectibleEmojiStatuses | log noise at start | gap, **done** | empty | S |
| account.updateNotifySettings / getNotifyExceptions | notification settings | #17 | real | M |
| account.updateEmojiStatus | emoji status | #19 | real | S |
| account.getWallPapers / uploadWallPaper | backgrounds list empty, custom bg fails | | real (TG wallpapers) | M |
| account.updatePasswordSettings / getTmpPassword / confirmPasswordEmail, auth.checkPassword | 2FA can't be set | | needs owner call: our login is token handoff, no SRP | L |
| account.getPasskeys (empty; entry hidden with settings_display_passkeys) / initPasskeyRegistration / registerPasskey / deletePasskey, auth.initPasskeyLogin / finishPasskeyLogin | no passkeys entry in settings | **hidden** | n/a for now | L |
| account.reorderUsernames / toggleUsername | collectible usernames order | | real (fragment) | S |
| account.updatePersonalChannel | personal channel on profile | | real | S |
| account.setMainProfileTab | profile tab order | | real | S |
| account.reportPeer / reportProfilePhoto | report dialogs fail | | real (admin queue) | M |
| account.toggleSponsoredMessages | premium "hide ads" | | empty | S |
| account.getPaidMessagesRevenue / toggleNoPaidMessagesException | paid messages | | real (stars) | M |
| account.getSavedMusicIds / saveMusic, users.getSavedMusic(ByID) | profile music | | empty | S |
| account.resolveBusinessChatLink | business links | | empty error | S |
| messages.getDefaultHistoryTTL / setDefaultHistoryTTL / setHistoryTTL | auto-delete timer | | real (needs a deleter job) | M |

### Saved Messages

| method | user notices | who | data | size |
|---|---|---|---|---|
| messages.getSavedDialogs (stub) | Saved Messages has no per-chat list | gap, **done** | real | M |
| messages.getSavedHistory (stub) | opening a saved sub-chat is empty | gap, **done** | real | S |
| messages.getPinnedSavedDialogs / toggleSavedDialogPin / reorderPinnedSavedDialogs | pin in saved | gap, **done** | real | S |
| messages.deleteSavedHistory | delete a saved sub-chat | gap, **done** | real | S |
| messages.getSavedReactionTags (stub) / updateSavedReactionTag | tags in saved | #17 | real | M |

### Contacts and users

| method | user notices | who | data | size |
|---|---|---|---|---|
| contacts.getTopPeers (stub, "disabled") | no "frequent contacts" row in search | gap, **done** | real | M |
| contacts.resetTopPeerRating | remove from frequent | gap, **done** | real | S |
| contacts.getBirthdays (stub) | no birthday banner | gap, **done** | real | S |
| messages.getPeerSettings (stub) / hidePeerSettingsBar | no "add contact / block" bar for strangers | gap, **done** | real | S |
| messages.getCommonChats | "groups in common" empty | gap, **done** | real | S |
| users.getRequirementsToContact | nothing yet (premium-only contacts) | gap, **done** | empty | S |
| contacts.resolvePhone | t.me/+number links | gap, **done** | real | S |
| contacts.updateContactNote | contact notes | | real | S |
| contacts.getSponsoredPeers | nothing | gap, **done** | empty | S |
| photos.uploadContactProfilePhoto | set/suggest a photo for a contact | | real | M |
| users.suggestBirthday | suggest birthday | | real | S |
| contacts.editCloseFriends | close friends | stories | real | S |
| contacts.block / unblock / getBlocked paging | | #13 | | |

### Search

| method | user notices | who | data | size |
|---|---|---|---|---|
| contacts.search (only username prefix) | names, groups, public channels not found | gap, **done** | real | S |
| messages.searchGlobal (private only, no links) | group/channel messages and links missing | gap, **done** | real | M |
| messages.search with InputMessagesFilterUrl | Links tab in a chat empty | gap, **done** | real | S |
| messages.getSearchResultsPositions (stub) | calendar/jump in media viewer | | real | S |
| channels.searchPosts / checkSearchPostsFlood | Posts tab, hashtag search in public posts | **done** (no quota) | real | M |
| mentions filter, getUnreadMentions | | #17 | | |

### help / config / langpack

| method | user notices | who | data | size |
|---|---|---|---|---|
| help.getTimezonesList | business hours / time zone picker empty, log noise | gap, **done** | real (tzdata) | S |
| help.getCountriesList (stub, empty) | country picker empty in phone fields | gap, **done** | real | S |
| langpack.getLanguages / getLanguage / getLangPack / getDifference / getStrings | language list empty, log noise | gap, **done** | real (packs from a dir) | M |
| help.getSupport | "Ask a question" in settings fails | | real (pick an admin) | S |
| help.acceptTermsOfService / dismissSuggestion | nothing visible | gap, **done** | empty | S |
| help.getPremiumPromo | (registered by stars) | | | |
| messages.getEmojiKeywordsDifference (and getEmojiKeywords*) | no emoji suggestions while typing | | real (TG keyword packs) | M |
| messages.getEmojiGroups / getEmojiStickerGroups / getFeaturedEmojiStickers / searchCustomEmoji | emoji panel tabs, log noise | | empty ok | S |
| communities.getJoinedCommunities / toggleCommunityCollapsedInDialogs | log noise | | empty | S |
| aicompose.* | AI compose tones, log noise at start | later | n/a | |

### Chats and messages (not this branch)

| method | user notices | who | data | size |
|---|---|---|---|---|
| messages.deleteHistory | delete/clear private chat fails | | real (journal) | M |
| channels.deleteHistory / deleteParticipantHistory | clear group history, delete all from user | | real | M |
| messages.markDialogUnread | "mark as unread" | | real | S |
| channels.exportMessageLink | copy message link | | real | S |
| channels.togglePreHistoryHidden / toggleSignatures / toggleJoinRequest / toggleJoinToSend / toggleParticipantsHidden / toggleAutotranslation / updatePaidMessagesPrice / setMainProfileTab | group/channel settings toggles | | real | S each |
| channels.toggleUsername / reorderUsernames / deactivateAllUsernames | extra usernames | | real | S |
| channels.getSendAs / messages.saveDefaultSendAs | send as channel | | real | M |
| messages.getChatInviteImporters / hideChatJoinRequest / hideAllChatJoinRequests | join requests list | | real | M |
| messages.deleteRevokedExportedChatInvites | delete revoked links | | real | S |
| messages.editChatCreator / getFutureChatCreatorAfterLeave / editChatParticipantRank | transfer ownership, member tags | | real | M |
| messages.getOutboxReadDate | read time in private chats | | real | S |
| messages.getMessageReadParticipants | who read in small groups | | real | M |
| messages.getMessagesViews | channel view counters | | real | M |
| messages.toggleNoForwards | restrict saving content | | real | S |
| messages.getFullChat / editChatTitle / editChatPhoto / addChatUser / deleteChatUser / deleteChat / migrateChat | nothing: every group is a supergroup here | | n/a | |
| messages.report / reportSpam / channels.reportSpam | report menu fails | #14-adjacent | real (admin queue) | M |
| messages.getSponsoredMessages / viewSponsoredMessage / clickSponsoredMessage / reportSponsoredMessage | nothing | | empty | S |
| messages.getFactCheck / getExtendedMedia | nothing | | empty | S |
| messages.reportMessagesDelivery / reportMusicListen | nothing | | empty (true) | S |
| messages.getQuickReplyMessages / sendQuickReplyMessages | business quick replies | | empty | S |
| messages.getInlineBotResults / sendInlineBotResult | @bot inline queries | | real | L |
| messages.requestUrlAuth / acceptUrlAuth / declineUrlAuth / checkUrlAuthMatchCode | bot login buttons | | real | M |
| messages.sendBotRequestedPeer / sendWebViewData / requestChatJoinWebView | bot buttons | mini | real | M |
| messages.appendTodoList / toggleTodoCompleted | checklists | media? | real | M |
| messages.toggleSuggestedPostApproval | suggested posts | | real | M |
| messages.getRichMessage / composeMessageWithAI / composeRichMessageWithAI / summarizeText / translateText / translateRichMessage / togglePeerTranslations / transcribeAudio | AI, translation, voice-to-text | later | n/a | |
| messages.getDhConfig | secret chats, calls | later | | |
| messages.sendReaction / getMessageReactionsList / getUnreadReactions / readReactions / setChatAvailableReactions / setDefaultReaction / clearRecentReactions / sendPaidReaction / getPaidReactionPrivacy / reportReaction / deleteParticipantReaction(s) | reactions | #17 | | |
| messages.saveDraft, updatePinnedMessage, unpinAllMessages, getUnreadMentions, readMentions, readMessageContents, channels.readMessageContents, getWebPage, getWebPagePreview | | #17 | | |
| messages.updateDialogFilter(sOrder), toggleDialogFilterTags, folders.editPeerFolders, forum topics, channels.toggleForum / toggleViewForumAsMessages, readDiscussion, chatlists.* | | #16 (chatlists: nobody yet) | | |
| messages.sendVote / getPollVotes / addPollAnswer / getUnreadPollVotes / readPollVotes, sendScheduledMessages / deleteScheduledMessages, saveGif / saveRecentSticker / faveSticker / getRecentStickers / clearRecentStickers | | media | | |
| messages.getFeaturedStickers / searchStickerSets / searchStickers / searchEmojiStickerSets | sticker store search | mini? | real | M |
| messages.getAttachMenuBot / toggleBotInAttachMenu, bots.* (allowSendMessage, canSendMessage, invokeWebViewCustomMethod, getPopularAppBots, getPreviewMedias, checkDownloadFileParams, toggleUserEmojiStatusPermission) | mini apps | mini | | |
| ephemeral.* | bot ephemeral messages (new) | | real | M |

### Money, stories, calls, stats

| area | methods | who |
|---|---|---|
| stars/gifts leftovers | payments.* (37: auctions, crafting, resale, subscriptions, giveaways, onramp, receipts, gift codes, revenue) | nobody; phase 7a covered the basics |
| wallet | wallet.*, toncenter.* (Telegram's built-in TON wallet API) | n/a: our wallet is the mini app (mini) |
| stories | stories.* (17), contacts.editCloseFriends | stories |
| calls | phone.* (20), messages.getDhConfig | later |
| stats | stats.* (7) | nobody; needs real graphs |
| auth | auth.signIn / signUp / resendCode / importLoginToken / importWebTokenAuthorization / cancelWebTokenAuthorization / importBotAuthorization / exportAuthorization | n/a: sign-in is the site's /login, one DC |

## Stubs in internal/api/lookups.go

| method | answer now | right for us? | who |
|---|---|---|---|
| messages.getArchivedStickers, getStickers, getEmojiStatusGroups, account.getDefaultProfilePhotoEmojis, getDefaultEmojiStatuses, getRecentEmojiStatuses | empty | until stickers land | mini / #19 |
| messages.getFavedStickers, getSavedGifs | empty | no | media |
| messages.getAvailableReactions, getTopReactions, getRecentReactions, getDefaultTagReactions, getSavedReactionTags, getMessagesReactions | empty | no | #17 |
| messages.getAvailableEffects | empty | yes (no effects pack) | |
| stories.getAllStories, getPinnedStories, getAllReadPeerStories, getAlbums, getPeerMaxIDs | empty | no | stories |
| payments.getStarGiftCollections | empty | until collections | |
| messages.getSearchResultsPositions | empty | no, small | |
| messages.getScheduledHistory | empty | no | media |
| messages.getSavedHistory, getSavedDialogs | empty | no | gap, **done** |
| messages.getAllDrafts | empty | no | #17 |
| messages.getQuickReplies | empty | yes (no business) | |
| messages.getSuggestedDialogFilters, getDialogFilters | empty | no | #16 |
| messages.getPeerSettings | empty | no | gap, **done** |
| messages.reportReadMetrics | true | yes | |
| messages.getAttachMenuBots | empty | until attach-menu bots | mini |
| contacts.getTopPeers | disabled | no | gap, **done** |
| contacts.getBirthdays | empty | no | gap, **done** |
| channels.getAdminedPublicChannels | empty | no: "personal channel" and limits use it | |
| channels.getChannelRecommendations, bots.getBotRecommendations | empty | yes for now | |
| account.getNotifySettings | default | no | #17 |
| account.getGlobalPrivacySettings | default | no | gap, **done** |
| account.getReactionsNotifySettings | default | no | #17 |
| account.getSavedRingtones, getThemes | empty | yes for now | |
| account.getContactSignUpNotification | false | no | gap, **done** |
| account.getAccountTTL | 365 | no | gap, **done** |
| account.getConnectedBots | empty | yes (no business bots) | |
| account.getAutoDownloadSettings | fixed | yes | |
| account.getPassword | no password | yes until 2FA is decided | |
| account.getWebBrowserSettings | not modified | yes | |
| account.registerDevice, help.saveAppLog | true | yes (no push yet) | |
| help.getAppConfig | freeze keys only | partly: premium/limits keys missing | |
| help.getTermsOfServiceUpdate, getPromoData | empty | yes | |
| help.getCountriesList | empty | no | gap, **done** |
| help.getInviteText | instance URL | yes | |

## Updates Web A handles that the server never sends

The server sends 18 update types (new/edit/delete message and channel
message, read inbox/outbox, typing, dialog pinned, peer blocked, channel,
user, stars balance, message id). Web A handles about 115. The ones that
matter:

| update | effect when missing | who |
|---|---|---|
| updateUserStatus | online/last seen frozen until reload | gap, **done** |
| updateUserName | own name/username change not shown on other tabs/devices | gap, **done** |
| updatePrivacy | privacy change not synced to other sessions | gap, **done** |
| updateSavedDialogPinned / updatePinnedSavedDialogs | saved pins not synced | gap, **done** |
| updateNewAuthorization | no "new login" notice | |
| updateDialogUnreadMark | mark-unread not synced | |
| updatePeerSettings | add-contact bar not refreshed | |
| updateContactsReset, updateUserPhone | contacts list stale | |
| updatePeerHistoryTTL | auto-delete | |
| updateChannelMessageViews / updateChannelMessageForwards | counters | |
| updateChatDefaultBannedRights, updatePendingJoinRequests | group settings | |
| updateReadMessagesContents / updateChannelReadMessagesContents, updateDraftMessage, updatePinnedMessages / updatePinnedChannelMessages, updateMessageReactions, updateNotifySettings, updateWebPage / updateChannelWebPage | | #17 |
| updateDialogFilter(Order), updateFolderPeers, updatePinnedForumTopic(s), updateChannelViewForumAsMessages, updateReadChannelDiscussionInbox/Outbox | | #16 |
| updateNewScheduledMessage / updateDeleteScheduledMessages, updateMessagePoll(Vote), updateSavedGifs, updateRecentStickers, updateFavedStickers | | media |
| updateNewStickerSet, updateStickerSets(Order), updateMoveStickerSetToTop, updateRecentEmojiStatuses, updateUserEmojiStatus | | mini / #19 |
| updateStory, updateReadStories, updateStoriesStealthMode, updateSentStoryReaction | | stories |
| updatePhoneCall(SignalingData), updateGroupCall(Participants/Connection) | | later |
| updateTranscribedAudio | | later |
| updateServiceNotification | system popups (e.g. frozen) | #14 |
| updateConfig, updateLangPack(TooLong) | config/langpack refresh | |
| updateShortMessage / updateShortChatMessage / updateShortSentMessage | not needed: we send full Updates | n/a |
| updateChatParticipant*, updateChatUserTyping | basic groups | n/a |
| wallet / gift auction / AI compose updates | | n/a |

## Also incomplete in methods that exist

- `users.getFullUser`: no `common_chats_count`, `birthday`,
  `personal_channel_id`, `read_dates_private`, `phone_calls_available`
  (common chats and birthday **done (gap)**).
- Profile changes (`photos.updateProfilePhoto`) don't push `updateUser` to
  the user's other sessions.
- `account.getPassword` always says "no password"; Web A then offers to set
  one, and that flow fails.
- `help.getAppConfig` sends only frozen-account keys; premium limits, emoji
  sounds, etc. use client defaults.
- No `account.updateProfile` means bios only come from the old site import.

## Done on this branch

See the PR (feat-gap-audit) for the list and how each was checked.
