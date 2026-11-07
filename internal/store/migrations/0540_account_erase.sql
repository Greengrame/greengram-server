-- Account deletion, from the admin panel, account.deleteAccount and the
-- inactivity job (account.setAccountTTL). A deleted account keeps its row,
-- so its messages still have an author; everything else about it goes (see
-- internal/erase).

-- When the account was last signed in somewhere, for "delete my account if
-- away for". Sessions can be gone long before that question comes up, so it
-- lives on the account. Existing accounts start from their newest session.
ALTER TABLE users ADD COLUMN last_seen_at timestamptz;
UPDATE users u SET last_seen_at = greatest(u.created_at, (SELECT max(s.last_active) FROM sessions s WHERE s.user_id = u.id));
ALTER TABLE users ALTER COLUMN last_seen_at SET DEFAULT now();
ALTER TABLE users ALTER COLUMN last_seen_at SET NOT NULL;
CREATE INDEX users_last_seen ON users(last_seen_at) WHERE account_state <> 'deleted' AND NOT is_system AND NOT is_bot;

-- source: admin, self or inactive
ALTER TABLE users ADD COLUMN deleted_at timestamptz;
ALTER TABLE users ADD COLUMN deletion_source text;
CREATE INDEX users_deleted ON users(id) WHERE account_state = 'deleted';

-- The password check before account.deleteAccount happens over HTTPS (the
-- password never travels over MTProto); it is good for a few minutes and
-- only for the session that passed it.
ALTER TABLE sessions ADD COLUMN delete_confirmed_at timestamptz;

-- What a Fragment item last sold for; units is the asking price once it is
-- listed again. Filled from the purchases on record.
ALTER TABLE fragment_items ADD COLUMN last_sale_units numeric(80, 0);
UPDATE fragment_items f SET last_sale_units = q.units
FROM (SELECT DISTINCT ON (item_id) item_id, units FROM fragment_quotes WHERE result IS NOT NULL ORDER BY item_id, expires_at DESC) q
WHERE q.item_id = f.id AND f.purchased_at IS NOT NULL;

-- The circulation page counts per catalog gift and per holder without
-- reading the table's rows.
CREATE INDEX gift_receipts_gift ON gift_receipts(gift_id, owner_id) INCLUDE (converted, unique_id, owner_channel_id);
