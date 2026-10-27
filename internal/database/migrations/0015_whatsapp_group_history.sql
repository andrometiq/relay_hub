-- Separate group conversations from participant direct-message history while
-- retaining immutable provider payloads and existing sender data.
CREATE INDEX relay_whatsapp_incoming_conversation_idx ON relay_whatsapp_incoming
(account, (coalesce(nullif(payload->>'group_id',''),sender)), id DESC);
