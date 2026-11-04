-- An event of an unknown number, such as a WABA account_update during
-- Embedded Signup, has no channel key and no site, so no site can take it.
-- A replay journals it again as a new routed event, so the stored row needs no
-- callback. Site notices (no channel key, with a site) still queue.
CREATE OR REPLACE FUNCTION relay_whatsapp_callback_enqueue() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.account='' AND NEW.site_id IS NULL THEN
  RETURN NEW;
 END IF;
 IF NEW.lane='realtime' THEN
  INSERT INTO relay_whatsapp_callback_delivery(event_id,lane) VALUES(NEW.id,'realtime');
 END IF;
 IF NEW.lane='status' OR (NEW.lane<>'bulk' AND NEW.payload @? '$.entry[*].changes[*].value.statuses') THEN
  INSERT INTO relay_whatsapp_callback_delivery(event_id,lane) VALUES(NEW.id,'status');
 END IF;
 RETURN NEW;
END $$;

DELETE FROM relay_whatsapp_callback_delivery d USING relay_whatsapp_events e
 WHERE e.id=d.event_id AND e.account='' AND e.site_id IS NULL AND NOT d.acknowledged;
