-- Taking a channel's username off sale used to leave its item behind, so the
-- name read as its seller's own collectible while the channel still used it.
-- Never bought and still on a channel: it was never a collectible.
DELETE FROM fragment_items f
WHERE f.kind = 'username' AND NOT f.listed AND f.owner_id IS NOT NULL AND f.purchased_at IS NULL
  AND EXISTS (SELECT 1 FROM channels c WHERE lower(c.username) = lower(substr(f.title, 2)) AND NOT c.deleted);
