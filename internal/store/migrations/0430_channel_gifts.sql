-- Ordinary gifts sent to a broadcast channel. A payment form for one names
-- the payer as recipient_id and the channel in channel_id, like a transfer's.
-- An upgrade that keeps the original details of a channel's gift names the
-- channel as the original recipient.
ALTER TABLE unique_gifts ADD COLUMN original_recipient_channel_id bigint REFERENCES channels(id) ON DELETE SET NULL;
