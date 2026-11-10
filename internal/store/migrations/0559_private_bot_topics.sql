ALTER TABLE messages ADD COLUMN bot_topic_id integer NOT NULL DEFAULT 0;
CREATE TABLE bot_private_topics (
 bot_id bigint NOT NULL REFERENCES bots(user_id) ON DELETE CASCADE,
 user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 id integer NOT NULL,
 title text NOT NULL,
 icon_color integer NOT NULL DEFAULT 0,
 icon_emoji_id bigint NOT NULL DEFAULT 0,
 closed boolean NOT NULL DEFAULT false,
 pinned boolean NOT NULL DEFAULT false,
 creator_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 date integer NOT NULL,
 PRIMARY KEY(bot_id,id)
);
CREATE INDEX private_bot_topics_user ON bot_private_topics(bot_id,user_id,id);
CREATE INDEX private_topic_messages ON messages(owner_id,peer_id,bot_topic_id,id) WHERE bot_topic_id<>0;
