-- BotFather's bot page shows the token behind a spoiler, so the owner needs to
-- read it again. Only the hash can check a token; this is the same token
-- encrypted with the instance secret. NULL for tokens made before this column
-- (they can't be recovered: revoking gives a new one that is kept).
ALTER TABLE bots ADD COLUMN token_sealed text;
