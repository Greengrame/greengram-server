ALTER TABLE bots ADD COLUMN manager_bot_id bigint REFERENCES bots(user_id) ON DELETE SET NULL;
CREATE TABLE bot_secondary_usernames (
 bot_id bigint PRIMARY KEY REFERENCES bots(user_id) ON DELETE CASCADE,
 username text NOT NULL UNIQUE,
 CHECK(username=lower(username))
);
