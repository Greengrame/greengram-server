-- Account settings Telegram keeps that greengram-web never had. One row
-- per account that changed any of them; no row means Telegram's defaults.
CREATE TABLE account_prefs (
    user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    birthday_day smallint,
    birthday_month smallint,
    birthday_year smallint,             -- optional even with a birthday
    account_ttl_days int NOT NULL DEFAULT 365,
    authorization_ttl_days int NOT NULL DEFAULT 180,
    sensitive_enabled boolean NOT NULL DEFAULT false,
    contact_signup_silent boolean NOT NULL DEFAULT false,
    -- account.setGlobalPrivacySettings
    archive_new_peers boolean NOT NULL DEFAULT false,
    keep_archived_unmuted boolean NOT NULL DEFAULT false,
    keep_archived_folders boolean NOT NULL DEFAULT false,
    hide_read_marks boolean NOT NULL DEFAULT false,
    new_peers_require_premium boolean NOT NULL DEFAULT false,
    display_gifts_button boolean NOT NULL DEFAULT false,
    noncontact_paid_stars bigint NOT NULL DEFAULT 0
);
CREATE INDEX account_prefs_birthday ON account_prefs(birthday_month, birthday_day) WHERE birthday_month IS NOT NULL;

-- account.changeAuthorizationSettings, per sign-in
ALTER TABLE sessions
    ADD COLUMN calls_disabled boolean NOT NULL DEFAULT false,
    ADD COLUMN secret_chats_disabled boolean NOT NULL DEFAULT false;
