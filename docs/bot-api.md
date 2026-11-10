# Bot API

Create a bot through @BotFather on your Greengram instance and keep its token
secret. Bot accounts and tokens belong to this instance. Use your instance URL
in your bot library instead of `api.telegram.org`.

The API base is `https://<instance>/bot<token>/`. Requests accept GET query
parameters, JSON POST bodies, URL-encoded forms, or multipart forms. Structured
form parameters such as `reply_markup` must contain JSON strings.

## Start a bot

1. Send `/newbot` to @BotFather, choose a name and username, and copy the token.
2. Open your bot and send `/start`. A bot cannot initiate a private conversation.
3. Call `getMe` to check the token, then poll `getUpdates` with `timeout=50`.
4. Process each update and send replies with `sendMessage`.
5. Set the next `offset` to the last processed `update_id + 1`.

```
export BOT_BASE='https://chat.example.org/bot<TOKEN>'
curl "$BOT_BASE/getMe"
curl -H 'Content-Type: application/json' \
  -d '{"timeout":50,"offset":0}' "$BOT_BASE/getUpdates"
curl -H 'Content-Type: application/json' \
  -d '{"chat_id":123,"text":"Hello!","reply_markup":{"inline_keyboard":[[{"text":"Press","callback_data":"hello"}]]}}' \
  "$BOT_BASE/sendMessage"
```

A runnable Python example uses only the standard library:

```
export GREENGRAM_URL='https://chat.example.org'
export BOT_TOKEN='<TOKEN>'
python3 examples/echo_bot.py
```

The example echoes text and answers inline button presses. It runs one polling
worker and keeps its offset in memory.

Read the `callback_query` updates and call `answerCallbackQuery` with the
query's `id`. Bot API user IDs and message IDs are the IDs returned by this
API; use those directly. Supergroup and channel IDs are negative.

Only one polling request per bot can run at once; a second request terminates
the first with HTTP 409. Webhooks are unavailable. `getWebhookInfo` reports an
empty URL. `deleteWebhook` supports `drop_pending_updates`.

## Working methods

| Area | Methods |
| --- | --- |
| Identity | `getMe`, `setMyName`, `getMyName`, `setMyDescription`, `getMyDescription`, `setMyShortDescription`, `getMyShortDescription` |
| Updates | `getUpdates`, `getWebhookInfo`, `deleteWebhook`; `setWebhook` accepts an empty URL only |
| Messages | `sendMessage`, `sendRichMessage`, `editMessageText`, `editMessageCaption`, `editMessageReplyMarkup`, `deleteMessage`, `deleteMessages`, `sendChatAction` |
| Files | `sendPhoto`, `sendLivePhoto`, `sendDocument`, `sendVideo`, `sendAudio`, `sendVoice`, `sendAnimation`, `sendSticker`, `sendVideoNote`, `getFile` |
| Other media | `sendContact`, `sendLocation` (static), `sendVenue`, `sendDice` |
| Queries | `answerCallbackQuery`, `answerInlineQuery` |
| Chats | `getChat`, `getChatMember`, `getChatMemberCount`, `getChatAdministrators`, `leaveChat` |
| Bot settings | `setMyCommands`, `getMyCommands`, `deleteMyCommands`, `setChatMenuButton`, `getChatMenuButton` |
| Collectibles | `getUniqueGift` |
| Widgets | `sendWidget`, `sendSharedWidget`, `answerWidgetQuery`, `editMessageWidgetData`, `editSharedWidgetData` |
| Ephemeral replies | The send methods above accept `ephemeral_message_parameters`; `editEphemeralMessageText`, `editEphemeralMessageMedia`, `editEphemeralMessageCaption`, `editEphemeralMessageReplyMarkup`, `deleteEphemeralMessage` |
| Share themes | `getChatShareThemeBackdrops`; `getChat` returns the saved share theme |
| Verification | `verifyUser`, `verifyChat`, `removeUserVerification`, `removeChatVerification` |
| Business | `getBusinessConnection`, `deleteBusinessAccountGift` |
| Private bot topics | `createForumTopic`, `editForumTopic`, `closeForumTopic`, `reopenForumTopic`, `deleteForumTopic` |
| Managed bots | `getManagedBotToken`, `replaceManagedBotToken`, `request_managed_bot` keyboard buttons |

Commands and profile text support the default language and command scope.
Inline queries must be enabled in the bot's settings. Access restrictions,
blocked users, membership, and chat permissions apply to Bot API messages.

## Upload and download files

