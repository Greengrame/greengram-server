-- "Get boosts via gifts" with Stars as the prize. What the buyer pays goes to
-- the winners in full; the channel's boosts last a year, in the share each
-- winner's row holds, and a giveaway's size decides that share, not a flat 4.
ALTER TABLE payment_forms DROP CONSTRAINT payment_forms_kind_check;
ALTER TABLE payment_forms ADD CONSTRAINT payment_forms_kind_check CHECK (kind IN ('premium', 'star_gift', 'stars_gift', 'giveaway', 'stars_giveaway'));
ALTER TABLE payment_forms ADD COLUMN boosts int NOT NULL DEFAULT 0;

ALTER TABLE channel_gift_boosts ADD COLUMN multiplier int NOT NULL DEFAULT 4;
ALTER TABLE channel_gift_boosts ADD COLUMN stars bigint NOT NULL DEFAULT 0;

-- the giveaway a row belongs to, for the transaction list
ALTER TABLE star_ledger ADD COLUMN channel_id bigint REFERENCES channels(id) ON DELETE SET NULL;
ALTER TABLE star_ledger ADD COLUMN post_id int NOT NULL DEFAULT 0;
