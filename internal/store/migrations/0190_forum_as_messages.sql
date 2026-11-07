-- "View as messages" in a forum is each member's own choice.
ALTER TABLE channel_members ADD COLUMN forum_as_messages boolean NOT NULL DEFAULT false;
