-- Keyboards bots attach to their messages: TL ReplyMarkup, newest layer.
ALTER TABLE messages ADD COLUMN reply_markup bytea;
ALTER TABLE channel_messages ADD COLUMN reply_markup bytea;
