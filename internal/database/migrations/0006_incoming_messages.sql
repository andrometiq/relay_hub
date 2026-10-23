CREATE TABLE relay_whatsapp_incoming (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 account text NOT NULL,
 provider_id text NOT NULL,
 sender text NOT NULL,
 payload jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(account,provider_id)
);
CREATE INDEX relay_whatsapp_incoming_history ON relay_whatsapp_incoming(account,sender,id);
