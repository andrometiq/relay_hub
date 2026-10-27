CREATE TABLE relay_outbox (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 account text NOT NULL,
 request_key text NOT NULL,
 payload jsonb NOT NULL,
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','submitting','submitted','uncertain','cancelled','rejected')),
 result jsonb,
 error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(account,request_key)
);
CREATE INDEX relay_outbox_pending ON relay_outbox(id) WHERE state='queued';
CREATE INDEX relay_outbox_account ON relay_outbox(account,id);

CREATE INDEX relay_outbox_account_pending ON relay_outbox(account,id) WHERE state IN ('queued','submitting');
