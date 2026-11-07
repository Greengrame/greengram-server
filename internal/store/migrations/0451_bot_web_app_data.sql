-- A keyboard button's mini app sends the bot data (messages.sendWebViewData):
-- the bot's copy is a messageActionWebViewDataSentMe service message, which
-- the Bot API hands over as web_app_data. Other service messages stay out.
DROP TRIGGER messages_to_bot ON messages;
CREATE TRIGGER messages_to_bot AFTER INSERT ON messages
    FOR EACH ROW WHEN (NOT NEW.out AND (NEW.action IS NULL OR substring(NEW.action from 1 for 4) = '\x7980dd47'::bytea))
    EXECUTE FUNCTION bot_private_update();
