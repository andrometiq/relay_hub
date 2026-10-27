CREATE TABLE relay_channel_outbox_account_claim (
 account text PRIMARY KEY,
 claimed_at timestamptz NOT NULL
);
ALTER TABLE relay_channel_outbox ADD COLUMN next_attempt timestamptz NOT NULL DEFAULT now();
ALTER TABLE relay_channel_outbox ADD COLUMN claim_version bigint NOT NULL DEFAULT 0;
ALTER TABLE relay_channel_outbox ADD COLUMN attempts integer NOT NULL DEFAULT 0;
ALTER TABLE relay_channel_outbox ADD COLUMN provider_error jsonb;
ALTER TABLE relay_channel_outbox ADD COLUMN submission_started boolean NOT NULL DEFAULT false;
-- Previously claimed rows may have reached a provider.
UPDATE relay_channel_outbox SET submission_started=true WHERE state='dispatching';
-- New admissions store canonical envelope recipients parsed by net/mail.
-- Legacy email rows conservatively block the account until resolved; parsing
-- arbitrary RFC mailbox syntax in SQL could miss overlapping recipients.
ALTER TABLE relay_channel_outbox ADD COLUMN destinations text[] NOT NULL DEFAULT ARRAY['*'];
UPDATE relay_channel_outbox SET destinations=ARRAY['telegram:' || (payload->>'chat_id')]
 WHERE operation='telegram.message' AND payload->>'chat_id' IS NOT NULL;
CREATE INDEX relay_channel_outbox_destination_pending ON relay_channel_outbox(account,id)
 WHERE state IN ('queued','dispatching','uncertain');
