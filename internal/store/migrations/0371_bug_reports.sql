-- @bugsbot, and what people send it. Admins get each one as a message.
ALTER TABLE bots DROP CONSTRAINT bots_kind_check;
ALTER TABLE bots ADD CONSTRAINT bots_kind_check CHECK (kind IN ('custom', 'botfather', 'stickers', 'wallet', 'fragment', 'spambot', 'bugsbot'));

CREATE TABLE bug_reports (
    id bigserial PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('bug', 'suggestion')),
    text text NOT NULL,
    -- in the bot's box; a picture without a caption is a report with no text
    msg_ids int[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX bug_reports_user ON bug_reports(user_id, created_at);
