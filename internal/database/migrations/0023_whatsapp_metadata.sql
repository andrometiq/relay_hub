CREATE TABLE relay_whatsapp_metadata (
 account text PRIMARY KEY,
 profile jsonb NOT NULL DEFAULT '{}'::jsonb,
 templates jsonb NOT NULL DEFAULT '[]'::jsonb,
 truncated boolean NOT NULL DEFAULT false,
 last_attempt timestamptz,
 last_success timestamptz,
 next_attempt timestamptz NOT NULL DEFAULT now(),
 lease_until timestamptz,
 last_error text NOT NULL DEFAULT ''
);
CREATE INDEX relay_whatsapp_metadata_due ON relay_whatsapp_metadata(next_attempt);
