package whatsapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCallbackBatchAcknowledgementAndIndependentLanes(t *testing.T) {
	connstr := os.Getenv("RELAY_TEST_DATABASE_URL")
	if connstr == "" {
		t.Skip("requires database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, connstr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	schema := fmt.Sprintf("relay_callback_test_%d", time.Now().UnixNano())
	if _, err = c.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer c.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	if _, err = c.Exec(ctx, "SET search_path TO "+schema); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"0002_whatsapp_events.sql", "0004_delivery_status.sql", "0005_core_callbacks.sql", "0006_incoming_messages.sql", "0026_status_callback_offset.sql"} {
		raw, err := os.ReadFile("../database/migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = c.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	scoped := connstr + " search_path=" + schema
	if strings.HasPrefix(connstr, "postgres") {
		u, _ := url.Parse(connstr)
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		scoped = u.String()
	}
	fail := true
	calls := 0
	batchSizes := []int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte(strings.Repeat("s", 64)))
		mac.Write([]byte(r.Header.Get("X-Relay-Timestamp") + "."))
		mac.Write(raw)
		if !hmac.Equal([]byte(r.Header.Get("X-Relay-Signature")), []byte(hex.EncodeToString(mac.Sum(nil)))) {
			t.Error("invalid callback signature")
		}
		if r.Header.Get("Authorization") != "token api:secret" {
			t.Error("missing site authentication")
		}
		var payload struct {
			Events   []Event         `json:"events"`
			Statuses []CompactStatus `json:"statuses"`
		}
		if json.Unmarshal(raw, &payload) != nil {
			t.Error("invalid batch")
		}
		batchSizes = append(batchSizes, len(payload.Events)+len(payload.Statuses))
		if fail {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"message":{"accepted":%d}}`, len(payload.Events)+len(payload.Statuses))
	}))
	defer server.Close()
	a := testAccount()
	a.Site = server.URL
	a.CoreAPIKey = "api"
	a.CoreAPISecret = "secret"
	a.CallbackSecret = strings.Repeat("s", 64)
	svc, err := New([]Account{a})
	if err != nil {
		t.Fatal(err)
	}
	d := CallbackDispatcher{URL: scoped, Service: svc}
	store := PostgresEvents{URL: scoped}
	for i := 0; i < 20; i++ {
		body := []byte(fmt.Sprintf(`{"entry":[{"changes":[{"value":{"statuses":[{"id":"wamid.%d","status":"sent","recipient_id":"919999999999","timestamp":"100"}]}}]}]}`, i))
		if err = store.Save(ctx, a.ID, body); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.Save(ctx, a.ID, []byte(`{"entry":[{"changes":[{"value":{"messages":[{"id":"wamid.in","from":"919999999999","text":{"body":"Hello"}}]}}]}]}`)); err != nil {
		t.Fatal(err)
	}
	if err = d.deliver(ctx, a, "status"); err == nil {
		t.Fatal("failed callback acknowledged")
	}
	var cursor int64
	if err = c.QueryRow(ctx, "SELECT last_id FROM relay_callback_cursor WHERE lane='status'").Scan(&cursor); err != nil || cursor != 0 {
		t.Fatal("failed callback advanced cursor", cursor, err)
	}
	fail = false
	if err = d.deliver(ctx, a, "realtime"); err != nil {
		t.Fatal(err)
	}
	if batchSizes[1] != 1 {
		t.Fatal("status backlog blocked realtime")
	}
	if _, err = c.Exec(ctx, "UPDATE relay_callback_cursor SET next_attempt=now()"); err != nil {
		t.Fatal(err)
	}
	if err = d.deliver(ctx, a, "status"); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || batchSizes[0] != 20 || batchSizes[2] != 20 {
		t.Fatal("statuses not batched", batchSizes)
	}
	large := statusFixture(800, 1251)
	if err = store.Save(ctx, a.ID, large.Payload); err != nil {
		t.Fatal(err)
	}
	if err = d.deliver(ctx, a, "status"); err != nil {
		t.Fatal(err)
	}
	var partial int
	if err = c.QueryRow(ctx, "SELECT status_offset FROM relay_callback_cursor WHERE account=$1 AND lane='status'", a.ID).Scan(&partial); err != nil || partial != 500 {
		t.Fatal("partial cursor not persisted", partial, err)
	}
	fail = true
	if err = d.deliver(ctx, a, "status"); err == nil {
		t.Fatal("failed partial batch acknowledged")
	}
	if err = c.QueryRow(ctx, "SELECT status_offset FROM relay_callback_cursor WHERE account=$1 AND lane='status'", a.ID).Scan(&partial); err != nil || partial != 500 {
		t.Fatal("failed partial batch advanced", partial, err)
	}
	fail = false
	if _, err = c.Exec(ctx, "UPDATE relay_callback_cursor SET next_attempt=now()"); err != nil {
		t.Fatal(err)
	}
	restarted := CallbackDispatcher{URL: scoped, Service: svc}
	if err = restarted.deliver(ctx, a, "status"); err != nil {
		t.Fatal(err)
	}
	if err = restarted.deliver(ctx, a, "status"); err != nil {
		t.Fatal(err)
	}
	if err = c.QueryRow(ctx, "SELECT status_offset FROM relay_callback_cursor WHERE account=$1 AND lane='status'", a.ID).Scan(&partial); err != nil || partial != 0 {
		t.Fatal("completed cursor offset not cleared", partial, err)
	}
	if fmt.Sprint(batchSizes[3:]) != "[500 500 500 251]" {
		t.Fatal("partial batches/retry", batchSizes)
	}

	if err = d.deliver(ctx, a, "status"); err != nil {
		t.Fatal(err)
	}
	if calls != 7 {
		t.Fatal("acknowledged events replayed")
	}
	_, err = c.Exec(ctx, `INSERT INTO relay_whatsapp_incoming(account,provider_id,sender,payload) SELECT $1,'history-'||n,'919999999999',jsonb_build_object('from','919999999999','type','text','text',jsonb_build_object('body','Message '||n)) FROM generate_series(1,120) n`, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.History(ctx, a.ID, "919999999999", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page["messages"].([]IncomingMessage)) != 50 || page["has_more"] != true {
		t.Fatal("history not bounded", page)
	}
	older, err := store.History(ctx, a.ID, "919999999999", page["first"].(string), "")
	if err != nil || len(older["messages"].([]IncomingMessage)) != 50 {
		t.Fatal("reverse history failed", err)
	}
	fresh, err := store.History(ctx, a.ID, "919999999999", "", page["last"].(string))
	if err != nil || len(fresh["messages"].([]IncomingMessage)) != 0 {
		t.Fatal("forward cursor replayed rows", err)
	}
	foreign, err := store.History(ctx, "another-account", "919999999999", "", "")
	if err != nil || len(foreign["messages"].([]IncomingMessage)) != 0 {
		t.Fatal("history leaked accounts", err)
	}

}
