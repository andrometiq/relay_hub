-- Status callbacks are a pending-row queue, not a high-water ID cursor: an older
-- transaction may commit after a newer one has already been acknowledged.
CREATE TABLE IF NOT EXISTS relay_channel_status_journal (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 delivery_id bigint NOT NULL REFERENCES relay_channel_outbox(id),
 account text NOT NULL,
 request_key text NOT NULL,
 operation text NOT NULL,
 state text NOT NULL,
 receipt jsonb NOT NULL DEFAULT '{}'::jsonb,
 occurred_at timestamptz NOT NULL DEFAULT now(),
 acknowledged_at timestamptz
);
CREATE INDEX IF NOT EXISTS relay_channel_status_pending
 ON relay_channel_status_journal(account,id) WHERE acknowledged_at IS NULL;
CREATE OR REPLACE FUNCTION relay_journal_channel_status() RETURNS trigger AS $$
DECLARE compact jsonb;
BEGIN
 IF NEW.state NOT IN ('submitted','rejected','uncertain','cancelled') OR NEW.state IS NOT DISTINCT FROM OLD.state THEN
  RETURN NEW;
 END IF;
 -- Retain only receipt identifiers needed to reconcile local Core actions,
 -- never the outbound message body, attachments, recipient list or credentials.
 compact=jsonb_strip_nulls(jsonb_build_object(
  'message_id',NEW.result->'message_id', 'chat_id',NEW.result->'chat_id',
  'operation',NEW.result->'operation', 'sent',NEW.result->'sent',
  'result',CASE WHEN jsonb_typeof(NEW.result->'result')='boolean' THEN NEW.result->'result' ELSE NULL END,
  'manually_reconciled',NEW.result->'manually_reconciled'));
 IF octet_length(compact::text)>8192 THEN compact='{}'::jsonb; END IF;
 INSERT INTO relay_channel_status_journal(delivery_id,account,request_key,operation,state,receipt)
 VALUES(NEW.id,NEW.account,NEW.request_key,NEW.operation,NEW.state,compact);
 RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS relay_channel_status_changed ON relay_channel_outbox;
CREATE TRIGGER relay_channel_status_changed AFTER UPDATE OF state ON relay_channel_outbox
 FOR EACH ROW EXECUTE FUNCTION relay_journal_channel_status();
CREATE TABLE IF NOT EXISTS relay_channel_status_schedule (
 account text PRIMARY KEY,
 failures integer NOT NULL DEFAULT 0,
 next_attempt timestamptz NOT NULL DEFAULT now()
);
