# Moving an instance from greengram-web to greengram-server

`cmd/ggimport` reads greengram-web's SQLite database (read-only) and fills
this server's Postgres. Accounts keep their ids, passwords, phones and web
sessions, so people stay signed in on the web and sign in to the apps with
what they already know.

Never run it against the live file. Every step below works on copies.

## What comes over

| web (SQLite)                                   | here (Postgres)                                          |
|------------------------------------------------|----------------------------------------------------------|
| `users`, `user_profiles`, `bots`               | `users`: same id, username, name, password hash, role, disabled, is_system, is_bot, phone (still encrypted), phone_lookup, premium_until, bio, username_public |
| `sessions` + `session_metadata` (live ones)    | `sessions`: same token hash, expiry, user agent          |
| `account_settings.privacy`                     | `privacy_rules`, same JSON; chat exceptions become channel ids |
| `account_settings.contacts` / `contactNames`   | `contacts`                                               |
| `settings`                                     | `instance_settings`                                      |
| `chats` kind `group` (+ `chat_profiles`)       | `channels` with the **same id**: megagroup, or broadcast when `is_channel`; public username only when `visibility='public'`; `discussion_links` become `linked_id` |
| `members`, `chat_admins`, `created_by`         | `channel_members`: creator / admin / member              |
| messages in groups and channels                | `channel_messages`, renumbered from 1 per channel in send order; replies, edits, entities, user forwards, discussion copies (`discussion_posts`) and comment threads (as replies to the post copy); service events as `messageActionCustomAction` |
| messages in `saved` and `direct` chats         | `messages`: a copy per box with per-box ids; `shared_id` = the old global id; replies remapped per box; "delete for me" (`chat_visibility.clear_until`) is honoured per user |
| `chat_preferences.last_read_id`, `pinned`      | dialog / member read marks and pins                      |
| `message_forwards`                             | `fwd_*` columns (migration 0061): a user, a channel post (renumbered), or a name; a user whose `forwards` privacy isn't "everybody" comes over as a name, since the header is now fixed when written |
| `blocked_users`                                | `blocked_users` (migration 0060), dated the import       |

Mapping tables (migration 0040): `import_chats` (old chat -> kind, channel),
`import_box_messages` (old message id -> each box copy),
`import_channel_messages` (old message id -> channel message). Old `/m/<id>`
links resolve through them: `GET /api/messages/<id>/link` (the web's path
and JSON, new ids) and `GET /m/<id>`, both with the session cookie.

Left out, on purpose, with a hook for the branch that owns it:

- **Media** (`message_files`, `message_stickers`, `message_rich_*`, chat and
  profile photos): the message rows are there (text, or empty text for a
  bare file); the media import reads the same SQLite copy, finds each message
  through `import_box_messages` / `import_channel_messages`, and attaches.
  Custom emoji entities keep the web's document ids (sticker id + 4000000000).
- **Stars, wallet, fragment, gifts** (`star_ledger`, `wallet_*`, `crypto_*`,
  `fragment_*`, `gift_*`, `phone_aliases`): stage for the stars branch.
- **Bots** (`bots` token/state/menu): only `is_bot` is set now, plus the built-ins' `bots` row so startup keeps them.
- **Admin** (`audit_log`, `instance_countries`, branding).
- Private service events (imported as plain text), reactions, pinned messages,
  drafts, mutes, archive, folders, invite links, scheduled
  messages, message TTLs. Per-user session lifetimes fall back to the
  instance's `sessionDays`.
- Redacted messages (from erased accounts) are skipped; the web only showed a
  placeholder for them.

A new stage goes into `internal/importer` as one more entry in `stages`,
recomputing what it needs from the SQLite file and the mapping tables. Its
migration takes the next free number in 0041-0049.

Journals start empty: user boxes at pts 0, channels at pts 1. Apps can't
carry state over anyway (auth keys aren't imported), so they sign in fresh.

## The switch

Times are rough; the import itself takes minutes for an instance with a few
thousand accounts.

### 1. Copies (5 min)

On the web host, with the web server still up:

```
sqlite3 /path/to/data/greengram.sqlite3 ".backup /tmp/gg-copy.sqlite3"
cp ~/.local/state/greengram/instance-secret.key /tmp/gg-secret.key   # or $GREENGRAM_SECRET_KEY_FILE
```

`.backup` gives one consistent file with the WAL folded in; a plain `cp`
of a live WAL database does not (ggimport opens the copy immutable, never
writes to it, and refuses to start if a non-empty `-wal` sits next to it). Move both files to the new box
(`scp`), mode 0600. The key file is 32 raw bytes; the new server must use
**this** file (`GREENGRAM_SECRET_KEY_FILE`), or phones won't decrypt and
phone sign-in breaks.

### 2. Dry run on the copy (2 min)

```
go build -o ggimport ./cmd/ggimport
./ggimport -sqlite /tmp/gg-copy.sqlite3 -secret /tmp/gg-secret.key -db "$GREENGRAM_DATABASE_URL" -dry-run
```

Writes nothing (not even migrations). Read the counts per stage. Expect
`phones_unreadable` to be absent; if it isn't, the key is the wrong file.
`skipped_*`, `sender_not_member`, `chats_bad_members` and `unknown_sender`
should be 0 or tiny; look at those rows in the copy before going on.

### 3. Rehearsal (10 min)

Import the copy into a scratch database and click around with the web
client and an app:

```
createdb gg_rehearsal
./ggimport -sqlite /tmp/gg-copy.sqlite3 -secret /tmp/gg-secret.key -db postgres://.../gg_rehearsal
GREENGRAM_DATABASE_URL=postgres://.../gg_rehearsal GREENGRAM_SECRET_KEY_FILE=/tmp/gg-secret.key ./greengram-server
```

Check: your own account signs in by phone and by username; your chat list,
a long DM and a group look right; `/api/messages/<some old id>/link`.

### 4. Real run (15 min, the downtime)

1. Put the web server in maintenance (stop it) so nothing changes.
2. Take the final copy exactly as in step 1.
3. Import into the production database (empty; the tool runs migrations):

   ```
   ./ggimport -sqlite /tmp/gg-final.sqlite3 -secret /tmp/gg-secret.key -db "$GREENGRAM_DATABASE_URL"
   ```

   It refuses a target that already has accounts. If a stage fails, fix the
   cause and run the same command again: finished stages are skipped and the
   failed one starts over (each stage is one transaction). `-force` wipes the
   target and starts from scratch, only for a target that is really yours to
   throw away.
4. Start greengram-server with the copied key
   (`GREENGRAM_SECRET_KEY_FILE`) and the same domain.

### 5. Verify (5 min)

- `curl https://<your domain>/healthz`
- Sign in on the web with an old account; an existing browser should still
  be signed in (`/api/me` is 200 with the old cookie).
- An app signs in, sees the chat list, history and unread counts.
- Spot-check counts against the copy:

  ```
  SELECT stage, counts FROM import_stages ORDER BY done_at;
  SELECT count(*) FROM users;       -- = SELECT count(*) FROM users in SQLite
  SELECT count(*) FROM channels;    -- = chats WHERE kind='group'
  ```

### 6. Cutover

Point the reverse proxy for your domain at greengram-server and leave the
web container stopped (not removed). Keep `/tmp/gg-final.sqlite3` and the
key: the media and stars imports read them later.

### Rollback

Until people have used the new server for real, rollback is just pointing
the proxy back at the web container and starting it: its SQLite file was
never written by the import. Anything sent on the new server after the
cutover does not go back to the web; say so before switching if the window
was long. To retry the import later, drop and recreate the Postgres
database (or `-force`) and start again at step 1.
