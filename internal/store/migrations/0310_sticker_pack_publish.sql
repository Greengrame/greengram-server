-- A pack made in the Stickers app stays a draft until its maker publishes it:
-- its link only opens for them. Packs that exist already are public.
ALTER TABLE sticker_sets ADD COLUMN draft boolean NOT NULL DEFAULT false;

-- Stickers sent, per sticker per UTC day. Nothing counted them before this, so
-- earlier sends are not in here.
CREATE TABLE sticker_uses (
    document_id bigint NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    day int NOT NULL,
    uses int NOT NULL DEFAULT 0,
    PRIMARY KEY (document_id, day)
);
