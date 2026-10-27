-- Templates belong to a WhatsApp Business Account, so numbers that share one
-- WABA share one synced copy. A row is fetched again only when it is stale:
-- after a template webhook or a template change through the Hub.
CREATE TABLE relay_whatsapp_waba_templates (
 waba_id text PRIMARY KEY CHECK (waba_id ~ '^[0-9]{1,30}$'),
 templates jsonb NOT NULL DEFAULT '[]'::jsonb,
 truncated boolean NOT NULL DEFAULT false,
 stale boolean NOT NULL DEFAULT true,
 last_success timestamptz,
 last_error text NOT NULL DEFAULT '',
 lease_until timestamptz,
 updated_at timestamptz NOT NULL DEFAULT now()
);
