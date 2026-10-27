ALTER TABLE relay_accounts ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled','error','pending'));
ALTER TABLE relay_accounts ADD COLUMN IF NOT EXISTS config_version bigint NOT NULL DEFAULT 1;
ALTER TABLE relay_accounts ADD COLUMN IF NOT EXISTS last_runtime_error text NOT NULL DEFAULT '';
ALTER TABLE relay_accounts ADD COLUMN IF NOT EXISTS last_runtime_sync timestamptz;
ALTER TABLE relay_channel_credentials ADD COLUMN IF NOT EXISTS credential_version bigint NOT NULL DEFAULT 1;
ALTER TABLE relay_channel_credentials ADD COLUMN IF NOT EXISTS last_verified_at timestamptz;
ALTER TABLE relay_channel_credentials ADD COLUMN IF NOT EXISTS last_error text NOT NULL DEFAULT '';
ALTER TABLE relay_auth_audit ADD COLUMN IF NOT EXISTS account_id text;
ALTER TABLE relay_auth_audit ADD COLUMN IF NOT EXISTS metadata jsonb NOT NULL DEFAULT '{}'::jsonb;
CREATE INDEX IF NOT EXISTS relay_auth_audit_created ON relay_auth_audit(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS relay_auth_audit_account ON relay_auth_audit(account_id, created_at DESC, id DESC);
CREATE TABLE IF NOT EXISTS relay_operational_audit (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 occurred_at timestamptz NOT NULL DEFAULT now(),
 actor_type text NOT NULL CHECK (actor_type IN ('user','api_token','system','provider','anonymous')),
 actor_id bigint,
 account_id text,
 channel text CHECK (channel IS NULL OR channel IN ('WhatsApp','Telegram','Email')),
 action text NOT NULL CHECK (length(action) BETWEEN 1 AND 120),
 outcome text NOT NULL CHECK (outcome IN ('accepted','completed','rejected','failed','uncertain')),
 request_id text,
 trace_id text,
 source_ip inet,
 user_agent text NOT NULL DEFAULT '',
 metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
 error_code text NOT NULL DEFAULT '',
 error_message text NOT NULL DEFAULT ''
);
CREATE INDEX relay_operational_audit_account_time ON relay_operational_audit(account_id, occurred_at DESC, id DESC);
CREATE INDEX relay_operational_audit_actor_time ON relay_operational_audit(actor_id, occurred_at DESC, id DESC);
CREATE INDEX relay_operational_audit_action_time ON relay_operational_audit(action, occurred_at DESC, id DESC);
CREATE INDEX relay_operational_audit_request ON relay_operational_audit(request_id) WHERE request_id IS NOT NULL;
CREATE INDEX relay_operational_audit_retention ON relay_operational_audit(occurred_at);
