CREATE TABLE peer_share_themes (
    peer_id bigint NOT NULL,
    channel boolean NOT NULL,
    user_id bigint REFERENCES users(id) ON DELETE CASCADE,
    channel_id bigint REFERENCES channels(id) ON DELETE CASCADE,
    theme bytea NOT NULL,
    PRIMARY KEY (peer_id, channel),
    CHECK (num_nonnulls(user_id, channel_id) = 1)
);
