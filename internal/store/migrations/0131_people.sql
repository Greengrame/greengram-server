-- contacts.resetTopPeerRating: messages before `since` stop counting
-- toward that peer's place in the category.
CREATE TABLE top_peer_resets (
    owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    category text NOT NULL,
    peer_id bigint NOT NULL,          -- user id, or -channel id
    since int NOT NULL,
    PRIMARY KEY (owner_id, category, peer_id)
);

-- messages.hidePeerSettingsBar: the "add contact / block" bar was closed.
CREATE TABLE peer_settings_hidden (
    owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    peer_id bigint NOT NULL,
    PRIMARY KEY (owner_id, peer_id)
);

-- the box's messages by date, for searchGlobal and top peers
CREATE INDEX messages_owner_date ON messages(owner_id, date DESC, id DESC);
CREATE INDEX channel_messages_from_date ON channel_messages(from_id, date DESC);
