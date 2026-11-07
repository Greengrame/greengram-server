-- Direct links: named mini apps of a bot, /u/<bot>/<short_name>?startapp=...
CREATE TABLE bot_apps (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    bot_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    access_hash bigint NOT NULL,
    short_name text NOT NULL,
    title text NOT NULL,
    description text NOT NULL,
    url text NOT NULL,
    photo_id bigint NOT NULL,
    version int NOT NULL DEFAULT 1,   -- messages.getBotApp's hash
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (bot_id, short_name)
);
