-- Saved Messages split by where each message came from, Telegram's
-- "saved dialogs": a forward files under the channel or user it came
-- from, a forward with only a name under "author hidden" (0), anything
-- else under the owner ("my notes"). NULL outside Saved Messages.
ALTER TABLE messages ADD COLUMN saved_peer bigint GENERATED ALWAYS AS (
    CASE
        WHEN owner_id <> peer_id THEN NULL
        WHEN fwd_from_chan IS NOT NULL THEN -fwd_from_chan
        WHEN fwd_from_id IS NOT NULL THEN fwd_from_id
        WHEN fwd_from_name IS NOT NULL THEN 0
        ELSE owner_id
    END) STORED;
CREATE INDEX messages_saved ON messages(owner_id, saved_peer, id DESC) WHERE saved_peer IS NOT NULL;

CREATE TABLE saved_dialog_pins (
    owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    saved_peer bigint NOT NULL,
    pinned_at int NOT NULL,           -- order, newest first
    PRIMARY KEY (owner_id, saved_peer)
);
