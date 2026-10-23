CREATE TABLE relay_whatsapp_status (
 account text NOT NULL,
 provider_id text NOT NULL,
 status text NOT NULL,
 status_rank smallint NOT NULL,
 provider_timestamp bigint NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(account,provider_id)
);
