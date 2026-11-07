-- Fragment: collectible usernames and anonymous numbers, sold for TON from
-- the wallet. Same tables as greengram-web; its SQLite triggers that kept
-- reserved usernames away from users and channels are Go checks now
-- (account sign-up, channels.UsernameFree).

-- id is "username:<name>" or "number:<digits>"; title "@name" or "+888...".
-- Number titles are stored in clear like the web did: they are public
-- listings, not someone's private phone.
CREATE TABLE fragment_items (
    id text PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('username', 'number', 'gift')),
    title text NOT NULL,
    units numeric(80, 0) NOT NULL CHECK (units >= 0), -- nanograms
    owner_id bigint REFERENCES users(id) ON DELETE SET NULL, -- NULL = admin reservation
    listed boolean NOT NULL DEFAULT true,
    purchased_at int,
    country_code text NOT NULL DEFAULT '',
    display_format text NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX fragment_items_title ON fragment_items(kind, lower(title));
CREATE INDEX fragment_items_owner ON fragment_items(owner_id) WHERE owner_id IS NOT NULL;

CREATE TABLE fragment_quotes (
    id text PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    item_id text NOT NULL REFERENCES fragment_items(id) ON DELETE CASCADE,
    units numeric(80, 0) NOT NULL,
    expires_at int NOT NULL,
    result jsonb
);

-- A bought number made primary; previous_phone (GCM context
-- "fragment-phone:<uid>") is the number it replaced, still good for sign-in.
CREATE TABLE fragment_assignments (
    user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    item_id text NOT NULL UNIQUE REFERENCES fragment_items(id),
    previous_phone text,
    previous_lookup text
);
CREATE INDEX fragment_assignments_previous ON fragment_assignments(previous_lookup) WHERE previous_lookup IS NOT NULL;

-- Numbers an account gave up as primary but still signs in with.
-- phone is encrypted with context "phone-alias:<uid>:<lookup>".
CREATE TABLE phone_aliases (
    lookup text PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    phone text NOT NULL
);
CREATE INDEX phone_aliases_user ON phone_aliases(user_id);
