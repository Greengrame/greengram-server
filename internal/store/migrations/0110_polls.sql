-- Polls and quizzes. A message carries the poll as MessageMediaPoll with
-- media_kind 'poll' and media_id the poll id; forwards keep the id, so
-- every copy shows the same votes. body is the TL Poll (newest layer) as
-- created, answers added later included; closed and the per-viewer flags
-- live outside it.
CREATE TABLE polls (
    id bigint PRIMARY KEY,
    creator_id bigint NOT NULL,
    date int NOT NULL,
    body bytea NOT NULL,
    closed boolean NOT NULL DEFAULT false,
    close_date int,                   -- closes by itself then
    correct int[] NOT NULL DEFAULT '{}', -- quiz: indexes of the right answers
    solution text NOT NULL DEFAULT '',
    solution_entities bytea,
    solution_media bytea,             -- TL MessageMedia
    attached_media bytea
);
CREATE INDEX polls_due ON polls(close_date) WHERE NOT closed AND close_date IS NOT NULL;

CREATE TABLE poll_votes (
    poll_id bigint NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    option bytea NOT NULL,
    date int NOT NULL,
    PRIMARY KEY (poll_id, user_id, option)
);
CREATE INDEX poll_votes_recent ON poll_votes(poll_id, date DESC, user_id DESC);

-- who to tell when the votes change
CREATE INDEX messages_poll ON messages(media_id) WHERE media_kind = 'poll';
CREATE INDEX channel_messages_poll ON channel_messages(media_id) WHERE media_kind = 'poll';
