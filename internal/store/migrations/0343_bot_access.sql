-- Restrict bot usage. The old site kept a key of this name that nothing acted on:
-- a bot shouldn't lock people out because of a switch from back then.
UPDATE bots SET state = state #- '{settings,access}' WHERE state #> '{settings,access}' IS NOT NULL;

-- Every update a message makes passes here, whichever trigger or write path it
-- came from: a restricted bot doesn't hear from people outside its list. A post
-- in a channel is the channel talking, not someone using the bot.
CREATE FUNCTION bot_update_access() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    st jsonb;
    owner bigint;
    sender bigint;
BEGIN
    IF NEW.kind NOT IN ('message', 'edited_message') THEN
        RETURN NEW;
    END IF;
    SELECT state, owner_id INTO st, owner FROM bots WHERE user_id = NEW.bot_id;
    IF coalesce(st #>> '{settings,access}' IN ('true', '1'), false) IS NOT TRUE THEN
        RETURN NEW;
    END IF;
    IF NEW.channel THEN
        SELECT from_id INTO sender FROM channel_messages WHERE channel_id = NEW.chat_id AND id = NEW.msg_id AND NOT post;
        IF sender IS NULL THEN
            RETURN NEW;
        END IF;
    ELSE
        SELECT from_id INTO sender FROM messages WHERE owner_id = NEW.bot_id AND id = NEW.msg_id;
    END IF;
    IF sender = owner OR coalesce(st #> '{settings,access_users}' @> to_jsonb(sender), false) THEN
        RETURN NEW;
    END IF;
    RETURN NULL;
END $$;

CREATE TRIGGER bot_updates_access BEFORE INSERT ON bot_updates
    FOR EACH ROW EXECUTE FUNCTION bot_update_access();
