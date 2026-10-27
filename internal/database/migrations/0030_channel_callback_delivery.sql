-- Each committed event owns its acknowledgement. Sequence allocation is not
-- commit order, so the legacy cursor is no longer a delivery boundary.
CREATE TABLE relay_channel_callback_delivery (
 event_id bigint PRIMARY KEY REFERENCES relay_channel_events(id),
 acknowledged boolean NOT NULL DEFAULT false,
 failures integer NOT NULL DEFAULT 0,
 next_attempt timestamptz NOT NULL DEFAULT now(),
 error text NOT NULL DEFAULT '',
 quarantined boolean NOT NULL DEFAULT false,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX relay_channel_callback_pending ON relay_channel_callback_delivery(next_attempt,event_id)
 WHERE NOT acknowledged AND NOT quarantined;
CREATE TABLE relay_channel_callback_audit (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 event_id bigint NOT NULL REFERENCES relay_channel_events(id),
 actor bigint NOT NULL,
 action text NOT NULL CHECK(action IN ('quarantine','replay')),
 note text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE FUNCTION relay_channel_callback_enqueue() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO relay_channel_callback_delivery(event_id) VALUES(NEW.id);
 RETURN NEW;
END $$;
CREATE TRIGGER relay_channel_callback_enqueue AFTER INSERT ON relay_channel_events
 FOR EACH ROW EXECUTE FUNCTION relay_channel_callback_enqueue();
-- Replay stable identities on upgrade because a legacy cursor cannot prove ACK.
INSERT INTO relay_channel_callback_delivery(event_id,quarantined,error)
 SELECT e.id,q.event_id IS NOT NULL,coalesce(q.reason,'') FROM relay_channel_events e
 LEFT JOIN relay_channel_callback_quarantine q ON q.event_id=e.id AND q.resolved_at IS NULL;
