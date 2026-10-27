CREATE TABLE relay_accounts (
 id bigserial PRIMARY KEY,
 account_id text NOT NULL UNIQUE CHECK (account_id ~ '^[A-Za-z0-9_-]{1,100}$'),
 site text NOT NULL CHECK (length(site) BETWEEN 8 AND 300),
 enabled boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE relay_channel_credentials (
 id bigserial PRIMARY KEY,
 account_id text NOT NULL REFERENCES relay_accounts(account_id) ON DELETE CASCADE,
 channel text NOT NULL CHECK (channel IN ('WhatsApp','Telegram','Email')),
 enabled boolean NOT NULL DEFAULT false,
 config jsonb NOT NULL DEFAULT '{}'::jsonb,
 secret_ciphertext bytea NOT NULL DEFAULT ''::bytea,
 secret_nonce bytea NOT NULL DEFAULT ''::bytea,
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(account_id, channel)
);
CREATE INDEX relay_channel_credentials_account ON relay_channel_credentials(account_id, channel);
