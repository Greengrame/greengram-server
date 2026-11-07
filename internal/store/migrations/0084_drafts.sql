-- messages.saveDraft, shared by all of a user's apps. A draft is for a
-- private chat (peer_user) or a channel (peer_channel), the other one 0,
-- and a comment thread or 0. body is the TL DraftMessage the apps get.
CREATE TABLE drafts (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    peer_user bigint NOT NULL DEFAULT 0,
    peer_channel bigint NOT NULL DEFAULT 0,
    thread int NOT NULL DEFAULT 0,
    body bytea NOT NULL,
    PRIMARY KEY (user_id, peer_user, peer_channel, thread)
);
