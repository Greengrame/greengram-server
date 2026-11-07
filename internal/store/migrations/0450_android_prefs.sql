-- contacts.toggleTopPeers and account.updatePersonalChannel.
ALTER TABLE account_prefs
    ADD COLUMN top_peers_disabled boolean NOT NULL DEFAULT false,
    ADD COLUMN personal_channel_id bigint REFERENCES channels(id) ON DELETE SET NULL;
