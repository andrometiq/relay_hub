CREATE TABLE relay_hub_metadata (
    key text PRIMARY KEY,
    value text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO relay_hub_metadata(key, value) VALUES ('application', 'relay_hub');
