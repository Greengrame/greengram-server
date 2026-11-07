-- Owned collectible usernames besides the primary one. active = shown on the
-- profile and resolvable; position = order among them. Only owned, unlisted
-- rows mean anything; selling one clears active.
ALTER TABLE fragment_items ADD COLUMN active boolean NOT NULL DEFAULT false;
ALTER TABLE fragment_items ADD COLUMN position int NOT NULL DEFAULT 0;
CREATE INDEX fragment_items_active ON fragment_items(lower(title)) WHERE active;
