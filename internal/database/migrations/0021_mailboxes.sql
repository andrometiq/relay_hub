CREATE TABLE relay_mailbox_cursor (
 account text NOT NULL, mailbox text NOT NULL,
 uid_validity bigint NOT NULL DEFAULT 0, last_uid bigint NOT NULL DEFAULT 0,
 last_sync timestamptz, last_error text NOT NULL DEFAULT '',
 PRIMARY KEY(account,mailbox)
);
CREATE TABLE relay_mailbox_rejections (
 account text NOT NULL, mailbox text NOT NULL, uid_validity bigint NOT NULL, uid bigint NOT NULL,
 reason text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(account,mailbox,uid_validity,uid)
);
CREATE TABLE relay_email_attachments (
 account text NOT NULL, provider_id text NOT NULL, slot integer NOT NULL CHECK(slot>=0 AND slot<8),
 filename text NOT NULL, content_type text NOT NULL, content bytea NOT NULL CHECK(octet_length(content)<=4194304),
 PRIMARY KEY(account,provider_id,slot)
);
