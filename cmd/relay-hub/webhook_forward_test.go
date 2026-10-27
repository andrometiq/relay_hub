package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/forward"
	"github.com/andrometiq/relay_hub/internal/whatsapp"
)

// TestWebhookForwardDoesNotDelayMeta runs the Meta webhook with the real
// journal and a slow forward target.
func TestWebhookForwardDoesNotDelayMeta(t *testing.T) {
	ctx, db := adminTestDatabase(t)
	if _, err := db.Exec(ctx, `INSERT INTO relay_containers(container_id) VALUES('main')`); err != nil {
		t.Fatal(err)
	}
	type delivery struct {
		body      []byte
		signature string
	}
	got := make(chan delivery, 4)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		time.Sleep(time.Second)
		got <- delivery{body, r.Header.Get("X-Hub-Signature-256")}
	}))
	defer slow.Close()
	store := forward.Store{DB: db, Wake: make(chan struct{}, 1)}
	if _, err := store.AddTarget(ctx, forward.Scope{Provider: "WhatsApp", Key: "main"}, slow.URL); err != nil {
		t.Fatal(err)
	}
	wa, err := whatsapp.New([]whatsapp.Account{{ID: "sales", ContainerID: "main", AppSecret: "app-secret", PhoneID: "111", WABAID: "222", Version: "v25.0", AccessToken: "token"}})
	if err != nil {
		t.Fatal(err)
	}
	wa.Events = whatsapp.PostgresEvents{Pool: db}
	wa.Forward = func(ctx context.Context, container string, header http.Header, body []byte) {
		store.Queue(ctx, forward.Scope{Provider: "WhatsApp", Key: container}, header, body)
	}
	workerCtx, stop := context.WithCancel(ctx)
	defer stop()
	go forward.Worker{DB: db, Wake: store.Wake, Client: forward.NewClient(), Tick: time.Second}.Run(workerCtx)

	sent := map[string]string{}
	for _, phone := range []string{"111", "999"} {
		body := `{"object":"whatsapp_business_account","entry":[{"id":"222","changes":[{"field":"messages","value":{"metadata":{"phone_number_id":"` + phone + `"},"messages":[{"id":"m` + phone + `","from":"91","type":"text","text":{"body":"hi"}}]}}]}]}`
		mac := hmac.New(sha256.New, []byte("app-secret"))
		mac.Write([]byte(body))
		signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		sent[body] = signature
		r := httptest.NewRequest("POST", "/webhooks/whatsapp/main", strings.NewReader(body))
		r.SetPathValue("container", "main")
		r.Header.Set("X-Hub-Signature-256", signature)
		w := httptest.NewRecorder()
		started := time.Now()
		wa.Webhook(w, r)
		if w.Code != 200 || time.Since(started) > 500*time.Millisecond {
			t.Fatalf("Meta answer %d after %s", w.Code, time.Since(started))
		}
	}
	for range sent {
		select {
		case d := <-got:
			if sent[string(d.body)] != d.signature {
				t.Fatal("forwarded body or signature differs")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("forward not delivered")
		}
	}
}
