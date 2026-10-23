CREATE INDEX relay_whatsapp_incoming_sticker_idx ON relay_whatsapp_incoming
(account, (payload->'sticker'->>'id')) WHERE payload->>'type'='sticker';
