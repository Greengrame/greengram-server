-- Articles: Telegram's rich messages (headings, lists, tables...), as a TL
-- richMessage. NULL is an ordinary message. text keeps the plain words, for
-- search and for clients on a layer without rich messages.
ALTER TABLE messages ADD COLUMN rich bytea;
ALTER TABLE channel_messages ADD COLUMN rich bytea;
