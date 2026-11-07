-- Sticker sets and custom emoji packs. Items are documents (kind sticker or
-- emoji). Sets marked default count as installed for everyone until they
-- uninstall them; builtin names the inputStickerSet* constructor a set
-- answers for (topic icons, animated emoji...).

CREATE TABLE sticker_sets (
    id bigint PRIMARY KEY,
    access_hash bigint NOT NULL,
    short_name text NOT NULL,
    title text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('stickers', 'emoji')),
    official boolean NOT NULL DEFAULT false,
    text_color boolean NOT NULL DEFAULT false,
    is_default boolean NOT NULL DEFAULT false,
    builtin text UNIQUE,
    thumb_document_id bigint,
    hash int NOT NULL DEFAULT 0,      -- changes whenever the items do
    created_at int NOT NULL
);
CREATE UNIQUE INDEX sticker_sets_name ON sticker_sets (lower(short_name));

CREATE TABLE sticker_set_items (
    set_id bigint NOT NULL REFERENCES sticker_sets(id) ON DELETE CASCADE,
    position int NOT NULL,
    document_id bigint NOT NULL REFERENCES documents(id),
    emoji text NOT NULL DEFAULT '',
    PRIMARY KEY (set_id, position)
);
CREATE INDEX sticker_set_items_document ON sticker_set_items(document_id);

-- installed = false is an uninstalled default set
CREATE TABLE user_sticker_sets (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    set_id bigint NOT NULL REFERENCES sticker_sets(id) ON DELETE CASCADE,
    installed boolean NOT NULL,
    date int NOT NULL,
    PRIMARY KEY (user_id, set_id)
);
