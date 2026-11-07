-- Private messages, Telegram style: every user has their own box with its
-- own message ids, so a message between two people is stored twice (once
-- per box). shared_id links the copies for edits, deletes and read marks.
-- Groups and channels come with phase 5 and live elsewhere.

CREATE TABLE user_boxes (
    user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    next_id int NOT NULL DEFAULT 1,  -- next message id in this box
    pts int NOT NULL DEFAULT 0       -- head of the update journal
);

CREATE SEQUENCE message_shared_ids;

CREATE TABLE messages (
    owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    id int NOT NULL,
    peer_id bigint NOT NULL,         -- the other user; owner_id for Saved Messages
    from_id bigint NOT NULL,
    out boolean NOT NULL,
    shared_id bigint NOT NULL,
    date int NOT NULL,
    text text NOT NULL DEFAULT '',
    entities bytea,                  -- TL Vector<MessageEntity>, newest layer
    reply_to int,                    -- id in this box
    fwd_from_id bigint,
    fwd_date int,
    edit_date int,
    random_id bigint,                -- sender's copy only: re-sends are no-ops
    PRIMARY KEY (owner_id, id)
);
CREATE INDEX messages_peer ON messages(owner_id, peer_id, id DESC);
CREATE INDEX messages_shared ON messages(shared_id);
CREATE UNIQUE INDEX messages_random ON messages(owner_id, random_id) WHERE random_id IS NOT NULL;

CREATE TABLE dialogs (
    owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    peer_id bigint NOT NULL,
    top_id int NOT NULL,
    top_date int NOT NULL,
    read_inbox_max int NOT NULL DEFAULT 0,
    read_outbox_max int NOT NULL DEFAULT 0,
    pinned_at int,                   -- order among pinned dialogs, newest first
    PRIMARY KEY (owner_id, peer_id)
);
CREATE INDEX dialogs_order ON dialogs(owner_id, top_date DESC, top_id DESC);

-- Everything that changed a box, in pts order, for updates.getDifference.
-- body is one TL Update (newest layer) with its pts fields filled in.
CREATE TABLE update_journal (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    pts int NOT NULL,
    pts_count int NOT NULL,
    date int NOT NULL,
    body bytea NOT NULL,
    PRIMARY KEY (user_id, pts)
);

CREATE TABLE contacts (
    owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    first_name text NOT NULL DEFAULT '',
    last_name text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_id, user_id)
);
