ALTER TABLE bug_reports ADD COLUMN title text NOT NULL DEFAULT '';
ALTER TABLE bug_reports ADD COLUMN published boolean NOT NULL DEFAULT false;
ALTER TABLE bug_reports ADD COLUMN status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed', 'fix coming', 'fixed'));
CREATE INDEX bug_reports_public ON bug_reports(created_at DESC) WHERE published;

CREATE TABLE bug_report_votes (
    report_id bigint NOT NULL REFERENCES bug_reports(id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    value smallint NOT NULL CHECK (value IN (-1, 1)),
    PRIMARY KEY (report_id, user_id)
);

CREATE TABLE bug_report_comments (
    id bigserial PRIMARY KEY,
    report_id bigint NOT NULL REFERENCES bug_reports(id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    text text NOT NULL CHECK (char_length(text) BETWEEN 1 AND 4000),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX bug_report_comments_report ON bug_report_comments(report_id, id);
