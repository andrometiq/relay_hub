package whatsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type siteCallback struct {
	Auth string
	Body struct {
		Account    string          `json:"account"`
		ChannelKey *string         `json:"channel_key"`
		Events     []Event         `json:"events"`
		Statuses   []CompactStatus `json:"statuses"`
	}
}

func TestCallbacksNameTheSiteAndChannel(t *testing.T) {
	ctx := context.Background()
	db, dsn := richSimulationDatabase(t, ctx)
	var mu sync.Mutex
	received := []siteCallback{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call siteCallback
		raw, _ := io.ReadAll(r.Body)
		if json.Unmarshal(raw, &call.Body) != nil {
			w.WriteHeader(400)
			return
		}
		call.Auth = r.Header.Get("Authorization")
		mu.Lock()
		received = append(received, call)
		mu.Unlock()
		fmt.Fprintf(w, `{"message":{"accepted":%d}}`, len(call.Body.Events)+len(call.Body.Statuses))
	}))
	defer server.Close()
	for _, site := range []string{"site-a", "site-b"} {
		if _, err := db.Exec(ctx, `INSERT INTO relay_sites(site_id,origin) VALUES($1,$2)`, site, "https://"+site+".invalid"); err != nil {
			t.Fatal(err)
		}
	}

	d, sales, support, _ := twoNumbers(t, dsn)
	sales.Site, sales.CoreAPIKey, sales.DisplayNumber = server.URL, "key-a", "+91 90000 00111"
	support.Site, support.CoreAPIKey, support.DisplayNumber = server.URL, "key-b", "+91 90000 00333"
	if err := d.Service.ReplaceAccounts([]Account{sales, support}); err != nil {
		t.Fatal(err)
	}
	status := `{"field":"messages","value":{"metadata":{"phone_number_id":"111"},"statuses":[{"id":"wamid.out","recipient_id":"919999999999","status":"delivered","timestamp":"1700000000"}]}}`
	body := `{"object":"whatsapp_business_account","entry":[{"id":"222","changes":[` + inbound("111", "wamid.sales") + `,` + inbound("333", "wamid.support") + `,` + status + `]}]}`
	if code := postWebhook(d.Service, "meta", "app-secret", body); code != 200 {
		t.Fatalf("webhook: %d", code)
	}
	if _, err := db.Exec(ctx, `INSERT INTO relay_whatsapp_events(account,digest,payload,lane,site_id)
 VALUES('','relay_channels:site-a:1','{"object":"relay_channels","changed":true}','realtime','site-a')`); err != nil {
		t.Fatal(err)
	}
	for _, a := range []Account{sales, support} {
		for _, lane := range []string{"realtime", "status"} {
			if err := d.deliver(ctx, a, lane); err != nil {
				t.Fatal(err)
			}
		}
	}
	d.SetSites([]Account{{SiteID: "site-a", Site: server.URL, CoreAPIKey: "key-a", CoreAPISecret: sales.CoreAPISecret, CallbackSecret: sales.CallbackSecret}})
	for _, site := range d.siteNotices() {
		if err := d.deliver(ctx, site, "realtime"); err != nil {
			t.Fatal(err)
		}
	}

	if len(received) != 4 {
		t.Fatalf("callbacks: %d", len(received))
	}
	seen := map[string]bool{}
	for _, call := range received {
		key := ""
		if call.Body.ChannelKey != nil {
			key = *call.Body.ChannelKey
		}
		want := map[string]string{"sales": "site-a", "support": "site-b", "": "site-a"}[key]
		if call.Body.Account != want || !strings.Contains(call.Auth, map[string]string{"site-a": "key-a", "site-b": "key-b"}[want]) {
			t.Fatalf("callback for %q went to %s with %s", key, call.Body.Account, call.Auth)
		}
		to := map[string]string{"sales": sales.DisplayNumber, "support": support.DisplayNumber}[key]
		for _, event := range call.Body.Events {
			if event.ChannelKey != key || event.To != to {
				t.Fatalf("event routing: %+v", event)
			}
			if key == "" && !strings.Contains(string(event.Payload), "relay_channels") {
				t.Fatal("site notice payload")
			}
		}
		for _, row := range call.Body.Statuses {
			if row.ChannelKey != "sales" || row.To != sales.DisplayNumber || row.MessageID != "wamid.out" {
				t.Fatalf("status routing: %+v", row)
			}
		}
		seen[fmt.Sprintf("%s/%d/%d", key, len(call.Body.Events), len(call.Body.Statuses))] = true
	}
	if !seen["sales/1/0"] || !seen["sales/0/1"] || !seen["support/1/0"] || !seen["/1/0"] {
		t.Fatalf("batches: %v", seen)
	}
	var touched int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM relay_sites WHERE last_callback_at IS NOT NULL`).Scan(&touched); err != nil || touched != 2 {
		t.Fatalf("last callback times: %d %v", touched, err)
	}
}
