-- One row per Hub HTTP request, kept without automatic deletion. It holds no
-- query string, body, credential or header other than a short user agent.
-- occurred_at is the request start; route is the matched mux pattern; site_id
-- and account are set when a handler resolved them; error is a short 5xx text.
CREATE TABLE IF NOT EXISTS relay_request_log (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 occurred_at timestamptz NOT NULL DEFAULT now(),
 method text NOT NULL,
 path text NOT NULL,
 route text,
 status integer NOT NULL,
 duration_ms real NOT NULL,
 request_bytes bigint NOT NULL DEFAULT 0,
 response_bytes bigint NOT NULL DEFAULT 0,
 remote_ip inet,
 user_agent text NOT NULL DEFAULT '',
 site_id text,
 account text,
 request_id text NOT NULL,
 error text
);
CREATE INDEX IF NOT EXISTS relay_request_log_time ON relay_request_log USING brin(occurred_at);
CREATE INDEX IF NOT EXISTS relay_request_log_status_time ON relay_request_log(status, occurred_at);
CREATE INDEX IF NOT EXISTS relay_request_log_site_time ON relay_request_log(site_id, occurred_at) WHERE site_id IS NOT NULL;
