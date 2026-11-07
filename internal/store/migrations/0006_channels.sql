-- Groups and channels. Every group is a supergroup (megagroup), so there is
-- one kind of shared chat: messages are stored once per channel with the
-- channel's own ids, and the channel has its own pts journal
-- (updates.getChannelDifference).

CREATE SEQUENCE channel_ids START 1000;

CREATE TABLE channels (
    id bigint PRIMARY KEY DEFAULT nextval('channel_ids'),
    title text NOT NULL,
    about text NOT NULL DEFAULT '',
    username text,
    megagroup boolean NOT NULL,       -- false = broadcast channel
    signatures boolean NOT NULL DEFAULT false,
    creator_id bigint NOT NULL REFERENCES users(id),
    created_at int NOT NULL,
    default_banned bytea,             -- TL ChatBannedRights, what members may not do
    linked_id bigint REFERENCES channels(id) ON DELETE SET NULL, -- discussion group / its channel
    next_id int NOT NULL DEFAULT 1,
    pts int NOT NULL DEFAULT 0,
    top_id int NOT NULL DEFAULT 0,
    top_date int NOT NULL DEFAULT 0,
    read_outbox_max int NOT NULL DEFAULT 0, -- newest message any member has read
    deleted boolean NOT NULL DEFAULT false
);
CREATE UNIQUE INDEX channels_username ON channels (lower(username)) WHERE username IS NOT NULL;

-- status: creator, admin, member, banned (kicked or restricted), left
CREATE TABLE channel_members (
    channel_id bigint NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status text NOT NULL,
    admin_rights bytea,               -- TL ChatAdminRights
    rank text NOT NULL DEFAULT '',
    banned_rights bytea,              -- TL ChatBannedRights for restricted/kicked
    inviter_id bigint,
    promoted_by bigint,
    date int NOT NULL,
    read_inbox_max int NOT NULL DEFAULT 0,
    pinned_at int,
    PRIMARY KEY (channel_id, user_id)
);
CREATE INDEX channel_members_user ON channel_members(user_id) WHERE status IN ('creator', 'admin', 'member', 'restricted');

CREATE TABLE channel_messages (
    channel_id bigint NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    id int NOT NULL,
    from_id bigint NOT NULL,
    date int NOT NULL,
    post boolean NOT NULL DEFAULT false, -- sent as the channel (broadcast)
    text text NOT NULL DEFAULT '',
    entities bytea,
    action bytea,                     -- TL MessageAction for service messages
    reply_to int,
    fwd_from_id bigint,
    fwd_date int,
    edit_date int,
    random_id bigint,
    -- discussions: a post's copy in the linked group points back here
    fwd_channel_id bigint,
    fwd_channel_msg int,
    PRIMARY KEY (channel_id, id)
);
CREATE UNIQUE INDEX channel_messages_random ON channel_messages(channel_id, from_id, random_id) WHERE random_id IS NOT NULL;

CREATE TABLE channel_journal (
    channel_id bigint NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    pts int NOT NULL,
    pts_count int NOT NULL,
    date int NOT NULL,
    body bytea NOT NULL,              -- one TL Update
    PRIMARY KEY (channel_id, pts)
);

CREATE TABLE channel_invites (
    hash text PRIMARY KEY,
    channel_id bigint NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    creator_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title text NOT NULL DEFAULT '',
    created_at int NOT NULL,
    expire_date int,
    usage_limit int,
    usage int NOT NULL DEFAULT 0,
    revoked boolean NOT NULL DEFAULT false,
    permanent boolean NOT NULL DEFAULT false
);
CREATE INDEX channel_invites_channel ON channel_invites(channel_id);
