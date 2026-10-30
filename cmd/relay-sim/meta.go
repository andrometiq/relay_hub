package main

import (
	"encoding/json"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/attachment"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// simulationNumbers maps each fake phone number ID to its WABA and display number.
var simulationNumbers = map[string]struct{ WABA, Display string }{
	"110": {"220", "919999999999"},
	"111": {"221", "919999999998"},
}

// graphNode splits a fake Graph path into its node and edge, and reports
// whether the node is a fake number (phone) or a fake WABA.
func graphNode(path string) (node, edge string, phone, waba bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/v25.0/"), "/")
	if len(parts) > 2 || !strings.HasPrefix(path, "/v25.0/") {
		return "", "", false, false
	}
	node = parts[0]
	if len(parts) == 2 {
		edge = parts[1]
	}
	_, phone = simulationNumbers[node]
	for _, number := range simulationNumbers {
		waba = waba || number.WABA == node
	}
	return node, edge, phone, waba
}

type fakeMeta struct {
	mu        sync.Mutex
	records   *recorder
	templates []map[string]any
	messages  int
	media     int
	pageSize  int
	bulk      *bulkReceipts
}

func (m *fakeMeta) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	limit := int64(2 << 20)
	if strings.HasSuffix(r.URL.Path, "/media") {
		limit = attachment.MaxBytes + 1<<20 // A media upload carries one attachment in a multipart body.
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
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
	node, edge, phone, waba := graphNode(r.URL.Path)
	fault := m.records.fault("graph")
	captured := true
	recorded := body
	m.mu.Lock()
	switch {
	case fault == "429" || fault == "503":
		status, _ = strconv.Atoi(fault)
		result = map[string]any{"error": map[string]any{"code": status, "is_transient": true, "message": "Simulation failure"}}
		w.Header().Set("Retry-After", "1")
	case r.Header.Get("Authorization") != "Bearer sim-provider-token":
		status = 401
		result = map[string]any{"error": "invalid fake provider token"}
	case r.Method == "POST" && phone && edge == "messages":
		m.messages++
		id := providerID("wamid.sim")
		result = map[string]any{"messages": []any{map[string]any{"id": id}}}
		var send struct {
			To       string `json:"to"`
			Type     string `json:"type"`
			Tracking string `json:"biz_opaque_callback_data"`
		}
		// Campaign sends are counted by bulkReceipts instead of captured one by one.
		captured = json.Unmarshal(body, &send) != nil || send.Type != "template" || !m.bulk.accept(node, send.To, id, send.Tracking)
	case r.Method == "POST" && phone && edge == "media":
		// Captures keep the upload size, not the file bytes.
		m.media++
		recorded, _ = json.Marshal(map[string]any{"media_upload_bytes": len(body), "content_type": r.Header.Get("Content-Type")})
		result = map[string]any{"id": fmt.Sprint(m.records.start.UnixMicro() + int64(m.media))}
	case r.Method == "POST" && waba && edge == "message_templates":
		var row map[string]any
		if json.Unmarshal(body, &row) != nil || row["name"] == nil {
			status = 400
			result = map[string]any{"error": "invalid template"}
			break
		}
		if problem := templateButtonProblem(row["components"]); problem != "" {
			status, result = 400, templateRejection(problem)
			break
		}
		row["id"] = fmt.Sprint(m.records.start.UnixMicro() + int64(len(m.templates)))
		row["status"] = "PENDING"
		row["quality_score"] = map[string]any{"score": "UNKNOWN"}
		m.templates = append(m.templates, row)
		result = map[string]any{"id": row["id"], "status": "PENDING", "category": row["category"]}
	case r.Method == "GET" && waba && edge == "message_templates":
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
			page["paging"] = map[string]any{"cursors": map[string]any{"after": fmt.Sprint(end)}, "next": "https://graph.facebook.com/v25.0/" + node + "/message_templates?" + query.Encode()}
		}
		result = page
	case r.Method == "POST" && edge == "" && m.template(node) != nil:
		var change map[string]any
		if json.Unmarshal(body, &change) != nil || change["components"] == nil {
			status, result = 400, map[string]any{"error": "invalid template"}
			break
		}
		if problem := templateButtonProblem(change["components"]); problem != "" {
			status, result = 400, templateRejection(problem)
			break
		}
		row := m.template(node)
		row["components"], row["status"] = change["components"], "PENDING"
		result = map[string]any{"success": true}
	case r.Method == "GET" && phone && edge == "":
		result = map[string]any{"id": node, "display_phone_number": simulationNumbers[node].Display, "verified_name": "Local simulation", "quality_rating": "GREEN",
			"whatsapp_business_manager_messaging_limit": "TIER_100K", "throughput": map[string]any{"level": "STANDARD"}, "status": "CONNECTED", "name_status": "APPROVED"}
	case r.Method == "GET" && waba && edge == "":
		result = map[string]any{"id": node, "name": "Local simulation", "account_review_status": "APPROVED", "business_verification_status": "verified", "whatsapp_business_manager_messaging_limit": "TIER_100K"}
	default:
		status = 404
		result = map[string]any{"error": "unimplemented fake Meta route"}
	}
	reply, _ := json.Marshal(result)
	m.mu.Unlock()
	if captured {
		m.records.record(r, "graph", recorded, status, reply, started, fault)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(reply)
}

// template finds a stored template by ID; callers hold m.mu.
func (m *fakeMeta) template(id string) map[string]any {
	for _, row := range m.templates {
		if row["id"] == id {
			return row
		}
	}
	return nil
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
