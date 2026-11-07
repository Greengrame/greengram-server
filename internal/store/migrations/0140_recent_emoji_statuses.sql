-- the status picker's recent row, one row per emoji a user has worn
CREATE TABLE recent_emoji_statuses (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    document_id bigint NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    used_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, document_id)
);
CREATE INDEX recent_emoji_statuses_user ON recent_emoji_statuses (user_id, used_at DESC);
