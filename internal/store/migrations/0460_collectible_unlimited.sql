-- A collection's supply of 0 is unlimited: upgrades keep taking the next
-- number for as long as people upgrade. issued only has to stay under a
-- supply that's set. No rows change: a collection nothing upgrades into yet
-- already has 0, and every one that's upgradable has a supply of 1 or more.
ALTER TABLE collectible_sets DROP CONSTRAINT collectible_sets_check;
ALTER TABLE collectible_sets ADD CONSTRAINT collectible_sets_issued_check CHECK (issued >= 0 AND (supply = 0 OR issued <= supply));
