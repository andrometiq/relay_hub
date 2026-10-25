-- Bulk sends report their latest state to Core in coalesced windows instead of
-- one realtime event per transition, so campaign volume cannot delay replies.
ALTER TABLE relay_outbox ADD COLUMN bulk boolean NOT NULL DEFAULT false;
ALTER TABLE relay_whatsapp_status ADD COLUMN error jsonb;
CREATE INDEX relay_outbox_bulk_message ON relay_outbox(account,(result->'messages'->0->>'id')) WHERE bulk;
CREATE TABLE relay_bulk_pending (
 outbox_id bigint PRIMARY KEY REFERENCES relay_outbox(id) ON DELETE CASCADE,
 account text NOT NULL,
 first_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 changed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX relay_bulk_pending_window ON relay_bulk_pending(account,first_at,outbox_id);

CREATE OR REPLACE FUNCTION relay_whatsapp_send_outcome() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state IN ('submitted','rejected','uncertain','cancelled') AND NEW.state IS DISTINCT FROM OLD.state THEN
  IF NEW.bulk THEN
   INSERT INTO relay_bulk_pending(outbox_id,account) VALUES(NEW.id,NEW.account)
   ON CONFLICT(outbox_id) DO UPDATE SET changed_at=clock_timestamp();
   RETURN NEW;
  END IF;
  INSERT INTO relay_whatsapp_events(account,digest,payload,lane)
  VALUES(NEW.account, 'outbox:' || NEW.id || ':' || NEW.state || ':' || txid_current(),
   jsonb_build_object('object','relay_delivery','delivery',jsonb_build_object(
    'channel','WhatsApp','id',NEW.id::text,'request_key',NEW.request_key,
    'state',NEW.state,'outcome',CASE NEW.state WHEN 'submitted' THEN 'accepted' WHEN 'cancelled' THEN 'rejected' ELSE NEW.state END,
    'provider_message_id',NEW.result->'messages'->0->>'id','error',NEW.error,
    'provider_error',NEW.provider_error)), 'realtime');
 END IF;
 RETURN NEW;
END $$;

-- Receipt-only events for bulk messages are journaled in the 'bulk' lane and
-- reach Core through relay_bulk_pending, not the per-event status lane.
CREATE OR REPLACE FUNCTION relay_whatsapp_callback_enqueue() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.lane='realtime' THEN
  INSERT INTO relay_whatsapp_callback_delivery(event_id,lane) VALUES(NEW.id,'realtime');
 END IF;
 IF NEW.lane='status' OR (NEW.lane<>'bulk' AND NEW.payload @? '$.entry[*].changes[*].value.statuses') THEN
  INSERT INTO relay_whatsapp_callback_delivery(event_id,lane) VALUES(NEW.id,'status');
 END IF;
 RETURN NEW;
END $$;
