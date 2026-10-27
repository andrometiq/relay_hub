-- Webhooks answer only on the random public_id path; readable key paths are gone.
ALTER TABLE relay_containers DROP COLUMN legacy_path;
ALTER TABLE relay_channel_credentials DROP COLUMN legacy_path;
