package whatsapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// twoNumbers is one Meta app container with two numbers in one WABA, each on its own site.
func twoNumbers(t *testing.T, dsn string) (*CallbackDispatcher, Account, Account, *callbackRecorder) {
	t.Helper()
	d, sales, record := callbackFixture(t, dsn)
	sales.ID, sales.SiteID, sales.ContainerID, sales.AppSecret, sales.VerifyToken, sales.CoreToken = "sales", "site-a", "meta", "app-secret", "verify", ""
	support := sales
	support.ID, support.SiteID, support.PhoneID = "support", "site-b", "333"
	if err := d.Service.ReplaceAccounts([]Account{sales, support}); err != nil {
		t.Fatal(err)
	}
	d.Service.Events = PostgresEvents{URL: dsn}
	return d, sales, support, record
}

func postWebhook(s *Service, container, secret, body string) int {
	r := httptest.NewRequest("POST", "/webhooks/whatsapp/"+container, strings.NewReader(body))
	r.SetPathValue("container", container)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	s.Webhook(w, r)
	return w.Code
}

func inbound(phone, id string) string {
	return `{"field":"messages","value":{"metadata":{"phone_number_id":"` + phone + `"},"messages":[{"id":"` + id + `","from":"919999999999","type":"text","text":{"body":"hi"}}]}}`
}

func TestContainerWebhookRoutesEachNumberToItsSite(t *testing.T) {
	ctx := context.Background()
	db, dsn := richSimulationDatabase(t, ctx)
	d, sales, support, record := twoNumbers(t, dsn)
	body := `{"object":"whatsapp_business_account","entry":[{"id":"222","changes":[` + inbound("111", "wamid.sales") + `,` + inbound("333", "wamid.support") + `,` + inbound("999", "wamid.unknown") + `]}]}`
	if code := postWebhook(d.Service, "meta", "wrong-secret", body); code != 403 {
		t.Fatalf("bad signature: %d", code)
	}
	if code := postWebhook(d.Service, "other", "app-secret", body); code != 403 {
		t.Fatalf("unknown container: %d", code)
	}
	if code := postWebhook(d.Service, "meta", "app-secret", body); code != 200 {
		t.Fatalf("container webhook: %d", code)
	}
	rows, err := db.Query(ctx, `SELECT account,coalesce(site_id,''),coalesce(container_id,''),payload::text FROM relay_whatsapp_events ORDER BY account`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][3]string{}
	for rows.Next() {
		var account, site, container, payload string
		if err = rows.Scan(&account, &site, &container, &payload); err != nil {
			t.Fatal(err)
		}
		got[account] = [3]string{site, container, payload}
	}
	rows.Close()
	if len(got) != 3 || got["sales"][0] != "site-a" || got["support"][0] != "site-b" || got[""][0] != "" || got[""][1] != "meta" {
		t.Fatalf("routing: %#v", got)
	}
	if strings.Contains(got["sales"][2], "wamid.support") || !strings.Contains(got[""][2], "wamid.unknown") {
		t.Fatal("changes crossed numbers")
	}
	for _, a := range []Account{sales, support} {
		if err = d.deliver(ctx, a, "realtime"); err != nil {
			t.Fatal(err)
		}
	}
	if len(record.events) != 2 {
		t.Fatalf("callbacks: %d", len(record.events))
	}

	// Meta verification uses the container's verify token.
	r := httptest.NewRequest("GET", "/webhooks/whatsapp/meta?hub.mode=subscribe&hub.verify_token=verify&hub.challenge=42", nil)
	r.SetPathValue("container", "meta")
	w := httptest.NewRecorder()
	d.Service.Webhook(w, r)
	if w.Code != 200 || w.Body.String() != "42" {
		t.Fatalf("verification: %d %s", w.Code, w.Body.String())
	}
}

func TestReassignedNumberKeepsOldEventsAndReplaysUnknown(t *testing.T) {
	ctx := context.Background()
	db, dsn := richSimulationDatabase(t, ctx)
	d, sales, support, record := twoNumbers(t, dsn)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	envelope := func(change string) string {
		return `{"object":"whatsapp_business_account","entry":[{"id":"222","changes":[` + change + `]}]}`
	}
	if code := postWebhook(d.Service, "meta", "app-secret", envelope(inbound("111", "wamid.before"))); code != 200 {
		t.Fatal(code)
	}
	// The number is unassigned: its events are kept without a site.
	unassigned := sales
	unassigned.SiteID, unassigned.Site, unassigned.CoreAPIKey, unassigned.CoreAPISecret, unassigned.CallbackSecret = "", "", "", "", ""
	if err = d.Service.ReplaceAccounts([]Account{unassigned, support}); err != nil {
		t.Fatal(err)
	}
	if code := postWebhook(d.Service, "meta", "app-secret", envelope(inbound("111", "wamid.while-unassigned"))); code != 200 {
		t.Fatal(code)
	}
	if code := postWebhook(d.Service, "meta", "app-secret", envelope(inbound("444", "wamid.new-number"))); code != 200 {
		t.Fatal(code)
	}
	// Then it moves to site-b. Its old site-a event never goes to site-b.
	moved := sales
	moved.SiteID = "site-b"
	added := support
	added.ID, added.PhoneID = "added", "444"
	if err = d.Service.ReplaceAccounts([]Account{moved, support, added}); err != nil {
		t.Fatal(err)
	}
	if err = d.deliver(ctx, moved, "realtime"); err != nil {
		t.Fatal(err)
	}
	if len(record.events) != 0 {
		t.Fatalf("old or unassigned events leaked to the new site: %d", len(record.events))
	}
	var unassignedID, unknownID int64
	if err = db.QueryRow(ctx, `SELECT id FROM relay_whatsapp_events WHERE account='sales' AND site_id IS NULL`).Scan(&unassignedID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT id FROM relay_whatsapp_events WHERE account='' AND site_id IS NULL`).Scan(&unknownID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{unassignedID, unknownID} {
		if err = d.Service.ReplayUnassigned(ctx, pool, id); err != nil {
			t.Fatal(err)
		}
	}
	if err = d.Service.ReplayUnassigned(ctx, pool, unknownID); err == nil {
		t.Fatal("event replayed twice")
	}
	for _, a := range []Account{moved, added} {
		if err = d.deliver(ctx, a, "realtime"); err != nil {
			t.Fatal(err)
		}
	}
	joined := ""
	for _, event := range record.events {
		joined += string(event.Payload)
	}
	if len(record.events) != 2 || !strings.Contains(joined, "wamid.while-unassigned") || !strings.Contains(joined, "wamid.new-number") || strings.Contains(joined, "wamid.before") {
		t.Fatalf("replay delivered %d events: %s", len(record.events), joined)
	}
}
