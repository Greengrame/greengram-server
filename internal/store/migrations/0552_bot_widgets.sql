CREATE TABLE bot_widgets (
    id text PRIMARY KEY,
    bot_id bigint NOT NULL REFERENCES bots(user_id) ON DELETE CASCADE,
    html text NOT NULL,
    width integer NOT NULL DEFAULT 0,
    height integer NOT NULL DEFAULT 0,
    public_data text NOT NULL DEFAULT '',
    shared boolean NOT NULL DEFAULT false,
    allow_queries boolean NOT NULL DEFAULT false,
    cache_time integer NOT NULL DEFAULT 30,
    updated_at timestamptz NOT NULL DEFAULT 'epoch'
);
CREATE TABLE bot_widget_messages (
    id text PRIMARY KEY,
    widget_id text NOT NULL REFERENCES bot_widgets(id) ON DELETE CASCADE,
    receiver_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    message_id integer NOT NULL DEFAULT 0,
    chat_data text NOT NULL DEFAULT ''
);
CREATE INDEX bot_widget_message_lookup ON bot_widget_messages(widget_id, receiver_id, message_id);
CREATE TABLE bot_widget_queries (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    bot_id bigint NOT NULL REFERENCES bots(user_id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    message_key text NOT NULL REFERENCES bot_widget_messages(id) ON DELETE CASCADE,
    answer text,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX bot_widget_query_rate ON bot_widget_queries(bot_id, user_id, created_at);
