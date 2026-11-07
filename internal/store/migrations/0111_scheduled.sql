-- Scheduled messages wait here until their date, visible to the person who
-- scheduled them only. Their ids are their own space, as in Telegram: the
-- message gets a real id in each box when it goes out. Exactly one of
-- peer_id (a private chat) and channel_id is set.
CREATE SEQUENCE scheduled_message_ids AS int;

CREATE TABLE scheduled_messages (
    id int PRIMARY KEY DEFAULT nextval('scheduled_message_ids'),
    owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    peer_id bigint NOT NULL DEFAULT 0,
    channel_id bigint NOT NULL DEFAULT 0,
    date int NOT NULL,
    repeat_period int NOT NULL DEFAULT 0, -- seconds; goes out again after that
    edit_date int,
    text text NOT NULL DEFAULT '',
    entities bytea,
    reply_to int,
    media bytea,
    media_kind text,
    media_id bigint,
    grouped_id bigint,
    random_id bigint,
    fwd_from_id bigint,               -- a scheduled forward keeps its origin
    fwd_from_chan bigint,
    fwd_from_post int,
    fwd_from_name text,
    fwd_date int,
    claimed_until int                 -- a sender is on it until then
);
CREATE INDEX scheduled_due ON scheduled_messages(date);
CREATE INDEX scheduled_chat ON scheduled_messages(owner_id, peer_id, channel_id, date);
CREATE UNIQUE INDEX scheduled_random ON scheduled_messages(owner_id, random_id) WHERE random_id IS NOT NULL;
