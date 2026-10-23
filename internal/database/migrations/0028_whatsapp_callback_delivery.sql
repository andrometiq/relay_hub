ALTER TABLE relay_whatsapp_events ADD COLUMN raw_payload jsonb;
-- Delivery state is keyed by immutable event, never by a high-water ID.
CREATE TABLE relay_whatsapp_callback_delivery (
 event_id bigint NOT NULL REFERENCES relay_whatsapp_events(id),
 lane text NOT NULL CHECK(lane IN ('realtime','status')),
 acknowledged boolean NOT NULL DEFAULT false,
 status_offset integer NOT NULL DEFAULT 0 CHECK(status_offset >= 0),
 failures integer NOT NULL DEFAULT 0,
 next_attempt timestamptz NOT NULL DEFAULT now(),
 error text NOT NULL DEFAULT '',
 error_position integer NOT NULL DEFAULT -1,
 quarantined boolean NOT NULL DEFAULT false,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(event_id,lane)
);
CREATE TABLE relay_whatsapp_callback_audit (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 event_id bigint NOT NULL REFERENCES relay_whatsapp_events(id),
 lane text NOT NULL,
 actor bigint NOT NULL,
 action text NOT NULL CHECK(action IN ('quarantine','replay')),
 note text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX relay_whatsapp_callback_pending ON relay_whatsapp_callback_delivery(lane,next_attempt,event_id)
 WHERE NOT acknowledged AND NOT quarantined;
CREATE FUNCTION relay_whatsapp_callback_enqueue() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.lane='realtime' THEN
  INSERT INTO relay_whatsapp_callback_delivery(event_id,lane) VALUES(NEW.id,'realtime');
 END IF;
 IF NEW.lane='status' OR NEW.payload @? '$.entry[*].changes[*].value.statuses' THEN
  INSERT INTO relay_whatsapp_callback_delivery(event_id,lane) VALUES(NEW.id,'status');
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER relay_whatsapp_callback_enqueue AFTER INSERT ON relay_whatsapp_events
 FOR EACH ROW EXECUTE FUNCTION relay_whatsapp_callback_enqueue();
-- Replay stable identities once on upgrade; a legacy ID cursor cannot prove
-- that an earlier transaction's event reached Core.
INSERT INTO relay_whatsapp_callback_delivery(event_id,lane)
 SELECT id,'realtime' FROM relay_whatsapp_events WHERE lane='realtime'
 UNION ALL
 SELECT id,'status' FROM relay_whatsapp_events
 WHERE lane='status' OR payload @? '$.entry[*].changes[*].value.statuses';
