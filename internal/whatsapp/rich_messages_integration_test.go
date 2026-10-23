package whatsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func richSimulationDatabase(t *testing.T, ctx context.Context) (*pgx.Conn, string) {
	t.Helper()
	dsn := os.Getenv("RELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	schema := fmt.Sprintf("relay_rich_message_test_%d", time.Now().UnixNano())
	if _, err = c.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, e := c.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); e != nil {
			t.Error(e)
		}
	})
	if _, err = c.Exec(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("../database/migrations/*.sql")
	if len(files) == 0 {
		t.Fatal("no migrations")
	}
	for _, file := range files {
		raw, e := os.ReadFile(file)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = c.Exec(ctx, string(raw)); e != nil {
			t.Fatal(file, e)
		}
	}
	scoped := dsn + " search_path=" + schema
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, e := url.Parse(dsn)
		if e != nil {
			t.Fatal(e)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		scoped = u.String()
	}
	return c, scoped
}
func TestRichMessagesDurableDispatchAndIncomingSimulation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, dsn := richSimulationDatabase(t, ctx)
	a := testAccount()
	a.MessagesPerSecond = 1000
	b := a
	b.ID = "site-b"
	b.PhoneID = "112"
	b.WABAID = "223"
	b.CoreToken = strings.Repeat("b", 64)
	b.AccessToken = "provider-b"
	submitted := map[string][]map[string]any{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner := ""
		for _, candidate := range []Account{a, b} {
			if r.URL.Path == "/v25.0/"+candidate.PhoneID+"/messages" && r.Header.Get("Authorization") == "Bearer "+candidate.AccessToken {
				owner = candidate.ID
			}
		}
		var body map[string]any
		if owner == "" || r.Method != "POST" || json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("outbox account/route/payload invalid")
			w.WriteHeader(400)
			return
		}
		submitted[owner] = append(submitted[owner], body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"messages":[{"id":"wamid.%s.%d"}]}`, owner, len(submitted[owner]))
	}))
	defer provider.Close()
	service, _ := New([]Account{a, b})
	service.base = provider.URL
	outbox := &Outbox{URL: dsn, Service: service}
	batch := []Submission{
		{Key: "location", Message: Input{Operation: "messages.location", To: "919999999999", Location: &Location{Latitude: coordinate(13.08), Longitude: coordinate(80.27), Name: "Warehouse"}, ReplyTo: "wamid.quoted"}},
		{Key: "contact", Message: Input{Operation: "messages.contacts", To: "919999999999", Contacts: []ContactCard{richCard()}, ReplyTo: "wamid.quoted"}},
		{Key: "sticker", Message: Input{Operation: "messages.media", To: "919999999999", MediaType: "sticker", MediaID: "123", ReplyTo: "wamid.quoted"}},
		{Key: "reaction-set", Message: Input{Operation: "messages.react", To: "919999999999", ReplyTo: "wamid.target", Emoji: "👍"}},
		{Key: "reaction-clear", Message: Input{Operation: "messages.react", To: "919999999999", ReplyTo: "wamid.target", Emoji: ""}},
	}
	first, err := outbox.Enqueue(ctx, a, batch)
	if err != nil {
		t.Fatal(err)
	}
	again, err := outbox.Enqueue(ctx, a, batch)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatal("rich replay duplicated", err)
	}
	other, err := outbox.Enqueue(ctx, b, batch)
	if err != nil || other[0].ID == first[0].ID {
		t.Fatal("account idempotency collided", err)
	}
	changed := append([]Submission{}, batch...)
	changed[0].Message.Location = &Location{Latitude: coordinate(0), Longitude: coordinate(0)}
	if _, err = outbox.Enqueue(ctx, a, changed); err == nil {
		t.Fatal("rich payload conflict accepted")
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if err = outbox.process(ctx); err != nil {
			t.Fatal(err)
		}
		var queued int
		if err = db.QueryRow(ctx, "SELECT count(*) FROM relay_outbox WHERE state IN ('queued','submitting')").Scan(&queued); err != nil {
			t.Fatal(err)
		}
		if queued == 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	for _, account := range []Account{a, b} {
		rows := submitted[account.ID]
		if len(rows) != 5 {
			t.Fatalf("duplicate or missing rich message count %d", len(rows))
		}
		for i, kind := range []string{"location", "contacts", "sticker", "reaction", "reaction"} {
			body := rows[i]
			if body["type"] != kind || body["to"] != "919999999999" || body["biz_opaque_callback_data"] == "" {
				t.Fatalf("rich message metadata lost %#v", body)
			}
			if i < 3 && body["context"].(map[string]any)["message_id"] != "wamid.quoted" {
				t.Fatal("quote context lost")
			}
		}
		if rows[0]["location"].(map[string]any)["latitude"] != 13.08 {
			t.Fatal("coordinates changed")
		}
		var expected any
		raw, _ := json.Marshal([]ContactCard{richCard()})
		json.Unmarshal(raw, &expected)
		if !reflect.DeepEqual(rows[1]["contacts"], expected) {
			t.Fatal("contact fields changed")
		}
		if rows[4]["reaction"].(map[string]any)["emoji"] != "" {
			t.Fatal("reaction removal became new reaction")
		}
	}
	store := PostgresEvents{URL: dsn}
	incoming := []map[string]any{
		{"id": "wamid.in-sticker", "from": "919999999999", "type": "sticker", "sticker": map[string]any{"id": "123", "mime_type": "image/webp", "animated": true}, "context": map[string]any{"id": "wamid.quoted"}},
		{"id": "wamid.in-location", "from": "919999999999", "type": "location", "location": map[string]any{"latitude": 13.08, "longitude": 80.27, "name": "Warehouse", "address": "Chennai"}},
		{"id": "wamid.in-contacts", "from": "919999999999", "type": "contacts", "contacts": []any{map[string]any{"name": map[string]any{"formatted_name": "தமிழ் Support"}, "phones": []any{map[string]any{"phone": "919999999999"}}}}},
		{"id": "wamid.in-reaction", "from": "919999999999", "type": "reaction", "reaction": map[string]any{"message_id": "wamid.target", "emoji": "👍"}},
		{"id": "wamid.in-clear", "from": "919999999999", "type": "reaction", "reaction": map[string]any{"message_id": "wamid.target"}},
	}
	for _, message := range incoming {
		raw, _ := json.Marshal(map[string]any{"entry": []any{map[string]any{"changes": []any{map[string]any{"value": map[string]any{"messages": []any{message}}}}}}})
		for i := 0; i < 2; i++ {
			if err = store.Save(ctx, a.ID, raw); err != nil {
				t.Fatal(err)
			}
		}
	}
	history, err := store.History(ctx, a.ID, "919999999999", "", "")
	if err != nil {
		t.Fatal(err)
	}
	rows := history["messages"].([]IncomingMessage)
	if len(rows) != 5 {
		t.Fatal("incoming duplicate or missing message")
	}
	for i, row := range rows {
		var got map[string]any
		json.Unmarshal(row.Payload, &got)
		if !reflect.DeepEqual(got, incoming[i]) {
			t.Fatalf("incoming metadata %d changed %#v", i, got)
		}
	}
	for _, tc := range []struct {
		account, id string
		want        bool
	}{{a.ID, "123", true}, {b.ID, "123", false}, {a.ID, "456", false}} {
		known, err := store.MediaKnown(ctx, tc.account, tc.id)
		if err != nil || known != tc.want {
			t.Fatal("media ownership scope failed", err)
		}
	}
	// Merely putting a sticker-shaped object on a different message type is not
	// proof that a verified sticker was received.
	forged := []byte(`{"entry":[{"changes":[{"value":{"messages":[{"id":"wamid.other","from":"919999999999","type":"text","sticker":{"id":"456"}}]}}]}]}`)
	if err = store.Save(ctx, a.ID, forged); err != nil {
		t.Fatal(err)
	}
	if known, e := store.MediaKnown(ctx, a.ID, "456"); e != nil || known {
		t.Fatal("nonsticker media authorized", e)
	}
}

func TestAttachmentOwnershipDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, dsn := richSimulationDatabase(t, ctx)
	_, err := db.Exec(ctx, `INSERT INTO relay_whatsapp_incoming(account,provider_id,sender,payload) VALUES
 ('site-a','image-event','123','{"type":"image","image":{"id":"100"}}'),
 ('site-a','audio-event','123','{"type":"audio","audio":{"id":"101"}}'),
 ('site-b','foreign-event','123','{"type":"document","document":{"id":"102"}}')`)
	if err != nil {
		t.Fatal(err)
	}
	store := PostgresEvents{URL: dsn}
	for _, tc := range []struct {
		account, id string
		want        bool
	}{{"site-a", "100", true}, {"site-a", "101", true}, {"site-a", "102", false}, {"site-b", "102", true}, {"site-a", "999", false}} {
		got, err := store.AttachmentKnown(ctx, tc.account, tc.id)
		if err != nil || got != tc.want {
			t.Fatalf("ownership %s/%s: %v, %v", tc.account, tc.id, got, err)
		}
	}
}
