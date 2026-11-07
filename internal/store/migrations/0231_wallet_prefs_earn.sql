-- The wallet's display currency; money itself stays in USD and units.
CREATE TABLE wallet_prefs (
    user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    local_currency text NOT NULL
);

-- Coins parked in an Earn campaign, outside the main balance.
CREATE TABLE earn_accounts (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    campaign_id bigint NOT NULL,
    units numeric NOT NULL CHECK (units >= 0),
    PRIMARY KEY (user_id, campaign_id)
);