Send files as multipart uploads, or reuse a `file_id` returned to the same bot.
`attach://<field name>` references another multipart file field. Files are
limited to 50 MiB; photos to 10 MiB. Photo input must decode as an image.

```
curl -F chat_id=123 -F 'document=@report.pdf' -F caption='Your report' \
  "$BOT_BASE/sendDocument"
curl -F chat_id=123 -F 'photo=@picture.jpg' "$BOT_BASE/sendPhoto"
curl -H 'Content-Type: application/json' \
  -d '{"file_id":"<FILE_ID>"}' "$BOT_BASE/getFile"
```

Download using `https://<instance>/file/bot<token>/<file_path>`, where
`file_path` is returned by `getFile`. File identifiers are signed for the bot;
another bot cannot reuse them. Token rotation invalidates these identifiers.
`file_unique_id` is stable across bots and cannot be used to download a file.
Direct HTTP URL fetching, albums, ordinary-message media replacement, live
locations, and arbitrary media metadata conversion are not implemented.
Ephemeral media replacement accepts uploads and reusable file IDs.
Live photos take `live_photo` for the video and `photo` for the static image;
the video is limited to 10 MiB and 10 seconds of declared duration.

Media messages and updates contain the corresponding `photo`, `document`,
`video`, `audio`, `voice`, `animation`, `sticker`, or `video_note` fields, with
captions under `caption` and `caption_entities`. Contacts, locations, venues,
and dice have their corresponding fields. Uploaded media is stored in the
same message and blob stores used by MTProto clients.

## November 9, 2026 additions

