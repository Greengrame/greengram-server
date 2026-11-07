-- The salt agreed in the DH exchange is valid for the new key; Android's
-- tgnet redoes the whole PFS handshake if its bind gets bad_server_salt.
ALTER TABLE auth_keys ADD COLUMN handshake_salt bigint NOT NULL DEFAULT 0;
