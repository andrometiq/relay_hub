CREATE TABLE relay_channel_outbox (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 account text NOT NULL,
 request_key text NOT NULL,
 operation text NOT NULL CHECK(operation IN ('telegram.message','email.send')),
 payload jsonb NOT NULL,
 payload_hash text NOT NULL,
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','dispatching','submitted','rejected','uncertain','cancelled')),
 result jsonb,
 error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(account,request_key)
);
CREATE INDEX relay_channel_outbox_pending ON relay_channel_outbox(id) WHERE state='queued';
CREATE INDEX relay_channel_outbox_account ON relay_channel_outbox(account,id);
CREATE INDEX relay_channel_outbox_recovery ON relay_channel_outbox(updated_at) WHERE state='dispatching';