Reference: [Telegram Bot API 10.4 changelog](https://core.telegram.org/bots/api-changelog#november-9-2026).
These additions run against Greengram accounts, messages, collectibles, and
wallet balances. This is not a claim of complete compatibility with every
older Telegram Bot API method or the Telegram blockchain.

| Addition | Greengram behavior |
| --- | --- |
| Widgets | Durable private and shared widgets, authenticated recipient access, bot query updates and answers, per-message data and cached shared public data |
| Widget wallet queries | Local GRAM wallet cards, passcode-protected Ed25519 signing, and atomic ledger transfers with idempotent request IDs |
| Ephemeral callback replies | `callback_query_id` resolves the recipient and chat; `receiver_user_id` and `chat_id` can be omitted; replies stay out of ordinary history |
| Rich quotes | `reply_parameters.quote_rich_texts` is stored in native reply headers and returned as `quote.rich_texts` |
| Wallet cards and NFTs | Published local wallet cards, collectible stickers, area positions, collectible attributes, and transfer service messages |
| Share themes | User/channel theme storage, emoji or owned unique gifts, backdrop catalog, and `getChat` rendering |
| Gifts | `getUniqueGift(name)` and authorized regular-gift deletion without crediting Stars |
| Managed bots | Suggested secondary suffixes, Premium checks, a two-bot limit, native creation consent, token access restricted to the manager, and `managed_bot` updates |
| Rich TON addresses | `ton_address` rich text and message entities; `bank_card_number` message entities |
| Verification descriptions | HTML or explicit entities for custom descriptions, with administrator-issued verifier permissions |
| Paid-message service messages | `paid_messages_waived` is serialized for native waiver service actions; ordinary Greengram messages do not carry a Telegram Stars sending fee |
| Private topic links | `https://t.me/<bot_username>/<message_thread_id>` opens a topic only in the private conversation where it exists |

## Widgets

Send a self-contained UTF-8 document using `html`. The limits are 32 KiB
uncompressed, 4 KiB compressed, and 1 KiB per state string. Set `is_shared`
to create a shared widget and keep the returned `widget.id` for
`sendSharedWidget` or `editSharedWidgetData`. Set `allow_queries` if the
widget needs to call the bot.

```sh
curl -H 'Content-Type: application/json' \
  -d '{"chat_id":123,"html":"<button data-query=\"hello\" onclick=\"TelegramWidget.request(this.dataset.query).then(r=>this.textContent=r.result)\">Ask bot</button>","allow_queries":true}' \
  "$BOT_BASE/sendWidget"
```

Consume `widget_query` updates and answer their `id` using
`answerWidgetQuery(widget_query_id, data)` within 15 seconds. Query data is
1-64 UTF-8 bytes. Answers and widget state are limited to 1 KiB. Shared
public-data edits are limited to once per second; `cache_time` is 5-3600
seconds, with a default of 30.

Widgets require the updated Greengram web client. The server carries widgets
in protected internal webpage messages because the current native schema has
no widget constructor. The client executes scripts in QuickJS with a bounded
DOM, SVG, Canvas 2D, timers, and browser utility bridge. External resources,
network APIs, forms, and keyboard focus are unavailable. Wallet actions open
a trusted client consent dialog outside the widget.

Production widgets are private-chat messages with protected content. Copying,
forwarding, and inline-query widget results are unavailable, matching the
production restrictions in the referenced release. Shared widgets are sent
again with `sendSharedWidget`; sharing a widget does not expose its per-chat
state to another chat.

## Local GRAM wallet

Wallet queries use the existing instance ledger. Addresses start with
`gram:`; collectible addresses start with `gram:nft:`. Amounts are decimal
strings in smallest units, with 1 GRAM equal to 1,000,000,000 units.
`ton_wallet_card` and widget wallet query fields retain their Bot API names.
They do not represent TON accounts or blockchain transactions.

A card becomes public only after explicit card-sharing consent or a card
sticker update. Signing and paying alone do not publish a card. Signing and
payments require the wallet passcode. Payments debit and credit the ledger
and enqueue the bot event in one transaction. Retrying the same request ID
with the same payload returns the previous result without paying twice;
a conflicting payload is rejected.

The signature response is a JSON string containing `network: "greengram"`,
`algorithm: "ed25519"`, `domain: "bot<id>"`, `timestamp`, `payload`, and a
base64 signature. Verify the signature with the wallet's hex public key over
the SHA-256 digest of:

```text
greengram-sign-data:v1:bot<id>\n<timestamp>\n<canonical JSON payload>
```

The canonical payload is JSON with sorted object keys. `state_init` is the
base64 public key for this local adapter. Neither the signature format nor
`state_init` is a TON Connect proof or a TON smart-contract state.

A payment query uses `type: "sent_transaction"` and `msg_hash` containing its
local transaction reference. A signed-data query uses `type: "sign_data"`.
The wallet card can display owned local collectible gifts as stickers; the
server validates ownership when saving and reading them.

## Rich messages and ephemeral replies

`sendRichMessage` accepts exactly one of `html`, `markdown`, or `blocks`.
Structured blocks support paragraphs, headings, preformatted text, footers,
dividers, mathematical expressions, anchors, lists, quotations, details,
and tables. Unsupported block types or HTML tags return an error. Embedded
media blocks and automatic entity detection are not implemented in this
converter. Existing native rich messages are stored in the native rich format.

For a callback reply, pass:

```json
{
  "text": "Only the person who pressed the button sees this",
  "ephemeral_message_parameters": {
    "callback_query_id": "<CALLBACK_QUERY_ID>"
  }
}
```

The callback must belong to the bot and be fresh. Explicit receivers must
match the callback, or belong to the specified chat. Edits and deletion are
scoped to the bot, receiver, chat, and ephemeral message ID. Ephemeral records
expire after 24 hours. Use `replace_callback_query_message` to anchor the
reply in place of the callback message.

For a rich quote, set `reply_parameters.message_id` and
`reply_parameters.quote_rich_texts`. Quoted text must occur in the original
rich message, and the quote must fit within 1024 UTF-16 units. Cross-chat
quotes are unavailable.

## Bot settings and permissions

Use `/settopics` in BotFather to enable private bot topic mode. Use
`/setsecondaryusername` to add or remove an eligible secondary username.
Regular primary bot usernames still end in `bot`. Secondary names resolve
through contacts and appear in bot profiles.

A `request_managed_bot` keyboard button opens a client confirmation and
creation form. It does not create a bot merely by being displayed. The user
remains the owner and the requesting bot receives manager access to its token.

Business access comes from `account.updateConnectedBot` in an authenticated
client session. A bot cannot grant itself rights. Gift deletion requires the
active connection's gift-conversion permission and only removes eligible
regular gifts belonging to that business account. Revoking a connection
immediately prevents later deletions.

Verification requires an administrator grant through
`PATCH /api/admin/bots/{id}/verifier`. The administrator chooses an existing
custom emoji, company, default description, and whether custom descriptions
are allowed. Grants and revocations are audited. A bot cannot replace another
verifier's badge.

## Validation and limits

Use polling against the instance URL. Webhooks, arbitrary remote file
fetching, and unrelated older Telegram methods outside the working-methods
table remain unavailable. Unsupported method names return HTTP 404.

The client renderer is implemented for Greengram and is not a complete browser
or a byte-for-byte implementation of every Telegram widget profile behavior.
Server integration checks cover files, callback privacy, rich quotes,
private-topic isolation, gifts, verification, widgets, secondary usernames,
and local-wallet payment rollback.
