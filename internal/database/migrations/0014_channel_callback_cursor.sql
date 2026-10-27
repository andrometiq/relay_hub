CREATE TABLE IF NOT EXISTS relay_channel_callback_cursor (
 account text NOT NULL,
 channel text NOT NULL CHECK(channel IN ('Telegram','Email')),
 last_id bigint NOT NULL DEFAULT 0,
 failures integer NOT NULL DEFAULT 0,
 next_attempt timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(account,channel)
);
