-- The site a bot or mailbox last left. Only that site gets the old key and its
-- history back on a reconnect; another site gets a new key. A channel removed
-- before this column has no known owner, so any reconnect gets a new key.
ALTER TABLE relay_channel_credentials ADD COLUMN IF NOT EXISTS removed_from text;
