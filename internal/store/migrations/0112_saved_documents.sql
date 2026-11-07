-- A user's saved GIFs, recently sent stickers and favourite stickers.
-- Newest first by pos; saving one again moves it to the front.
CREATE TABLE saved_documents (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    list text NOT NULL,               -- gifs, recent, faved
    document_id bigint NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    date int NOT NULL,
    pos bigint NOT NULL,
    PRIMARY KEY (user_id, list, document_id)
);
CREATE INDEX saved_documents_order ON saved_documents(user_id, list, pos DESC);
