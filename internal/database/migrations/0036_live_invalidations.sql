CREATE FUNCTION relay_hub_invalidate() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE value jsonb; account_id text;
BEGIN
 value := to_jsonb(NEW);
 account_id := coalesce(value->>'account',value->>'account_id');
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
CREATE TRIGGER relay_live AFTER INSERT OR UPDATE ON relay_channel_events FOR EACH ROW EXECUTE FUNCTION relay_hub_invalidate();
CREATE TRIGGER relay_live AFTER INSERT OR UPDATE ON relay_channel_outbox FOR EACH ROW EXECUTE FUNCTION relay_hub_invalidate();
CREATE TRIGGER relay_live AFTER INSERT OR UPDATE ON relay_whatsapp_events FOR EACH ROW EXECUTE FUNCTION relay_hub_invalidate();
CREATE TRIGGER relay_live AFTER INSERT OR UPDATE ON relay_outbox FOR EACH ROW EXECUTE FUNCTION relay_hub_invalidate();
CREATE TRIGGER relay_live AFTER INSERT OR UPDATE ON relay_channel_callback_delivery FOR EACH ROW EXECUTE FUNCTION relay_hub_invalidate();
CREATE TRIGGER relay_live AFTER INSERT OR UPDATE ON relay_channel_credentials FOR EACH ROW EXECUTE FUNCTION relay_hub_invalidate();
CREATE TRIGGER relay_live AFTER INSERT OR UPDATE ON relay_accounts FOR EACH ROW EXECUTE FUNCTION relay_hub_invalidate();
CREATE TRIGGER relay_live AFTER INSERT OR UPDATE ON relay_whatsapp_callback_delivery FOR EACH ROW EXECUTE FUNCTION relay_hub_invalidate();
CREATE TRIGGER relay_live AFTER INSERT OR UPDATE ON relay_whatsapp_status FOR EACH ROW EXECUTE FUNCTION relay_hub_invalidate();
CREATE TRIGGER relay_live AFTER INSERT OR UPDATE ON relay_channel_status_journal FOR EACH ROW EXECUTE FUNCTION relay_hub_invalidate();
