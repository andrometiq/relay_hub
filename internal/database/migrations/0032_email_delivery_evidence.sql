CREATE TABLE relay_email_delivery_evidence (
 account text NOT NULL,
 message_id text NOT NULL,
 recipient text NOT NULL,
 state text NOT NULL CHECK(state IN ('delayed','bounced','delivered')),
 code text NOT NULL,
 rank integer NOT NULL CHECK(rank BETWEEN 1 AND 3),
 published_rank integer NOT NULL DEFAULT 0,
 occurred_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(account,message_id,recipient)
);
CREATE INDEX relay_email_evidence_pending ON relay_email_delivery_evidence(account)
 WHERE rank > published_rank;
CREATE INDEX relay_email_outbox_message ON relay_channel_outbox(account,(result->>'message_id'))
 WHERE operation='email.send';
