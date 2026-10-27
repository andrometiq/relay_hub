CREATE TABLE relay_account_send_rate (
 account text PRIMARY KEY,
 next_at timestamptz NOT NULL
);
