-- Group Privacy and Bot-to-Bot Communication Mode. Both used to be off the map:
-- a bot in a group got every message, other bots' included. A new bot now starts
-- with privacy on and bot-to-bot off, as on Telegram. Every bot that exists today
-- is written down as privacy off, bot-to-bot on, so it keeps getting exactly what
-- it got; its owner can turn either on in BotFather. A missing "priv" means on.
UPDATE bots SET state = state || jsonb_build_object('settings', coalesce(state->'settings', '{}') || '{"priv": false, "loop": true}');

ALTER TABLE messages ADD COLUMN via_bot_id bigint;
ALTER TABLE channel_messages ADD COLUMN via_bot_id bigint;

-- Bots that answer each other at machine speed are the warning on the settings
-- page. A bot's own filtering comes first; this is the floor under a pair that
-- has none. Past the limit the message still lands in the chat, the other bots
-- just don't hear of it, and with nothing to answer the loop stops.
CREATE TABLE bot_loop_guard (
    chat text NOT NULL,            -- c<channel> or p<lower bot>:<higher bot>
    minute timestamptz NOT NULL,
    sent int NOT NULL,
    PRIMARY KEY (chat, minute)
);

CREATE FUNCTION bot_loop_tripped(key text) RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE
    n int;
BEGIN
    INSERT INTO bot_loop_guard(chat, minute, sent) VALUES (key, date_trunc('minute', now()), 1)
        ON CONFLICT (chat, minute) DO UPDATE SET sent = bot_loop_guard.sent + 1
        RETURNING sent INTO n;
    RETURN n > 30;
END $$;

-- What one bot gets of a group message. Commands are the first word, the way
-- Telegram's server finds them; /start@other is for the other bot. Telegram also
-- holds a bare /start back from all but the bot that spoke last; here every bot
-- in the group gets it, which is what the settings page says.
CREATE FUNCTION bot_gets_group_message(bot bigint, username text, state jsonb, member_status text, sender_bot boolean, sender_loop boolean,
    body text, replied_to bigint, via bigint) RETURNS boolean LANGUAGE plpgsql STABLE AS $$
DECLARE
    privacy boolean := coalesce(state #>> '{settings,priv}' NOT IN ('false', '0'), true);
    loop_mode boolean := coalesce(state #>> '{settings,loop}' IN ('true', '1'), false);
    reads_all boolean := NOT privacy OR member_status IN ('creator', 'admin');
    low text := lower(body);
    named boolean := replied_to = bot OR low ~ ('(^|\s)/[a-z0-9_]{1,64}@' || lower(username) || '($|[^a-z0-9_])');
BEGIN
    IF sender_bot THEN
        IF named THEN
            RETURN loop_mode OR sender_loop;
        END IF;
        RETURN loop_mode AND reads_all;
    END IF;
    RETURN reads_all OR named OR via = bot
        OR low ~ '^/[a-z0-9_]{1,64}($|[^a-z0-9_@])'
        OR low ~ ('(^|[^a-z0-9_])@' || lower(username) || '($|[^a-z0-9_])');
END $$;

CREATE OR REPLACE FUNCTION bot_channel_update() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    b bigint;
    broadcast boolean;
    sender_bot boolean;
    sender_loop boolean;
    replied bigint;
BEGIN
    -- service messages (joins, title changes) aren't bot updates yet
    IF NEW.action IS NOT NULL THEN
        RETURN NULL;
    END IF;
    -- most groups have no bots: don't pay for the lookups below
    IF NOT EXISTS (SELECT 1 FROM channel_members m JOIN bots ON bots.user_id = m.user_id
            WHERE m.channel_id = NEW.channel_id AND m.status IN ('creator', 'admin', 'member', 'restricted') AND m.user_id <> NEW.from_id) THEN
        RETURN NULL;
    END IF;
    SELECT NOT megagroup INTO broadcast FROM channels WHERE id = NEW.channel_id;
    SELECT true, coalesce(state #>> '{settings,loop}' IN ('true', '1'), false) INTO sender_bot, sender_loop FROM bots WHERE user_id = NEW.from_id;
    sender_bot := coalesce(sender_bot, false);
    IF sender_bot AND bot_loop_tripped('c' || NEW.channel_id) THEN
        RETURN NULL;
    END IF;
    IF NEW.reply_to IS NOT NULL THEN
        SELECT from_id INTO replied FROM channel_messages WHERE channel_id = NEW.channel_id AND id = NEW.reply_to AND NOT post;
    END IF;
    FOR b IN
        INSERT INTO bot_updates(bot_id, kind, chat_id, channel, msg_id)
        SELECT m.user_id, CASE WHEN TG_OP = 'INSERT' THEN 'message' ELSE 'edited_message' END, NEW.channel_id, true, NEW.id
        FROM channel_members m JOIN bots ON bots.user_id = m.user_id JOIN users u ON u.id = m.user_id
        WHERE m.channel_id = NEW.channel_id AND m.status IN ('creator', 'admin', 'member', 'restricted') AND m.user_id <> NEW.from_id
            -- privacy is about groups: a channel's member bots get its posts
            AND (coalesce(broadcast, false) OR bot_gets_group_message(m.user_id, u.username, bots.state, m.status, sender_bot, sender_loop, NEW.text, replied, NEW.via_bot_id))
        RETURNING bot_id
    LOOP
        PERFORM pg_notify('greengram_bots', b::text);
    END LOOP;
    RETURN NULL;
END $$;

-- bot to bot in private needs both bots in the mode (botapi checks that before
-- it sends); the guard is the same as in groups
CREATE OR REPLACE FUNCTION bot_private_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM bots WHERE user_id = NEW.owner_id) THEN
        RETURN NULL;
    END IF;
    IF EXISTS (SELECT 1 FROM bots WHERE user_id = NEW.from_id) AND NEW.from_id <> NEW.owner_id
        AND bot_loop_tripped('p' || least(NEW.owner_id, NEW.from_id) || ':' || greatest(NEW.owner_id, NEW.from_id)) THEN
        RETURN NULL;
    END IF;
    INSERT INTO bot_updates(bot_id, kind, chat_id, msg_id)
        VALUES (NEW.owner_id, CASE WHEN TG_OP = 'INSERT' THEN 'message' ELSE 'edited_message' END, NEW.peer_id, NEW.id);
    PERFORM pg_notify('greengram_bots', NEW.owner_id::text);
    RETURN NULL;
END $$;
