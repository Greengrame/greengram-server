-- Message reactions. Both copies of a private message share one set, by
-- shared_id; a channel message has its own. reaction is the emoticon, or
-- '#' and the document id for a custom emoji. position is the order of a
-- user's own reactions (chosen_order). author_id is who wrote the message:
-- a reaction stays unread for them until they look at it.

CREATE TABLE message_reactions (
    shared_id bigint NOT NULL,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reaction text NOT NULL,
    position int NOT NULL,
    big boolean NOT NULL DEFAULT false,
    author_id bigint NOT NULL,
    unread boolean NOT NULL DEFAULT false,
    date int NOT NULL,
    PRIMARY KEY (shared_id, user_id, reaction)
);
CREATE INDEX message_reactions_unread ON message_reactions(author_id, shared_id) WHERE unread;

CREATE TABLE channel_reactions (
    channel_id bigint NOT NULL,
    msg_id int NOT NULL,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reaction text NOT NULL,
    position int NOT NULL,
    big boolean NOT NULL DEFAULT false,
    author_id bigint NOT NULL,
    unread boolean NOT NULL DEFAULT false,
    date int NOT NULL,
    PRIMARY KEY (channel_id, msg_id, user_id, reaction),
    FOREIGN KEY (channel_id, msg_id) REFERENCES channel_messages(channel_id, id) ON DELETE CASCADE
);
CREATE INDEX channel_reactions_unread ON channel_reactions(author_id, channel_id) WHERE unread;
