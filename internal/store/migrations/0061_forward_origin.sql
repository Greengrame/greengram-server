-- Where a forward came from when it isn't a user: a channel post
-- (fwd_from_chan, fwd_from_post), or a name only, for authors whose
-- forwards privacy hides the link. fwd_date marks a forward either way.
ALTER TABLE messages
    ADD COLUMN fwd_from_chan bigint,
    ADD COLUMN fwd_from_post int,
    ADD COLUMN fwd_from_name text;
ALTER TABLE channel_messages
    ADD COLUMN fwd_from_chan bigint,
    ADD COLUMN fwd_from_post int,
    ADD COLUMN fwd_from_name text;
