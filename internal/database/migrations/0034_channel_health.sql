-- Historical reception time is unknown. Do not present migration time or the
-- provider's event date as fresh ingress; only new inserts get a Hub timestamp.
ALTER TABLE relay_channel_events ADD COLUMN received_at timestamptz;
ALTER TABLE relay_channel_events ALTER COLUMN received_at SET DEFAULT clock_timestamp();
CREATE INDEX relay_channel_health_inbound ON relay_channel_events
 (account,channel,received_at DESC NULLS LAST,id DESC) WHERE direction='inbound';
CREATE INDEX relay_channel_health_outbox ON relay_channel_outbox
 (account,(split_part(operation,'.',1)),id DESC)
 WHERE state IN ('queued','dispatching','uncertain','rejected');
