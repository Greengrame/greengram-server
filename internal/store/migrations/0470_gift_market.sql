-- The collectible gift market (core.telegram.org/api/gifts#reselling-collectible-gifts).
--
-- A catalog gift that upgrades into a collection can be sold "as variants":
-- the shop opens the collection's resale screen instead of selling the plain
-- gift, and next to what owners list there the instance offers every
-- combination of model, symbol and backdrop nobody holds yet. Offers are
-- worked out per request, nothing is stored for them; buying one mints it
-- with the collection's next number. base_stars NULL is the gift's price
-- plus its upgrade. rarity_x100 is what the rarest combination costs
-- against the most common one, 100 being a flat price.
CREATE TABLE gift_market (
    gift_id bigint PRIMARY KEY REFERENCES gift_catalog(id) ON DELETE CASCADE,
    enabled boolean NOT NULL DEFAULT false,
    base_stars bigint CHECK (base_stars > 0),
    rarity_x100 int NOT NULL DEFAULT 300 CHECK (rarity_x100 BETWEEN 100 AND 100000)
);

-- One row: the instance's cut of a sale between users, in permille of the
-- price. The seller gets the rest.
CREATE TABLE gift_market_config (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    commission_permille int NOT NULL DEFAULT 100 CHECK (commission_permille BETWEEN 0 AND 900)
);
INSERT INTO gift_market_config DEFAULT VALUES;

-- An owner's listing. It goes with the gift: a sale, a transfer or the
-- owner taking it off clears it.
ALTER TABLE unique_gifts ADD COLUMN resale_stars bigint CHECK (resale_stars > 0);
ALTER TABLE unique_gifts ADD COLUMN resale_since int NOT NULL DEFAULT 0;
CREATE INDEX unique_gifts_listed ON unique_gifts(set_id) WHERE resale_stars IS NOT NULL;

-- A resale form names the collectible by slug: a listing (unique_id, and
-- the seller it was bought from) or one of the instance's offers (no
-- unique_id yet).
ALTER TABLE payment_forms DROP CONSTRAINT payment_forms_kind_check;
ALTER TABLE payment_forms ADD CONSTRAINT payment_forms_kind_check CHECK (kind IN ('premium', 'star_gift', 'stars_gift', 'giveaway', 'stars_giveaway',
    'star_gift_upgrade', 'star_gift_transfer', 'star_gift_resale'));
ALTER TABLE payment_forms ADD COLUMN slug text NOT NULL DEFAULT '';
ALTER TABLE payment_forms ADD COLUMN unique_id bigint;
ALTER TABLE payment_forms ADD COLUMN seller_id bigint;
