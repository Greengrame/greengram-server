-- Chat folders (dialog filters) and the archive.
--
-- A folder is kept as the TL the client sent: the apps filter the chat
-- list themselves, the server only stores, orders and syncs the folders.
-- id 0 is the "All chats" row, present only once the user has put it
-- somewhere other than first.

CREATE TABLE dialog_filters (
    owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    id int NOT NULL,
    pos int NOT NULL,
    body bytea,                      -- TL DialogFilter / DialogFilterChatlist; NULL for id 0
    PRIMARY KEY (owner_id, id)
);

CREATE TABLE dialog_filter_tags (
    owner_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    enabled boolean NOT NULL
);

-- 0 = main list, 1 = archive
ALTER TABLE dialogs ADD COLUMN folder_id int NOT NULL DEFAULT 0;
ALTER TABLE channel_members ADD COLUMN folder_id int NOT NULL DEFAULT 0;
