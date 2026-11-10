CREATE TABLE local_wallet_cards (
    user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    address text NOT NULL UNIQUE,
    public boolean NOT NULL DEFAULT false
);
CREATE TABLE widget_wallet_transactions (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    request_id text NOT NULL,
    request_hash text NOT NULL,
    result jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(user_id,request_id)
);
