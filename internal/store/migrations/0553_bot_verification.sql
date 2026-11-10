CREATE TABLE bot_verifiers (
    bot_id bigint PRIMARY KEY REFERENCES bots(user_id) ON DELETE CASCADE,
    icon bigint NOT NULL,
    company text NOT NULL,
    description text NOT NULL DEFAULT '',
    can_modify_description boolean NOT NULL DEFAULT false
);
CREATE TABLE bot_peer_verifications (
    bot_id bigint NOT NULL REFERENCES bot_verifiers(bot_id) ON DELETE CASCADE,
    user_id bigint UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    channel_id bigint UNIQUE REFERENCES channels(id) ON DELETE CASCADE,
    description bytea NOT NULL,
    CHECK (num_nonnulls(user_id, channel_id) = 1)
);
