package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andrometiq/relay_hub/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type captureDuringStateRead struct{ records *recorder }

func (c captureDuringStateRead) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "FROM relay_whatsapp_callback_delivery") {
		// Reproduce a callback completing while the durable state is being read.
		r := httptest.NewRequest("POST", "/api/method/relay_core.inbound.receive_statuses", nil)
		c.records.record(r, "status", []byte(`{"statuses":[{}]}`), 200,
			[]byte(`{"message":{"accepted":1}}`), time.Now(), "")
	}
	return ctx
}

func (c captureDuringStateRead) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestStateIncludesCapturesCompletedDuringDatabaseRead(t *testing.T) {
	dsn := isolatedLauncherDatabase(t)
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	records := newRecorder()
	config.ConnConfig.Tracer = captureDuringStateRead{records}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = database.Migrate(ctx, conn.Conn())
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	sim := simulation{db: db, meta: &fakeMeta{records: records}, token: "local-test"}
	req := httptest.NewRequest("GET", "/_sim/state", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer local-test")
	response := httptest.NewRecorder()
	sim.control(response, req)
	var state struct {
		Records []capture `json:"records"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &state) != nil {
		t.Fatal("could not read simulation state", response.Code)
	}
	if len(state.Records) != 1 || state.Records[0].Kind != "status" {
		t.Fatal("durable callback state returned with an older capture snapshot")
	}
}
