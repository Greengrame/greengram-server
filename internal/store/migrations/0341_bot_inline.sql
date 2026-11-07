-- Inline mode. A person typing "@bot query" is a row here and an inline_query
-- update for the bot; answerInlineQuery fills the row in, and it stays as the
-- cache and as what messages.sendInlineBotResult sends from.
CREATE TABLE bot_inline_queries (
    id bigint PRIMARY KEY,            -- query id, random
    bot_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    query text NOT NULL,
    query_offset text NOT NULL,
    chat_type text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    answered boolean NOT NULL DEFAULT false,
    answer jsonb,
    personal boolean NOT NULL DEFAULT false,
    cache_until timestamptz
);
CREATE INDEX bot_inline_queries_cache ON bot_inline_queries(bot_id, query, query_offset, cache_until) WHERE answered;
CREATE INDEX bot_inline_queries_age ON bot_inline_queries(created_at);

-- chosen_inline_result has nothing else to point at
ALTER TABLE bot_updates ADD COLUMN payload jsonb;
-- a press on a keyboard under a message sent via the bot, not by it
ALTER TABLE bot_callbacks ADD COLUMN inline_message_id text NOT NULL DEFAULT '';
