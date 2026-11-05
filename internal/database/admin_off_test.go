package database

import "testing"

func TestAdminOffMigrationLeavesChannelsThatAreOffUnlocked(t *testing.T) {
	c, ctx := testConnection(t)
	if err := Migrate(ctx, c); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO relay_sites(site_id,origin,enabled) VALUES('site-a','https://a.test',true)`,
		`INSERT INTO relay_containers(container_id) VALUES('main')`,
		`INSERT INTO relay_channel_credentials(channel,channel_key,container_id,label,address,site_id,enabled,config) VALUES
 ('WhatsApp','off','main','Off','111','site-a',false,'{"phone_id":"111","waba_id":"222"}'),
 ('Telegram','bot','main','Bot','500001','site-a',false,'{"username":"a_bot"}'),
 ('Email','on','main','On','a@example.test','site-a',true,'{}')`,
		`ALTER TABLE relay_channel_credentials DROP COLUMN admin_off`,
	}
	for _, statement := range statements {
		if _, err := c.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	migration, err := Files.ReadFile("migrations/0050_channel_admin_off.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	var locked int
	if err = c.QueryRow(ctx, "SELECT count(*) FROM relay_channel_credentials WHERE admin_off").Scan(&locked); err != nil {
		t.Fatal(err)
	}
	if locked != 0 {
		t.Fatalf("the migration locked %d channels that were off before it ran", locked)
	}
}
