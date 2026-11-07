-- Star reactions: each one moves Stars from the reactor to the message's
-- author (the ledger has the money, these are the totals on the message).
-- Both copies of a private message share a set by shared_id, as with
-- message_reactions.
CREATE TABLE message_paid_reactions (
    shared_id bigint NOT NULL,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    count bigint NOT NULL CHECK (count > 0),
    anonymous boolean NOT NULL DEFAULT false,
    date int NOT NULL,
    PRIMARY KEY (shared_id, user_id)
);

CREATE TABLE channel_paid_reactions (
    channel_id bigint NOT NULL,
    msg_id int NOT NULL,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    count bigint NOT NULL CHECK (count > 0),
    anonymous boolean NOT NULL DEFAULT false,
    date int NOT NULL,
    PRIMARY KEY (channel_id, msg_id, user_id),
    FOREIGN KEY (channel_id, msg_id) REFERENCES channel_messages(channel_id, id) ON DELETE CASCADE
);
