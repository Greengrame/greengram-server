-- "Telegram Emoji's by @trillio" was never meant to ship. The documents stay:
-- old messages name them by id. ggstickers skips the pack from now on
-- (removedPacks in internal/media/seed.go).
CREATE TEMP TABLE dropped_emoji ON COMMIT DROP AS
    SELECT i.document_id FROM sticker_set_items i JOIN sticker_sets s ON s.id = i.set_id
    WHERE lower(s.short_name) = 'emojiuzbek'
      AND NOT EXISTS (SELECT 1 FROM sticker_set_items o JOIN sticker_sets t ON t.id = o.set_id
                      WHERE o.document_id = i.document_id AND lower(t.short_name) <> 'emojiuzbek');
UPDATE users SET emoji_status_id = NULL, emoji_status_until = NULL
    WHERE emoji_status_id IN (SELECT document_id FROM dropped_emoji);
DELETE FROM recent_emoji_statuses WHERE document_id IN (SELECT document_id FROM dropped_emoji);
DELETE FROM sticker_sets WHERE lower(short_name) = 'emojiuzbek';
