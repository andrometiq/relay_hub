CREATE TABLE relay_whatsapp_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 account text NOT NULL,
 digest text NOT NULL,
 payload jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(account,digest)
);
CREATE INDEX relay_whatsapp_events_account_cursor ON relay_whatsapp_events(account,id);
