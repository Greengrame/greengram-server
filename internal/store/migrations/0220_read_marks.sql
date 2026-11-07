-- Who read a group message and when ("seen by"). Each move of a member's read
-- mark is a row; a message was read at the first mark at or past its id.
-- Kept a week, like Telegram's chat_read_mark_expire_period.
CREATE TABLE channel_read_marks (
    channel_id bigint NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    max_id int NOT NULL,
    date int NOT NULL,
    PRIMARY KEY (channel_id, user_id, max_id)
);
CREATE INDEX channel_read_marks_msg ON channel_read_marks(channel_id, max_id);

-- Marks from before this table: who is known, when isn't. The date of the last
-- message they read is the earliest it can have been.
INSERT INTO channel_read_marks(channel_id, user_id, max_id, date)
SELECT m.channel_id, m.user_id, m.read_inbox_max, x.date
FROM channel_members m
JOIN channel_messages x ON x.channel_id = m.channel_id AND x.id = m.read_inbox_max
WHERE x.date > extract(epoch FROM now())::int - 604800;

-- The outbox mark used to move when authors read their own messages, so
-- everything looked read. Rebuild it from what other people have read.
UPDATE channels c SET read_outbox_max = coalesce((
    SELECT max(x.id) FROM channel_members m
    JOIN channel_messages x ON x.channel_id = m.channel_id AND x.id <= m.read_inbox_max AND x.from_id <> m.user_id AND x.action IS NULL
    WHERE m.channel_id = c.id), 0);
