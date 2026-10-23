ALTER TABLE relay_outbox ADD COLUMN resolution_note text NOT NULL DEFAULT '';
ALTER TABLE relay_outbox ADD COLUMN resolved_at timestamptz;
