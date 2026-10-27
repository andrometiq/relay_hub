-- Forward targets get a copy of each verified provider webhook. The scope is
-- the container for WhatsApp and the bot or mailbox key for Telegram and Email.
CREATE TABLE relay_forward_targets (
 id bigserial PRIMARY KEY,
 provider text NOT NULL CHECK (provider IN ('WhatsApp','Telegram','Email')),
 scope_key text NOT NULL CHECK (scope_key ~ '^[A-Za-z0-9_-]{1,100}$'),
 url text NOT NULL CHECK (length(url) BETWEEN 8 AND 2048),
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (provider, scope_key, url)
);

-- One row per received request and target: the exact body and the provider
-- headers, and the result of the last try.
CREATE TABLE relay_forwards (
 id bigserial PRIMARY KEY,
 target_id bigint NOT NULL REFERENCES relay_forward_targets(id) ON DELETE CASCADE,
 headers jsonb NOT NULL DEFAULT '{}'::jsonb,
 body bytea NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','delivered','failed')),
 attempts integer NOT NULL DEFAULT 0,
 status_code integer NOT NULL DEFAULT 0,
 error text NOT NULL DEFAULT '',
 latency_ms integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 delivered_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX relay_forwards_due ON relay_forwards (next_attempt_at, id) WHERE state = 'pending';
CREATE INDEX relay_forwards_target ON relay_forwards (target_id, id DESC);
CREATE INDEX relay_forwards_created ON relay_forwards (created_at);
