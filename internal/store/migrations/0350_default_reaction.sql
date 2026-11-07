-- messages.setDefaultReaction: the quick reaction, a key as in message reactions.
ALTER TABLE account_prefs ADD COLUMN default_reaction text;
