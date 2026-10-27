-- Telegram and Email events and sends take the site of their channel when
-- they are written. A channel without a site leaves site_id NULL: the event
-- is kept for replay and is not sent to any site.
CREATE FUNCTION relay_channel_event_site() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.site_id IS NULL THEN
  SELECT c.site_id INTO NEW.site_id FROM relay_channel_credentials c WHERE c.channel=NEW.channel AND c.channel_key=NEW.account;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER relay_channel_events_site BEFORE INSERT ON relay_channel_events
 FOR EACH ROW EXECUTE FUNCTION relay_channel_event_site();

CREATE FUNCTION relay_channel_outbox_site() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.site_id IS NULL THEN
  SELECT c.site_id INTO NEW.site_id FROM relay_channel_credentials c
  WHERE c.channel_key=NEW.account AND c.channel=CASE WHEN NEW.operation LIKE 'telegram.%' THEN 'Telegram' ELSE 'Email' END;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER relay_channel_outbox_site BEFORE INSERT ON relay_channel_outbox
 FOR EACH ROW EXECUTE FUNCTION relay_channel_outbox_site();
