-- Who a group message mentions (by @username, by id, or by replying to
-- them) and whether they have seen it: unread_mentions_count and the
-- jump-to-mention button.
CREATE TABLE channel_mentions (
    channel_id bigint NOT NULL,
    msg_id int NOT NULL,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    unread boolean NOT NULL DEFAULT true,
    PRIMARY KEY (channel_id, user_id, msg_id),
    FOREIGN KEY (channel_id, msg_id) REFERENCES channel_messages(channel_id, id) ON DELETE CASCADE
);
CREATE INDEX channel_mentions_msg ON channel_mentions(channel_id, msg_id);
CREATE INDEX channel_mentions_unread ON channel_mentions(user_id, channel_id) WHERE unread;
