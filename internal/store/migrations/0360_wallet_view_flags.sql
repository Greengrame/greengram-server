-- Display choices the wallet keeps for someone between opens.
CREATE TABLE wallet_view_flags (
    user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    hide_balances boolean NOT NULL DEFAULT false
);
