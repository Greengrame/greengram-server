-- The admin panel's Stickers & Emoji page: packs can be Premium only or unpublished.
ALTER TABLE sticker_sets ADD COLUMN premium_only boolean NOT NULL DEFAULT false;
ALTER TABLE sticker_sets ADD COLUMN enabled boolean NOT NULL DEFAULT true;
