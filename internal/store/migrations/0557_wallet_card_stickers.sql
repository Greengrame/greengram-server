ALTER TABLE local_wallet_cards ADD COLUMN stickers jsonb NOT NULL DEFAULT '[]';
ALTER TABLE local_wallet_cards ADD COLUMN background text NOT NULL DEFAULT '';
