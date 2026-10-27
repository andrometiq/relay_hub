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
	if a.Templates {
		if err := m.syncTemplates(ctx, a); err != nil {
			return err
		}
	}
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
	if !m.current(a) {
		return fail()
	}
	rawProfile, _ := json.Marshal(safeProfile)
	_, err = m.DB.Exec(ctx, `UPDATE relay_whatsapp_metadata SET profile=$2,last_success=now(),last_error='',next_attempt=now()+interval '1 hour',lease_until=NULL WHERE account=$1 AND source_hash=$3`, a.ID, rawProfile, source)
	if err != nil {
		return errors.New("could not store metadata refresh")
	}
	return nil
}
func (m *MetadataSync) Read(ctx context.Context, account string) (map[string]any, error) {
	var profile []byte
	templates := []byte(`[]`)
	var lastAttempt, lastSuccess *time.Time
	var lastError, source string
	var truncated bool
	err := m.DB.QueryRow(ctx, `SELECT profile,last_attempt,last_success,last_error,source_hash FROM relay_whatsapp_metadata WHERE account=$1`, account).Scan(&profile, &lastAttempt, &lastSuccess, &lastError, &source)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]any{"status": "not_synced", "profile": map[string]any{}, "templates": []any{}}, nil
	}
	if err != nil {
		return nil, errors.New("metadata unavailable")
	}
	var number *Account
	for _, a := range m.Service.AccountsSnapshot() {
		if a.ID == account && metadataSource(a) == source {
			number = &a
			break
		}
	}
	if number == nil {
		return map[string]any{"status": "not_synced", "profile": map[string]any{}, "templates": []any{}}, nil
	}
	if number.Templates {
		var templateError string
		err = m.DB.QueryRow(ctx, `SELECT templates,truncated,last_error FROM relay_whatsapp_waba_templates WHERE waba_id=$1`, number.WABAID).Scan(&templates, &truncated, &templateError)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("metadata unavailable")
		}
		if lastError == "" {
			lastError = templateError
		}
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

// syncTemplates fetches the templates of the number's WABA once while the
// stored copy is stale. Other numbers of the WABA reuse the copy.
func (m *MetadataSync) syncTemplates(ctx context.Context, a Account) error {
	claimed, err := m.DB.Exec(ctx, `INSERT INTO relay_whatsapp_waba_templates(waba_id,lease_until) VALUES($1,now()+interval '2 minutes')
 ON CONFLICT(waba_id) DO UPDATE SET lease_until=excluded.lease_until
 WHERE relay_whatsapp_waba_templates.stale AND (relay_whatsapp_waba_templates.lease_until IS NULL OR relay_whatsapp_waba_templates.lease_until<=now())`, a.WABAID)
	if err != nil {
		return errors.New("metadata storage unavailable")
	}
	if claimed.RowsAffected() == 0 {
		return nil
	}
	templates, truncated, err := m.fetchTemplates(ctx, a)
	if err != nil {
		_, _ = m.DB.Exec(ctx, `UPDATE relay_whatsapp_waba_templates SET last_error='Templates could not be refreshed',lease_until=now()+interval '5 minutes',updated_at=now() WHERE waba_id=$1`, a.WABAID)
		return err
	}
	raw, _ := json.Marshal(templates)
	_, err = m.DB.Exec(ctx, `UPDATE relay_whatsapp_waba_templates SET templates=$2,truncated=$3,stale=false,last_success=now(),last_error='',lease_until=NULL,updated_at=now() WHERE waba_id=$1`, a.WABAID, raw, truncated)
	if err != nil {
		return errors.New("could not store template refresh")
	}
	return nil
}

func (m *MetadataSync) fetchTemplates(ctx context.Context, a Account) ([]map[string]any, bool, error) {
	failed := errors.New("provider metadata could not be refreshed")
	templates := []map[string]any{}
	after := ""
	for page := 0; page < 20; page++ {
		if !m.current(a) {
			return nil, false, failed
		}
		data, err := m.Service.Execute(ctx, a, Input{Operation: "templates.list", After: after})
		if err != nil {
			return nil, false, failed
		}
		values, ok := data["data"].([]any)
		if !ok {
			return nil, false, failed
		}
		for _, value := range values {
			item, ok := value.(map[string]any)
			if !ok {
				return nil, false, failed
			}
			safe := map[string]any{}
			for _, key := range []string{"id", "name", "status", "language", "category", "components", "parameter_format"} {
				if field, ok := item[key]; ok {
					safe[key] = field
				}
			}
			templates = append(templates, safe)
			if len(templates) >= 1000 {
				return templates, true, nil
			}
		}
		if raw, _ := json.Marshal(templates); len(raw) > 1024*1024 {
			return nil, false, failed
		}
		paging, _ := data["paging"].(map[string]any)
		cursors, _ := paging["cursors"].(map[string]any)
		cursor, _ := cursors["after"].(string)
		if more, _ := paging["has_more"].(bool); !more || cursor == "" {
			return templates, false, nil
		}
		if cursor == after {
			return nil, false, failed
		}
		after = cursor
	}
	return templates, true, nil
}

// MarkTemplatesStale makes the next sync of any number in the WABA fetch its templates again.
func (m *MetadataSync) MarkTemplatesStale(ctx context.Context, waba string) error {
	if m == nil || m.DB == nil || waba == "" {
		return nil
	}
	_, err := m.DB.Exec(ctx, `UPDATE relay_whatsapp_waba_templates SET stale=true,updated_at=now() WHERE waba_id=$1`, waba)
	return err
}
