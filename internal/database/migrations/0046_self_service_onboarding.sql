-- Self-service onboarding: a Frappe site links itself to the Hub with an
-- enrolment token, then connects WhatsApp numbers through Meta Embedded Signup.
CREATE TABLE relay_enrolment_tokens (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 name text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
 token_hash text NOT NULL UNIQUE,
 kind text NOT NULL CHECK (kind IN ('bench','setup_code')),
 origin_pattern text NOT NULL DEFAULT '' CHECK (length(origin_pattern) <= 300),
 max_sites int CHECK (max_sites IS NULL OR max_sites > 0),
 used_sites int NOT NULL DEFAULT 0 CHECK (used_sites >= 0),
 expires_at timestamptz NOT NULL,
 revoked_at timestamptz,
 created_by bigint REFERENCES relay_users(id),
 created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE relay_users
 ADD COLUMN kind text NOT NULL DEFAULT 'person' CHECK (kind IN ('person','service'));

ALTER TABLE relay_sites
 ADD COLUMN enrolled_by bigint REFERENCES relay_enrolment_tokens(id),
 ADD COLUMN enrolled_at timestamptz,
 ADD COLUMN service_user_id bigint REFERENCES relay_users(id);

-- Only one container hosts self-service numbers.
ALTER TABLE relay_containers ADD COLUMN signup boolean NOT NULL DEFAULT false;
CREATE UNIQUE INDEX relay_containers_signup ON relay_containers(signup) WHERE signup;

CREATE TABLE relay_signup_sessions (
 session_id text PRIMARY KEY CHECK (session_id ~ '^sus_[a-f0-9]{32}$'),
 site_id text NOT NULL REFERENCES relay_sites(site_id) ON DELETE CASCADE,
 container_id text NOT NULL REFERENCES relay_containers(container_id),
 flow text NOT NULL CHECK (flow IN ('new','coexistence','migrate')),
 flow_actual text CHECK (flow_actual IS NULL OR flow_actual IN ('new','coexistence','migrate')),
 state text NOT NULL DEFAULT 'created' CHECK (state IN ('created','waiting_for_meta','working','completed','cancelled','failed','expired','superseded')),
 steps jsonb NOT NULL DEFAULT '[]'::jsonb,
 launch_hash text NOT NULL,
 code_hash text,
 prefill jsonb NOT NULL DEFAULT '{}'::jsonb,
 meta_event jsonb,
 waba_id text,
 phone_id text,
 business_id text,
 channel_key text,
 phone jsonb,
 token_ciphertext bytea,
 token_nonce bytea,
 error jsonb,
 coexistence jsonb,
 payment_method_ready boolean,
 actor text NOT NULL DEFAULT '' CHECK (length(actor) <= 200),
 attempts int NOT NULL DEFAULT 0,
 next_attempt_at timestamptz,
 lease_until timestamptz,
 code_received_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL
);
CREATE INDEX relay_signup_sessions_site ON relay_signup_sessions(site_id, created_at DESC);
CREATE INDEX relay_signup_sessions_due ON relay_signup_sessions(next_attempt_at) WHERE state = 'working';
CREATE INDEX relay_signup_sessions_waba ON relay_signup_sessions(waba_id) WHERE waba_id IS NOT NULL;
