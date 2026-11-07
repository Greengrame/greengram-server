-- Accounts the instance speaks through: announcements (shown to clients as
-- 777000, service notifications), the payments accounts. kind is the
-- greengram-web name so imports line up.
-- 0010_stars may have made it already (same columns)
CREATE TABLE IF NOT EXISTS system_accounts (
    kind text PRIMARY KEY,
    user_id bigint NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE
);

-- One announcement to many people. sent moves as the fan-out runs, so a
-- restart mid-way shows how far it got.
CREATE TABLE broadcasts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_id bigint REFERENCES users(id) ON DELETE SET NULL,
    audience text NOT NULL CHECK (audience IN ('all', 'admins', 'active')),
    text text NOT NULL,
    recipients int NOT NULL DEFAULT 0,
    sent int NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);
