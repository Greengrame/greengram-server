-- account.updateNotifySettings: a user's defaults for private chats,
-- groups and channels (scope users/chats/broadcasts) and their exceptions
-- for one chat (scope peer, with peer_user or peer_channel set). body is
-- the TL PeerNotifySettings the apps get back.
CREATE TABLE notify_settings (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope text NOT NULL CHECK (scope IN ('peer', 'users', 'chats', 'broadcasts')),
    peer_user bigint NOT NULL DEFAULT 0,
    peer_channel bigint NOT NULL DEFAULT 0,
    body bytea NOT NULL,
    PRIMARY KEY (user_id, scope, peer_user, peer_channel)
);
