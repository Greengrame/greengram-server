CREATE TABLE bot_ephemeral_messages (
    id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    bot_id bigint NOT NULL REFERENCES bots(user_id) ON DELETE CASCADE,
    receiver_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    chat_id bigint NOT NULL,
    channel boolean NOT NULL,
    body bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX bot_ephemeral_expiry ON bot_ephemeral_messages(created_at);
