CREATE TABLE bot_business_connections (
    id text PRIMARY KEY,
    bot_id bigint NOT NULL REFERENCES bots(user_id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    rights bytea NOT NULL,
    recipients bytea NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    date integer NOT NULL
);

CREATE UNIQUE INDEX bot_business_connection_active ON bot_business_connections(user_id) WHERE enabled;
