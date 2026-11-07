-- What cmd/ggimport brought over from greengram-web's SQLite database.
-- Stage rows make a run resumable; the maps let anything that still speaks
-- old ids (/m/<id> links, the media, stars and admin imports to come) find
-- where a chat or message lives now.

CREATE TABLE import_stages (
    stage text PRIMARY KEY,
    source text NOT NULL,             -- fingerprint of the SQLite file
    counts jsonb NOT NULL,
    done_at timestamptz NOT NULL DEFAULT now()
);

-- Every old chat. Groups and channels kept their id (channels.id = old_id);
-- saved and direct chats became box messages of their members.
CREATE TABLE import_chats (
    old_id bigint PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('saved', 'direct', 'group', 'channel')),
    channel_id bigint REFERENCES channels(id) ON DELETE SET NULL,
    members bigint[] NOT NULL DEFAULT '{}' -- saved/direct only
);

-- A private message had one global id; now there is a copy per box. The
-- copies also carry it as shared_id.
CREATE TABLE import_box_messages (
    old_id bigint NOT NULL,
    owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    id int NOT NULL,
    PRIMARY KEY (old_id, owner_id)
);
CREATE INDEX import_box_messages_new ON import_box_messages(owner_id, id);

CREATE TABLE import_channel_messages (
    old_id bigint PRIMARY KEY,
    channel_id bigint NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    id int NOT NULL
);
CREATE INDEX import_channel_messages_new ON import_channel_messages(channel_id, id);
