-- @SpamBot, and the appeals frozen accounts file through it. status: open,
-- then approved or rejected by an admin in the panel.
ALTER TABLE bots DROP CONSTRAINT bots_kind_check;
ALTER TABLE bots ADD CONSTRAINT bots_kind_check CHECK (kind IN ('custom', 'botfather', 'stickers', 'wallet', 'fragment', 'spambot'));

CREATE TABLE freeze_appeals (
    id bigserial PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    text text NOT NULL,
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'approved', 'rejected')),
    reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz,
    decided_by bigint REFERENCES users(id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX freeze_appeals_one_open ON freeze_appeals(user_id) WHERE status = 'open';
CREATE INDEX freeze_appeals_recent ON freeze_appeals(user_id, decided_at DESC) WHERE status = 'rejected';
