-- Numbers added on the Hub page started with templates off, and the page had
-- no switch, so Core could not use templates. The Hub cannot tell which numbers
-- an operator turned off on purpose, so templates go on for every number.
-- This changes no Meta state. Hourly sync, groups and calls stay as they are.
WITH changed AS (
 UPDATE relay_channel_credentials SET config=config||'{"templates":true}'::jsonb,updated_at=now()
 WHERE channel='WhatsApp' AND config->'templates' IS DISTINCT FROM 'true'::jsonb
 RETURNING site_id
)
INSERT INTO relay_whatsapp_events(account,digest,payload,lane,site_id)
SELECT DISTINCT '','relay_channels:'||site_id||':'||txid_current(),'{"object":"relay_channels","changed":true}'::jsonb,'realtime',site_id
FROM changed WHERE site_id IS NOT NULL
ON CONFLICT(account,digest) DO NOTHING;
