package whatsapp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/andrometiq/relay_hub/internal/lifecycle"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sync/atomic"
	"time"
)

// MetadataSync only calls providers for accounts explicitly opting into sync.
type MetadataSync struct {
	DB      *pgxpool.Pool
	Service *Service
	cursor  atomic.Uint64
}

func (m *MetadataSync) Run(ctx context.Context) {
	lifecycle.Go(ctx, func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			m.Tick(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}
func (m *MetadataSync) Tick(ctx context.Context) {
	accounts := m.Service.AccountsSnapshot()
	if len(accounts) == 0 {
		return
	}
	start := int((m.cursor.Add(20) - 20) % uint64(len(accounts)))
	for i := 0; i < 20 && i < len(accounts); i++ {
		if ctx.Err() != nil {
			return
		}
		a := accounts[(start+i)%len(accounts)]
		if a.SyncMetadata {
			m.Sync(ctx, a)
		}
	}
}
func (m *MetadataSync) current(a Account) bool {
	for _, candidate := range m.Service.AccountsSnapshot() {
		if candidate == a {
			return true
		}
	}
	return false
}
func metadataSource(a Account) string {
	a.SyncMetadata = false
	raw, _ := json.Marshal(a)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
func (m *MetadataSync) Sync(ctx context.Context, a Account) error {
	normalizeAccount(&a)
	if !a.SyncMetadata || !m.current(a) {
		return nil
	}
	source := metadataSource(a)
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	result, err := m.DB.Exec(ctx, `INSERT INTO relay_whatsapp_metadata(account,source_hash,last_attempt,lease_until) VALUES($1,$2,now(),now()+interval '2 minutes') ON CONFLICT(account) DO UPDATE SET profile=CASE WHEN relay_whatsapp_metadata.source_hash<>$2 THEN '{}'::jsonb ELSE relay_whatsapp_metadata.profile END, templates=CASE WHEN relay_whatsapp_metadata.source_hash<>$2 THEN '[]'::jsonb ELSE relay_whatsapp_metadata.templates END, last_success=CASE WHEN relay_whatsapp_metadata.source_hash<>$2 THEN NULL ELSE relay_whatsapp_metadata.last_success END,source_hash=$2,last_attempt=now(),lease_until=now()+interval '2 minutes' WHERE relay_whatsapp_metadata.source_hash<>$2 OR (relay_whatsapp_metadata.next_attempt<=now() AND (relay_whatsapp_metadata.lease_until IS NULL OR relay_whatsapp_metadata.lease_until<=now()))`, a.ID, source)
	if err != nil {
		return errors.New("metadata storage unavailable")
	}
	if result.RowsAffected() == 0 {
		return nil
	}
	fail := func() error {
		_, _ = m.DB.Exec(ctx, `UPDATE relay_whatsapp_metadata SET last_error='Provider metadata could not be refreshed',next_attempt=now()+interval '5 minutes',lease_until=NULL WHERE account=$1 AND source_hash=$2`, a.ID, source)
		return errors.New("provider metadata could not be refreshed")
	}
	profile, err := m.Service.Execute(ctx, a, Input{Operation: "account.profile"})
	if err != nil {
		return fail()
	}
	safeProfile := map[string]any{}
	for _, key := range []string{"id", "display_phone_number", "verified_name", "quality_rating"} {
		if value, ok := profile[key].(string); ok && len(value) <= 1000 {
			safeProfile[key] = value
		}
	}
	templates := []map[string]any{}
	after := ""
	truncated := false
	if a.Templates {
		for page := 0; page < 20; page++ {
			if !m.current(a) {
				return fail()
			}
			data, err := m.Service.Execute(ctx, a, Input{Operation: "templates.list", After: after})
			if err != nil {
				return fail()
			}
			values, ok := data["data"].([]any)
			if !ok {
				return fail()
			}
			for _, value := range values {
				item, ok := value.(map[string]any)
				if !ok {
					return fail()
				}
				safe := map[string]any{}
				for _, key := range []string{"id", "name", "status", "language", "category", "components", "parameter_format"} {
					if field, ok := item[key]; ok {
						safe[key] = field
					}
				}
				templates = append(templates, safe)
				if len(templates) >= 1000 {
					truncated = true
					break
				}
			}
			raw, _ := json.Marshal(templates)
			if len(raw) > 1024*1024 {
				return fail()
			}
			paging, _ := data["paging"].(map[string]any)
			cursors, _ := paging["cursors"].(map[string]any)
			cursor, _ := cursors["after"].(string)
			more, _ := paging["has_more"].(bool)
			if !more || cursor == "" {
				break
			}
			if cursor == after {
				return fail()
			}
			after = cursor
			if truncated || page == 19 {
				truncated = true
				break
			}
		}
	}
	if !m.current(a) {
		return fail()
	}
	rawProfile, _ := json.Marshal(safeProfile)
	rawTemplates, _ := json.Marshal(templates)
	_, err = m.DB.Exec(ctx, `UPDATE relay_whatsapp_metadata SET profile=$2,templates=$3,truncated=$4,last_success=now(),last_error='',next_attempt=now()+interval '1 hour',lease_until=NULL WHERE account=$1 AND source_hash=$5`, a.ID, rawProfile, rawTemplates, truncated, source)
	if err != nil {
		return errors.New("could not store metadata refresh")
	}
	return nil
}
func (m *MetadataSync) Read(ctx context.Context, account string) (map[string]any, error) {
	var profile, templates []byte
	var lastAttempt, lastSuccess *time.Time
	var lastError, source string
	var truncated bool
	err := m.DB.QueryRow(ctx, `SELECT profile,templates,truncated,last_attempt,last_success,last_error,source_hash FROM relay_whatsapp_metadata WHERE account=$1`, account).Scan(&profile, &templates, &truncated, &lastAttempt, &lastSuccess, &lastError, &source)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]any{"status": "not_synced", "profile": map[string]any{}, "templates": []any{}}, nil
	}
	if err != nil {
		return nil, errors.New("metadata unavailable")
	}
	matched := false
	for _, a := range m.Service.AccountsSnapshot() {
		if a.ID == account && metadataSource(a) == source {
			matched = true
			break
		}
	}
	if !matched {
		return map[string]any{"status": "not_synced", "profile": map[string]any{}, "templates": []any{}}, nil
	}
	var p map[string]any
	var t []any
	if json.Unmarshal(profile, &p) != nil || json.Unmarshal(templates, &t) != nil {
		return nil, errors.New("stored metadata is invalid")
	}
	status := "ready"
	if lastError != "" {
		status = "failed"
	} else if lastSuccess == nil {
		status = "syncing"
	}
	return map[string]any{"status": status, "profile": p, "templates": t, "truncated": truncated, "last_attempt": lastAttempt, "last_success": lastSuccess, "last_error": lastError}, nil
}
