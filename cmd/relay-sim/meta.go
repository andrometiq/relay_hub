package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type fakeMeta struct {
	mu        sync.Mutex
	records   *recorder
	templates []map[string]any
	messages  int
	pageSize  int
}

func (m *fakeMeta) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		reply := []byte(`{"error":"body too large"}`)
		m.records.record(r, "graph", body, http.StatusRequestEntityTooLarge, reply, started, "body_too_large")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		w.Write(reply)
		return
	}
	status := 200
	var result any
	fault := m.records.fault("graph")
	m.mu.Lock()
	switch {
	case fault == "429" || fault == "503":
		status, _ = strconv.Atoi(fault)
		result = map[string]any{"error": map[string]any{"code": status, "is_transient": true, "message": "Simulation failure"}}
		w.Header().Set("Retry-After", "1")
	case r.Header.Get("Authorization") != "Bearer sim-provider-token":
		status = 401
		result = map[string]any{"error": "invalid fake provider token"}
	case r.Method == "POST" && r.URL.Path == "/v25.0/110/messages":
		m.messages++
		result = map[string]any{"messages": []any{map[string]any{"id": fmt.Sprintf("wamid.sim.%d.%d", m.records.start.UnixNano(), m.messages)}}}
	case r.Method == "POST" && r.URL.Path == "/v25.0/220/message_templates":
		var row map[string]any
		if json.Unmarshal(body, &row) != nil || row["name"] == nil {
			status = 400
			result = map[string]any{"error": "invalid template"}
			break
		}
		row["id"] = fmt.Sprint(m.records.start.UnixMicro() + int64(len(m.templates)))
		row["status"] = "PENDING"
		m.templates = append(m.templates, row)
		result = map[string]any{"id": row["id"], "status": "PENDING", "category": row["category"]}
	case r.Method == "GET" && r.URL.Path == "/v25.0/220/message_templates":
		filtered := []map[string]any{}
		for _, row := range m.templates {
			if name := r.URL.Query().Get("name"); name == "" || row["name"] == name {
				filtered = append(filtered, row)
			}
		}
		offset := 0
		if cursor := r.URL.Query().Get("after"); cursor != "" {
			offset, err = strconv.Atoi(cursor)
		}
		if err != nil || offset < 0 || offset > len(filtered) {
			status = 400
			result = map[string]any{"error": "invalid cursor"}
			break
		}
		limit := m.pageSize
		if limit < 1 {
			limit = 50
		}
		end := min(offset+limit, len(filtered))
		data := filtered[offset:end]
		page := map[string]any{"data": data}
		if end < len(filtered) {
			query := r.URL.Query()
			query.Set("after", fmt.Sprint(end))
			page["paging"] = map[string]any{"cursors": map[string]any{"after": fmt.Sprint(end)}, "next": "https://graph.facebook.com/v25.0/220/message_templates?" + query.Encode()}
		}
		result = page
	case r.Method == "GET" && r.URL.Path == "/v25.0/110":
		result = map[string]any{"id": "110", "display_phone_number": "919999999999", "verified_name": "Local simulation", "quality_rating": "GREEN"}
	default:
		status = 404
		result = map[string]any{"error": "unimplemented fake Meta route"}
	}
	reply, _ := json.Marshal(result)
	m.mu.Unlock()
	m.records.record(r, "graph", body, status, reply, started, fault)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(reply)
}
func (m *fakeMeta) approve(id, status string) (map[string]any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.templates {
		if t["id"] == id {
			t["status"] = status
			return map[string]any{"event": status, "message_template_id": id, "message_template_name": t["name"], "message_template_language": t["language"]}, true
		}
	}
	return nil, false
}
