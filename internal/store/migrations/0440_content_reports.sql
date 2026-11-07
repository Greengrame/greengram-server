-- What people report from the apps (messages.report, account.reportPeer,
-- stories.report, the spam bar...), for the admins' Reports page. Not
-- @bugsbot's bug_reports: these are about someone else's content.
--
-- chat_kind/chat_id is where it was seen: a private chat (the other user;
-- msg_ids are the reporter's box ids, shared_ids the copies both boxes
-- share) or a group/channel (msg_ids are the channel's). target_key groups
-- reports on the same account or chat: 'u<id>' or 'c<id>'. snapshot is what
-- the reporter saw when they reported, so an edit or delete afterwards
-- doesn't change what the admins judge.
CREATE TABLE content_reports (
    id bigserial PRIMARY KEY,
    reporter_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('message', 'peer', 'photo', 'story', 'reaction', 'spam')),
    chat_kind text NOT NULL CHECK (chat_kind IN ('user', 'channel')),
    chat_id bigint NOT NULL,
    target_key text NOT NULL,
    target_type text NOT NULL CHECK (target_type IN ('user', 'bot', 'group', 'channel')),
    msg_ids int[] NOT NULL DEFAULT '{}',
    shared_ids bigint[] NOT NULL DEFAULT '{}',
    story_id int,
    photo_id bigint,
    reason text[] NOT NULL,
    comment text NOT NULL DEFAULT '',
    snapshot jsonb NOT NULL DEFAULT '{}',
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'dismissed', 'resolved')),
    actions text[] NOT NULL DEFAULT '{}',
    closed_by bigint REFERENCES users(id) ON DELETE SET NULL,
    closed_at timestamptz,
    notified_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX content_reports_reporter ON content_reports(reporter_id, created_at);
CREATE INDEX content_reports_target ON content_reports(target_key, status);
CREATE INDEX content_reports_status ON content_reports(status, created_at DESC);
CREATE INDEX content_reports_unnotified ON content_reports(id) WHERE notified_at IS NULL;
CREATE INDEX content_reports_notified ON content_reports(notified_at DESC) WHERE notified_at IS NOT NULL;
