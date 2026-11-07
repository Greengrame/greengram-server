-- The wallet's language, picked in its settings. Empty: follow the app that
-- opened it. A row can now exist for either setting, so neither is required.
ALTER TABLE wallet_prefs ALTER COLUMN local_currency SET DEFAULT '';
ALTER TABLE wallet_prefs ADD COLUMN language text NOT NULL DEFAULT '';
