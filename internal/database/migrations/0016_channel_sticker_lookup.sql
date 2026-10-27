CREATE INDEX relay_channel_sticker_lookup_idx ON relay_channel_events
(account, (coalesce(payload#>>'{message,sticker,file_id}',payload#>>'{edited_message,sticker,file_id}')))
WHERE channel='Telegram' AND direction='inbound';
