-- Stored files. The bytes live in the blob store under their sha256 (hex),
-- so the same upload twice is one blob; key is that address.
CREATE TABLE files (
    key text PRIMARY KEY CHECK (key ~ '^[0-9a-f]{64}$'),
    size bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- An upload the client finished (upload.saveFilePart + an InputFile). Apps
-- retry sendMedia with the same InputFile, so it has to resolve again after
-- the parts are gone.
CREATE TABLE uploads (
    owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    file_id bigint NOT NULL,
    key text NOT NULL REFERENCES files(key),
    size bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_id, file_id)
);
