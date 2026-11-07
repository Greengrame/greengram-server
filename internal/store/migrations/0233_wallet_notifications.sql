-- Which optional wallet notifications someone wants. Nothing sends these yet;
-- transactions and security messages aren't optional and aren't here.
CREATE TABLE wallet_notifications (
    user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    settings jsonb NOT NULL DEFAULT '{}'
);
