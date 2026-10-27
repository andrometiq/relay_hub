-- Read paths used by session cleanup, delivery workers and audit pagination.
-- These are additive and safe to apply to an existing Hub database.
CREATE INDEX IF NOT EXISTS relay_auth_sessions_expiry ON relay_auth_sessions(expires_at, last_seen);
CREATE INDEX IF NOT EXISTS relay_api_tokens_expiry ON relay_api_tokens(expires_at) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS relay_outbox_updated_state ON relay_outbox(updated_at, id) WHERE state IN ('queued','submitting','uncertain');
CREATE INDEX IF NOT EXISTS relay_whatsapp_events_created ON relay_whatsapp_events(account, created_at, id);
CREATE INDEX IF NOT EXISTS relay_whatsapp_incoming_created ON relay_whatsapp_incoming(account, created_at, id);
CREATE INDEX IF NOT EXISTS relay_whatsapp_status_updated ON relay_whatsapp_status(account, updated_at, provider_id);
