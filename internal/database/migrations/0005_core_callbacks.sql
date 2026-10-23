ALTER TABLE relay_whatsapp_events ADD COLUMN lane text NOT NULL DEFAULT 'realtime';
CREATE INDEX relay_whatsapp_events_lane ON relay_whatsapp_events(account,lane,id);
CREATE TABLE relay_callback_cursor (
 account text NOT NULL,
 lane text NOT NULL,
 last_id bigint NOT NULL DEFAULT 0,
 failures integer NOT NULL DEFAULT 0,
 next_attempt timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(account,lane)
);
