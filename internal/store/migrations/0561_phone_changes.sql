-- A number change in progress (account.sendChangePhoneCode, then
-- account.changePhone). There is no SMS here: the code goes to the
-- account's own apps as a service message, so it proves the session, and
-- the number is only checked for being free. One per account, the newest
-- request wins.
CREATE TABLE phone_changes (
    user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    phone text NOT NULL,             -- ggenc:v1:..., GCM context "phone-change:<user>"
    hash text NOT NULL,              -- phone_code_hash the app sends back
    code_hash text NOT NULL,
    attempts int NOT NULL DEFAULT 0,
    expires_at timestamptz NOT NULL
);
