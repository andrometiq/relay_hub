-- A site that disconnects itself from the Hub is turned off and its tokens are
-- revoked. disconnected_at tells re-enrolment to turn it on again; a site the
-- Hub admin turned off stays off.
ALTER TABLE relay_sites ADD COLUMN IF NOT EXISTS disconnected_at timestamptz;
