-- Original payload remains in relay_channel_events. A quarantine record and
-- cursor movement must commit together before later events may be delivered.
CREATE TABLE IF NOT EXISTS relay_channel_callback_quarantine (
 event_id bigint PRIMARY KEY REFERENCES relay_channel_events(id),
 account text NOT NULL,
 channel text NOT NULL CHECK(channel IN ('Telegram','Email')),
 reason text NOT NULL,
 resolved_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS relay_channel_callback_quarantine_account
 ON relay_channel_callback_quarantine(account,channel,event_id DESC);
