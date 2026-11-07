-- Forum topics in groups. A topic's id is the id of the service message
-- that created it, like Telegram; General is id 1 and owns the messages
-- with no topic, which is everything written before the group became a
-- forum.

ALTER TABLE channels ADD COLUMN forum boolean NOT NULL DEFAULT false;
ALTER TABLE channels ADD COLUMN forum_tabs boolean NOT NULL DEFAULT false;

-- NULL = General (or not a forum)
ALTER TABLE channel_messages ADD COLUMN topic_id int;
CREATE INDEX channel_messages_topic ON channel_messages(channel_id, topic_id, id) WHERE topic_id IS NOT NULL;

CREATE TABLE channel_topics (
    channel_id bigint NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    id int NOT NULL,
    title text NOT NULL,
    title_missing boolean NOT NULL DEFAULT false,
    icon_color int NOT NULL DEFAULT 0,
    icon_emoji_id bigint NOT NULL DEFAULT 0,
    creator_id bigint NOT NULL,
    date int NOT NULL,
    closed boolean NOT NULL DEFAULT false,
    hidden boolean NOT NULL DEFAULT false,
    pinned_pos int,                   -- higher comes first; NULL = not pinned
    read_outbox_max int NOT NULL DEFAULT 0, -- newest message in it any member has read
    PRIMARY KEY (channel_id, id)
);

-- A member's read mark in one topic. The member's channel mark
-- (channel_members.read_inbox_max) still counts for every topic: reading
-- the whole group, or joining it, reads the topics up to there.
CREATE TABLE channel_topic_reads (
    channel_id bigint NOT NULL,
    user_id bigint NOT NULL,
    topic_id int NOT NULL,
    read_inbox_max int NOT NULL,
    PRIMARY KEY (channel_id, user_id, topic_id),
    FOREIGN KEY (channel_id, topic_id) REFERENCES channel_topics(channel_id, id) ON DELETE CASCADE
);
