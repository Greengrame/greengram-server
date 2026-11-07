-- A private message can be a service message now (gifts), same encoding as
-- channel_messages.action. NULL is an ordinary message, so old rows and the
-- gifts already sent as plain text stay as they are.
-- IF NOT EXISTS: 0082 adds it too, and a database can have either one first.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS action bytea;
