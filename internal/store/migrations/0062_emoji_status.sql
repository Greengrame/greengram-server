-- custom emoji status next to a Premium user's name
ALTER TABLE users ADD COLUMN emoji_status_id bigint, ADD COLUMN emoji_status_until bigint;
