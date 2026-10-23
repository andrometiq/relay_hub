CREATE TABLE relay_outbox_account_claim (
 account text PRIMARY KEY,
 claimed_at timestamptz NOT NULL
);
ALTER TABLE relay_outbox ADD COLUMN next_attempt timestamptz NOT NULL DEFAULT now();
ALTER TABLE relay_outbox ADD COLUMN attempts integer NOT NULL DEFAULT 0;
ALTER TABLE relay_outbox ADD COLUMN provider_error jsonb;
ALTER TABLE relay_outbox ADD COLUMN submission_started boolean NOT NULL DEFAULT false;
-- Existing claims may already have reached Meta on the previous version.
UPDATE relay_outbox SET submission_started=true WHERE state='submitting';
CREATE INDEX relay_outbox_conversation_pending ON relay_outbox(account,(payload->>'to'),id)
 WHERE state IN ('queued','submitting','uncertain');

-- The transition and its callback journal commit together, including receipt
-- reconciliation, interrupted workers and operator resolution.
CREATE FUNCTION relay_whatsapp_send_outcome() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state IN ('submitted','rejected','uncertain','cancelled') AND NEW.state IS DISTINCT FROM OLD.state THEN
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
CREATE TRIGGER relay_whatsapp_send_outcome AFTER UPDATE ON relay_outbox
 FOR EACH ROW EXECUTE FUNCTION relay_whatsapp_send_outcome();
