-- Bots. A bot is a users row with is_bot; this is the rest of it. Built-in
-- bots (BotFather, Stickers, Wallet, Fragment) have no owner and no token:
-- the server runs them itself off the same update queue.
CREATE TABLE bots (
    user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    owner_id bigint REFERENCES users(id) ON DELETE CASCADE,
    kind text NOT NULL DEFAULT 'custom' CHECK (kind IN ('custom', 'botfather', 'stickers', 'wallet', 'fragment')),
    token_hash text UNIQUE,           -- sha256 hex of "<id>:<secret>", like greengram-web
    webapp_key bytea,                 -- HMAC-SHA256("WebAppData", token): signs mini app initData
    description text NOT NULL DEFAULT '', -- "What can this bot do?"; the short one is users.bio
    commands jsonb NOT NULL DEFAULT '[]',
    menu_button jsonb,                -- {"text", "url"}; NULL = the commands menu
    main_app_url text,
    info_version int NOT NULL DEFAULT 1, -- clients cache bot_info by this
    state jsonb NOT NULL DEFAULT '{}', -- BotFather settings we keep but don't act on yet
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX bots_owner ON bots(owner_id) WHERE owner_id IS NOT NULL;

-- setChatMenuButton for one private chat
CREATE TABLE bot_chat_menu_buttons (
    bot_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    button jsonb NOT NULL,
    PRIMARY KEY (bot_id, user_id)
);

-- What getUpdates hands out, oldest first. Rows point at the message (or
-- callback query) and are rendered when read, so an edit before the bot
-- polls shows the new text, as on Telegram. id is the update_id.
CREATE TABLE bot_updates (
    id bigint GENERATED ALWAYS AS IDENTITY,
    bot_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind text NOT NULL,               -- message, edited_message, callback_query
    chat_id bigint NOT NULL,          -- user (private) or channel id
    channel boolean NOT NULL DEFAULT false,
    msg_id int,                       -- in the bot's box, or the channel's id
    query_id bigint,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (bot_id, id)
);

-- Filled by triggers so every write path (MTProto, Bot API, imports) feeds
-- bots without knowing about them. The notify wakes long polls.
CREATE FUNCTION bot_private_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM bots WHERE user_id = NEW.owner_id) THEN
        RETURN NULL;
    END IF;
    INSERT INTO bot_updates(bot_id, kind, chat_id, msg_id)
        VALUES (NEW.owner_id, CASE WHEN TG_OP = 'INSERT' THEN 'message' ELSE 'edited_message' END, NEW.peer_id, NEW.id);
    PERFORM pg_notify('greengram_bots', NEW.owner_id::text);
    RETURN NULL;
END $$;

CREATE TRIGGER messages_to_bot AFTER INSERT ON messages
    FOR EACH ROW WHEN (NOT NEW.out) EXECUTE FUNCTION bot_private_update();
CREATE TRIGGER messages_to_bot_edit AFTER UPDATE OF edit_date ON messages
    FOR EACH ROW WHEN (NOT NEW.out AND NEW.edit_date IS DISTINCT FROM OLD.edit_date) EXECUTE FUNCTION bot_private_update();

CREATE FUNCTION bot_channel_update() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    b bigint;
BEGIN
    -- service messages (joins, title changes) aren't bot updates yet
    IF NEW.action IS NOT NULL THEN
        RETURN NULL;
    END IF;
    FOR b IN
        INSERT INTO bot_updates(bot_id, kind, chat_id, channel, msg_id)
        SELECT m.user_id, CASE WHEN TG_OP = 'INSERT' THEN 'message' ELSE 'edited_message' END, NEW.channel_id, true, NEW.id
        FROM channel_members m JOIN bots ON bots.user_id = m.user_id
        WHERE m.channel_id = NEW.channel_id AND m.status IN ('creator', 'admin', 'member', 'restricted') AND m.user_id <> NEW.from_id
        RETURNING bot_id
    LOOP
        PERFORM pg_notify('greengram_bots', b::text);
    END LOOP;
    RETURN NULL;
END $$;

CREATE TRIGGER channel_messages_to_bots AFTER INSERT ON channel_messages
    FOR EACH ROW EXECUTE FUNCTION bot_channel_update();
CREATE TRIGGER channel_messages_to_bots_edit AFTER UPDATE OF edit_date ON channel_messages
    FOR EACH ROW WHEN (NEW.edit_date IS DISTINCT FROM OLD.edit_date) EXECUTE FUNCTION bot_channel_update();

-- A press on an inline button. The pressing client waits for the answer
-- (messages.getBotCallbackAnswer), the bot gives it with answerCallbackQuery.
CREATE TABLE bot_callbacks (
    id bigint PRIMARY KEY,            -- query id, random
    bot_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    chat_id bigint NOT NULL,
    channel boolean NOT NULL,
    msg_id int NOT NULL,              -- as the bot sees it
    data bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    answered boolean NOT NULL DEFAULT false,
    text text NOT NULL DEFAULT '',
    alert boolean NOT NULL DEFAULT false,
    url text NOT NULL DEFAULT '',
    cache_time int NOT NULL DEFAULT 0
);
CREATE INDEX bot_callbacks_age ON bot_callbacks(created_at);
