package whatsapp

import (
	"os"
	"strings"
)

// siteColumnsFile stands for the parts of 0038 that tests with a partial
// schema need: site columns and the outcome trigger that records the site.
const siteColumnsFile = "0038_site_columns"

func migrationSQL(file string) ([]byte, error) {
	if file != siteColumnsFile {
		return os.ReadFile("../database/migrations/" + file)
	}
	full, err := os.ReadFile("../database/migrations/0038_containers_sites.sql")
	if err != nil {
		return nil, err
	}
	text := string(full)
	start := strings.Index(text, "CREATE OR REPLACE FUNCTION relay_whatsapp_send_outcome()")
	end := strings.Index(text, "CREATE OR REPLACE FUNCTION relay_hub_invalidate()")
	return []byte(`ALTER TABLE relay_whatsapp_events ADD COLUMN IF NOT EXISTS site_id text, ADD COLUMN IF NOT EXISTS container_id text, ADD COLUMN IF NOT EXISTS replayed_at timestamptz;
ALTER TABLE relay_outbox ADD COLUMN IF NOT EXISTS site_id text;
CREATE TABLE IF NOT EXISTS relay_sites(site_id text PRIMARY KEY, last_callback_at timestamptz);
` + text[start:end]), nil
}
