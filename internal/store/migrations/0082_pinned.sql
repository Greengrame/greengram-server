-- Pinned messages (per box: a private pin can be one-sided), messages sent
-- without sound, and service messages in private chats, which so far had
-- none: the note that a message was pinned.
ALTER TABLE messages
    ADD COLUMN pinned boolean NOT NULL DEFAULT false,
    ADD COLUMN silent boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS action bytea;            -- TL MessageAction
CREATE INDEX messages_pinned ON messages(owner_id, peer_id, id DESC) WHERE pinned;

ALTER TABLE channel_messages
    ADD COLUMN pinned boolean NOT NULL DEFAULT false,
    ADD COLUMN silent boolean NOT NULL DEFAULT false;
CREATE INDEX channel_messages_pinned ON channel_messages(channel_id, id DESC) WHERE pinned;

-- service messages aren't bot updates, as in channels
DROP TRIGGER messages_to_bot ON messages;
CREATE TRIGGER messages_to_bot AFTER INSERT ON messages
    FOR EACH ROW WHEN (NOT NEW.out AND NEW.action IS NULL) EXECUTE FUNCTION bot_private_update();
