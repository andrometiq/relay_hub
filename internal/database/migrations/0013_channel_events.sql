CREATE TABLE IF NOT EXISTS relay_channel_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 account text NOT NULL,
 channel text NOT NULL CHECK(channel IN ('Telegram','Email')),
 direction text NOT NULL CHECK(direction IN ('inbound','outbound')),
 provider_id text NOT NULL,
 sender text NOT NULL DEFAULT '',
 recipient text NOT NULL DEFAULT '',
 subject text NOT NULL DEFAULT '',
 body text NOT NULL DEFAULT '',
 occurred_at timestamptz NOT NULL DEFAULT now(),
 payload jsonb NOT NULL DEFAULT '{}'::jsonb,
 UNIQUE(channel,account,provider_id)
);
CREATE INDEX IF NOT EXISTS relay_channel_events_history ON relay_channel_events(account,channel,id);
CREATE INDEX IF NOT EXISTS relay_channel_events_time ON relay_channel_events(account,occurred_at DESC,id DESC);
