-- Instance-wide settings the admin panel edits (branding, limits, flags).
-- One row per key; the value shape is owned by the Go code that reads it.
CREATE TABLE instance_settings (
    key text PRIMARY KEY,
    value jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
