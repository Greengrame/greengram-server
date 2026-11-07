-- bots.Prune sweeps bot_updates by age across every bot.
CREATE INDEX IF NOT EXISTS bot_updates_age ON bot_updates(created_at);
