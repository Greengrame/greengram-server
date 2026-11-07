-- Packs people make in the Stickers mini app belong to them; seeded and
-- imported packs have no owner.
ALTER TABLE sticker_sets ADD COLUMN owner_id bigint REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX sticker_sets_owner ON sticker_sets(owner_id) WHERE owner_id IS NOT NULL;
