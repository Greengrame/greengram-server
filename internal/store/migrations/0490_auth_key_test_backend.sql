-- Whether the key's exchange named 10000+dc, as clients on Telegram's test
-- backend do (our Android build). help.getConfig's test_mode follows it, so
-- Web A doesn't show "[T]". NULL for keys made before this was kept.
ALTER TABLE auth_keys ADD COLUMN test_backend boolean;
