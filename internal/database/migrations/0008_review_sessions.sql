CREATE TABLE relay_review_sessions (
 token_hash text PRIMARY KEY,
 credential_version text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 last_seen timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL,
 user_agent text NOT NULL
);
CREATE INDEX relay_review_sessions_expiry ON relay_review_sessions(expires_at);
