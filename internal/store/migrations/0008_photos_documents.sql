-- Photos and documents, Telegram style: immutable once made, addressed by
-- id + access_hash. access_hash is random per row, so knowing an id is not
-- enough to download.

CREATE TABLE photos (
    id bigint PRIMARY KEY,
    access_hash bigint NOT NULL,
    owner_id bigint REFERENCES users(id) ON DELETE SET NULL, -- who uploaded it
    date int NOT NULL
);

-- type is Telegram's size letter: s m x y w, smallest first
CREATE TABLE photo_sizes (
    photo_id bigint NOT NULL REFERENCES photos(id) ON DELETE CASCADE,
    type text NOT NULL,
    w int NOT NULL,
    h int NOT NULL,
    key text NOT NULL REFERENCES files(key),
    size int NOT NULL,
    PRIMARY KEY (photo_id, type)
);

-- kind: file video round voice audio gif sticker emoji
CREATE TABLE documents (
    id bigint PRIMARY KEY,
    access_hash bigint NOT NULL,
    owner_id bigint REFERENCES users(id) ON DELETE SET NULL, -- NULL for seeded packs
    date int NOT NULL,
    mime text NOT NULL,
    kind text NOT NULL,
    key text NOT NULL REFERENCES files(key),
    size bigint NOT NULL,
    attributes bytea,                -- TL Vector<DocumentAttribute>, newest layer
    thumb_key text REFERENCES files(key), -- one "m" thumb, JPEG
    thumb_w int,
    thumb_h int,
    thumb_size int
);

-- Profile photos, newest first; users.photo_id is the one shown.
CREATE TABLE user_photos (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    photo_id bigint NOT NULL REFERENCES photos(id) ON DELETE CASCADE,
    date int NOT NULL,
    PRIMARY KEY (user_id, photo_id)
);
CREATE INDEX user_photos_order ON user_photos(user_id, date DESC, photo_id DESC);

ALTER TABLE users ADD COLUMN photo_id bigint REFERENCES photos(id) ON DELETE SET NULL;
ALTER TABLE channels ADD COLUMN photo_id bigint REFERENCES photos(id) ON DELETE SET NULL;

-- A message's media: the TL clients get (MessageMedia, newest layer), which
-- photo or document it is, and its kind for the shared-media filters.
-- grouped_id ties an album together.
ALTER TABLE messages
    ADD COLUMN media bytea,
    ADD COLUMN media_kind text,
    ADD COLUMN media_id bigint,
    ADD COLUMN grouped_id bigint;
CREATE INDEX messages_media ON messages(owner_id, media_kind, id DESC) WHERE media_kind IS NOT NULL;

ALTER TABLE channel_messages
    ADD COLUMN media bytea,
    ADD COLUMN media_kind text,
    ADD COLUMN media_id bigint,
    ADD COLUMN grouped_id bigint;
CREATE INDEX channel_messages_media ON channel_messages(channel_id, media_kind, id DESC) WHERE media_kind IS NOT NULL;
