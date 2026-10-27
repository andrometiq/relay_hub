-- Stable bytewise prefix lookup, independent of database locale.
CREATE INDEX IF NOT EXISTS relay_accounts_option_prefix ON relay_accounts (account_id COLLATE "C");
CREATE INDEX IF NOT EXISTS relay_grants_option_prefix ON relay_user_accounts (user_id, account_id COLLATE "C");
