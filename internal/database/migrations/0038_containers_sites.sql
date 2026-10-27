-- A container holds provider apps and their channels (numbers, bots, mailboxes).
-- A site is one Frappe site. A channel belongs to at most one site.
-- Legacy rows keep one container, one site and one channel key per old account id.
CREATE TABLE relay_containers (
 id bigserial PRIMARY KEY,
 container_id text NOT NULL UNIQUE CHECK (container_id ~ '^[A-Za-z0-9_-]{1,100}$'),
 name text NOT NULL DEFAULT '' CHECK (length(name) <= 200),
 enabled boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE relay_container_providers (
 container_id text NOT NULL REFERENCES relay_containers(container_id) ON DELETE CASCADE,
 provider text NOT NULL CHECK (provider = 'WhatsApp'),
 config jsonb NOT NULL DEFAULT '{}'::jsonb,
 config_ciphertext bytea NOT NULL DEFAULT ''::bytea,
 config_nonce bytea NOT NULL DEFAULT ''::bytea,
 secret_ciphertext bytea NOT NULL DEFAULT ''::bytea,
 secret_nonce bytea NOT NULL DEFAULT ''::bytea,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (container_id, provider)
);

ALTER TABLE relay_accounts RENAME TO relay_sites;
ALTER TABLE relay_sites RENAME COLUMN account_id TO site_id;
ALTER TABLE relay_sites RENAME COLUMN site TO origin;
ALTER TABLE relay_sites RENAME CONSTRAINT relay_accounts_account_id_key TO relay_sites_site_id_key;
ALTER TABLE relay_sites RENAME CONSTRAINT relay_accounts_pkey TO relay_sites_pkey;
ALTER INDEX relay_accounts_option_prefix RENAME TO relay_sites_option_prefix;
ALTER TABLE relay_sites
 ADD COLUMN secret_ciphertext bytea NOT NULL DEFAULT ''::bytea,
 ADD COLUMN secret_nonce bytea NOT NULL DEFAULT ''::bytea;

ALTER TABLE relay_user_accounts RENAME TO relay_user_sites;
ALTER TABLE relay_user_sites RENAME COLUMN account_id TO site_id;
ALTER TABLE relay_api_tokens RENAME COLUMN account_id TO site_id;

INSERT INTO relay_containers(container_id, name) SELECT site_id, site_id FROM relay_sites ORDER BY id;

ALTER TABLE relay_channel_credentials
 ADD COLUMN container_id text REFERENCES relay_containers(container_id) ON DELETE CASCADE,
 ADD COLUMN channel_key text,
 ADD COLUMN label text NOT NULL DEFAULT '' CHECK (length(label) <= 200),
 ADD COLUMN address text,
 ADD COLUMN site_id text REFERENCES relay_sites(site_id) ON DELETE SET NULL,
 ADD COLUMN assigned_at timestamptz;
UPDATE relay_channel_credentials SET container_id = account_id, channel_key = account_id, site_id = account_id,
 assigned_at = now(), label = account_id,
 address = CASE channel WHEN 'WhatsApp' THEN nullif(config->>'phone_id', '') WHEN 'Email' THEN nullif(lower(config->>'from'), '') END;
ALTER TABLE relay_channel_credentials
 ALTER COLUMN container_id SET NOT NULL,
 ALTER COLUMN channel_key SET NOT NULL,
 DROP CONSTRAINT relay_channel_credentials_account_id_channel_key,
 DROP COLUMN account_id,
 ADD CONSTRAINT relay_channel_credentials_key_check CHECK (channel_key ~ '^[A-Za-z0-9_-]{1,100}$'),
 ADD CONSTRAINT relay_channel_credentials_channel_key UNIQUE (channel, channel_key),
 ADD CONSTRAINT relay_channel_credentials_address UNIQUE (channel, address);
CREATE INDEX relay_channel_credentials_site ON relay_channel_credentials(site_id, channel);
CREATE INDEX relay_channel_credentials_container ON relay_channel_credentials(container_id, channel);

-- The site is fixed when an event is journaled or a send is queued, so a
-- reassigned channel never sends old events to its new site. NULL is unassigned.
ALTER TABLE relay_whatsapp_events
 ADD COLUMN site_id text,
 ADD COLUMN container_id text,
 ADD COLUMN replayed_at timestamptz;
UPDATE relay_whatsapp_events SET site_id = account, container_id = account;
CREATE INDEX relay_whatsapp_events_unassigned ON relay_whatsapp_events(id) WHERE site_id IS NULL AND replayed_at IS NULL;
ALTER TABLE relay_channel_events ADD COLUMN site_id text, ADD COLUMN replayed_at timestamptz;
UPDATE relay_channel_events SET site_id = account;
CREATE INDEX relay_channel_events_unassigned ON relay_channel_events(id) WHERE site_id IS NULL AND replayed_at IS NULL;
ALTER TABLE relay_outbox ADD COLUMN site_id text;
UPDATE relay_outbox SET site_id = account;
CREATE INDEX relay_outbox_site ON relay_outbox(site_id, id);
ALTER TABLE relay_channel_outbox ADD COLUMN site_id text;
UPDATE relay_channel_outbox SET site_id = account;
CREATE INDEX relay_channel_outbox_site ON relay_channel_outbox(site_id, id);

CREATE OR REPLACE FUNCTION relay_whatsapp_send_outcome() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state IN ('submitted','rejected','uncertain','cancelled') AND NEW.state IS DISTINCT FROM OLD.state THEN
  IF NEW.bulk THEN
   INSERT INTO relay_bulk_pending(outbox_id,account) VALUES(NEW.id,NEW.account)
   ON CONFLICT(outbox_id) DO UPDATE SET changed_at=clock_timestamp();
   RETURN NEW;
  END IF;
  INSERT INTO relay_whatsapp_events(account,digest,payload,lane,site_id)
  VALUES(NEW.account, 'outbox:' || NEW.id || ':' || NEW.state || ':' || txid_current(),
   jsonb_build_object('object','relay_delivery','delivery',jsonb_build_object(
    'channel','WhatsApp','id',NEW.id::text,'request_key',NEW.request_key,
    'state',NEW.state,'outcome',CASE NEW.state WHEN 'submitted' THEN 'accepted' WHEN 'cancelled' THEN 'rejected' ELSE NEW.state END,
    'provider_message_id',NEW.result->'messages'->0->>'id','error',NEW.error,
    'provider_error',NEW.provider_error)), 'realtime', NEW.site_id);
 END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION relay_hub_invalidate() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE value jsonb; account_id text;
BEGIN
 value := to_jsonb(NEW);
 account_id := coalesce(value->>'account',value->>'account_id',value->>'channel_key',value->>'site_id',value->>'container_id');
 IF TG_TABLE_NAME='relay_channel_callback_delivery' THEN
  SELECT account INTO account_id FROM relay_channel_events WHERE id=NEW.event_id;
 ELSIF TG_TABLE_NAME='relay_whatsapp_callback_delivery' THEN
  SELECT account INTO account_id FROM relay_whatsapp_events WHERE id=NEW.event_id;
 END IF;
 IF account_id IS NOT NULL THEN
  PERFORM pg_notify('relay_hub_changes',json_build_object('schema',TG_TABLE_SCHEMA,'account',account_id)::text);
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER relay_live AFTER INSERT OR UPDATE ON relay_containers FOR EACH ROW EXECUTE FUNCTION relay_hub_invalidate();
