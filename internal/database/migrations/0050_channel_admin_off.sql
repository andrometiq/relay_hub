-- admin_off is a channel a Hub admin turned off. Site tools cannot turn it on
-- again; only an admin save that turns it on clears it. The Hub did not record
-- who turned off older channels, so channels that are off now stay unlocked:
-- the lock applies only to admin saves from now on.
ALTER TABLE relay_channel_credentials ADD COLUMN IF NOT EXISTS admin_off boolean NOT NULL DEFAULT false;
