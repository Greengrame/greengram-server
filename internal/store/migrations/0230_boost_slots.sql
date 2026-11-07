-- Accounts have as many slots as they earned now, not two.
ALTER TABLE channel_boosts DROP CONSTRAINT channel_boosts_slot_check;
ALTER TABLE channel_boosts ADD CONSTRAINT channel_boosts_slot_check CHECK (slot >= 1);
