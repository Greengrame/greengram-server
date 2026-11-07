-- Temp keys go once expires_at passes; keys from a handshake that never
-- carried a call go after a while. Keys from before this are kept.
ALTER TABLE auth_keys ADD COLUMN used boolean NOT NULL DEFAULT true;
ALTER TABLE auth_keys ALTER COLUMN used SET DEFAULT false;
CREATE INDEX auth_keys_expires ON auth_keys(expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX auth_keys_unused ON auth_keys(created_at) WHERE NOT used;
