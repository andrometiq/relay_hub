-- admin_off is a channel a Hub admin turned off. Site tools cannot turn it on
-- again; only an admin save that turns it on clears it. The Hub did not record
-- who turned off older channels, so every linked channel that is off now is
-- locked, except a number Meta removed, which a new signup restores.
ALTER TABLE relay_channel_credentials ADD COLUMN IF NOT EXISTS admin_off boolean NOT NULL DEFAULT false;
UPDATE relay_channel_credentials SET admin_off=true
 WHERE NOT enabled AND site_id IS NOT NULL AND coalesce(config->>'partner_status','')<>'removed';
