ALTER TABLE relay_mailbox_cursor ADD COLUMN failures integer NOT NULL DEFAULT 0;
ALTER TABLE relay_mailbox_cursor ADD COLUMN next_attempt timestamptz NOT NULL DEFAULT now();
ALTER TABLE relay_mailbox_cursor ADD COLUMN reset_uid_next bigint NOT NULL DEFAULT 0;
CREATE TABLE relay_mailbox_messages (
 account text NOT NULL,
 mailbox text NOT NULL,
 uid_validity bigint NOT NULL,
 uid bigint NOT NULL,
 content_hash text NOT NULL,
 provider_id text NOT NULL,
 PRIMARY KEY(account,mailbox,uid_validity,uid),
 UNIQUE(account,mailbox,uid_validity,provider_id)
);
CREATE INDEX relay_mailbox_content ON relay_mailbox_messages(account,mailbox,content_hash);
-- Correlate legacy events before a server can change UIDVALIDITY. Their raw
-- hashes are unavailable, so compare persisted message content until rescanned.
INSERT INTO relay_mailbox_messages(account,mailbox,uid_validity,uid,content_hash,provider_id)
 SELECT account,split_part(provider_id,':',2),split_part(provider_id,':',3)::bigint,
 split_part(provider_id,':',4)::bigint,coalesce(payload->>'content_hash',''),provider_id
 FROM relay_channel_events WHERE channel='Email' AND direction='inbound'
 AND provider_id ~ '^imap:[^:]+:[0-9]{1,10}:[0-9]{1,10}$';
-- Revisit the current epoch to fill raw hashes without replaying events.
UPDATE relay_mailbox_cursor SET last_uid=0,next_attempt=now();
