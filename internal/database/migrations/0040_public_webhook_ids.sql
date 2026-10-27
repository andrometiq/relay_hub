-- Public webhook paths use random ids, never container or channel keys, so a
-- URL shows no business name and cannot be guessed. Rows that existed before
-- keep answering on their old key path (legacy_path) until the provider is
-- moved to the new URL; new rows never have one.
ALTER TABLE relay_containers
 ADD COLUMN public_id text NOT NULL DEFAULT replace(gen_random_uuid()::text,'-','') || replace(gen_random_uuid()::text,'-','')
  CHECK (public_id ~ '^[a-f0-9]{32,64}$'),
 ADD COLUMN legacy_path boolean NOT NULL DEFAULT false;
UPDATE relay_containers SET legacy_path = true;
ALTER TABLE relay_containers ADD CONSTRAINT relay_containers_public_id UNIQUE (public_id);

ALTER TABLE relay_channel_credentials
 ADD COLUMN public_id text NOT NULL DEFAULT replace(gen_random_uuid()::text,'-','') || replace(gen_random_uuid()::text,'-','')
  CHECK (public_id ~ '^[a-f0-9]{32,64}$'),
 ADD COLUMN legacy_path boolean NOT NULL DEFAULT false;
UPDATE relay_channel_credentials SET legacy_path = true;
ALTER TABLE relay_channel_credentials ADD CONSTRAINT relay_channel_credentials_public_id UNIQUE (public_id);
