-- A profile's gifts pinned to the top (payments.toggleStarGiftsPinnedToTop):
-- the place among them, from 1. Only a shown gift with its holder keeps one.
ALTER TABLE gift_receipts ADD COLUMN pinned int;
