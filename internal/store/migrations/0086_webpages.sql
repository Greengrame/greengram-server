-- Link previews fetched for messages, by normalized URL. ok is false for
-- a page that had nothing to show (or didn't answer); fetched_at says
-- when to ask again.
CREATE TABLE webpages (
    url text PRIMARY KEY,
    id bigint NOT NULL,
    type text NOT NULL DEFAULT '',
    site_name text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    photo_id bigint REFERENCES photos(id) ON DELETE SET NULL,
    hash int NOT NULL DEFAULT 0,
    ok boolean NOT NULL,
    fetched_at int NOT NULL
);
