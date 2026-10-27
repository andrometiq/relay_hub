ALTER TABLE relay_channel_credentials
 ADD COLUMN config_ciphertext bytea NOT NULL DEFAULT ''::bytea,
 ADD COLUMN config_nonce bytea NOT NULL DEFAULT ''::bytea;
