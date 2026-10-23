CREATE TABLE relay_users (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 username text NOT NULL UNIQUE,
 password_salt text NOT NULL,
 password_hash text NOT NULL,
 role text NOT NULL CHECK(role IN ('admin','member')),
 enabled boolean NOT NULL DEFAULT true,
 auth_version bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE relay_user_accounts (
 user_id bigint NOT NULL REFERENCES relay_users(id),
 account_id text NOT NULL,
 PRIMARY KEY(user_id,account_id)
);
CREATE TABLE relay_api_tokens (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 user_id bigint NOT NULL REFERENCES relay_users(id),
 name text NOT NULL,
 token_hash text NOT NULL UNIQUE,
 account_id text NOT NULL,
 scopes text[] NOT NULL,
 expires_at timestamptz NOT NULL,
 revoked_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX relay_api_tokens_user ON relay_api_tokens(user_id,id);
CREATE TABLE relay_auth_sessions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 user_id bigint NOT NULL REFERENCES relay_users(id),
 token_hash text NOT NULL UNIQUE,
 auth_version bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 last_seen timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL,
 user_agent text NOT NULL
);
CREATE INDEX relay_auth_sessions_user ON relay_auth_sessions(user_id,id);
CREATE TABLE relay_auth_audit (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 actor_id bigint REFERENCES relay_users(id),
 action text NOT NULL,
 target_id bigint,
 created_at timestamptz NOT NULL DEFAULT now()
);
