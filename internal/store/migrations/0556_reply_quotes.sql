ALTER TABLE messages ADD COLUMN reply_quote bytea;
ALTER TABLE channel_messages ADD COLUMN reply_quote bytea;
