ALTER TABLE relay_channel_events ADD COLUMN receipt boolean NOT NULL DEFAULT false;
ALTER TABLE relay_channel_callback_delivery ADD COLUMN replay boolean NOT NULL DEFAULT false;
-- Pending upgrade work retains its acknowledgement obligation but yields to fresh traffic.
UPDATE relay_channel_callback_delivery SET replay=true WHERE NOT acknowledged;
CREATE OR REPLACE FUNCTION relay_channel_callback_enqueue() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO relay_channel_callback_delivery(event_id,acknowledged) VALUES(NEW.id,NEW.receipt);
 RETURN NEW;
END $$;
CREATE INDEX relay_channel_callback_priority ON relay_channel_callback_delivery(replay,event_id)
 WHERE NOT acknowledged AND NOT quarantined;
