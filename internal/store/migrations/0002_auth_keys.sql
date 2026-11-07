-- MTProto auth keys. Temp (PFS) keys point at their permanent key once the
-- client calls auth.bindTempAuthKey; the account lives on the permanent key.
CREATE TABLE auth_keys (
    id bigint PRIMARY KEY,
    key bytea NOT NULL CHECK (length(key) = 256),
    perm_id bigint REFERENCES auth_keys(id) ON DELETE CASCADE,
    expires_at timestamptz,
    layer int NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX auth_keys_perm ON auth_keys(perm_id) WHERE perm_id IS NOT NULL;
