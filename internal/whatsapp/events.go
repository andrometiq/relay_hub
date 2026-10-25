package whatsapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type Event struct {
	ID      int64           `json:"id"`
	Payload json.RawMessage `json:"payload"`
}
type EventStore interface {
	Save(context.Context, string, []byte) error
	List(context.Context, string, int64) ([]Event, error)
}

// PostgresEvents journals webhooks. Pool, when set, avoids a new database
// connection per webhook under receipt storms.
type PostgresEvents struct {
	URL  string
	Pool *pgxpool.Pool
}

func (p PostgresEvents) connect(ctx context.Context) (*pgx.Conn, func(), error) {
	if p.Pool != nil {
		conn, err := p.Pool.Acquire(ctx)
		if err != nil {
			return nil, nil, err
		}
		return conn.Conn(), conn.Release, nil
	}
	conn, err := pgx.Connect(ctx, p.URL)
	if err != nil {
		return nil, nil, err
	}
	return conn, func() { conn.Close(context.Background()) }, nil
}

func (p PostgresEvents) Save(ctx context.Context, account string, body []byte) error {
	return p.SaveEnvelope(ctx, account, body, body)
}
func (p PostgresEvents) SaveEnvelope(ctx context.Context, account string, body, original []byte) error {
	c, release, e := p.connect(ctx)
	if e != nil {
		return e
	}
	defer release()
	digest := sha256.Sum256(body)
	tx, e := c.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	_, e = tx.Exec(ctx, "INSERT INTO relay_whatsapp_events(account,digest,payload,lane,raw_payload) VALUES($1,$2,$3,$4,$5) ON CONFLICT(account,digest) DO NOTHING", account, hex.EncodeToString(digest[:]), string(body), eventLane(body), string(original))
	if e != nil {
		return e
	}
	for _, status := range extractStatuses(body) {
		_, e = tx.Exec(ctx, `INSERT INTO relay_whatsapp_status(account,provider_id,status,status_rank,provider_timestamp) VALUES($1,$2,$3,$4,$5)
      ON CONFLICT(account,provider_id) DO UPDATE SET status=excluded.status,status_rank=excluded.status_rank,provider_timestamp=excluded.provider_timestamp,updated_at=now()
      WHERE excluded.status_rank>relay_whatsapp_status.status_rank OR (excluded.status_rank=relay_whatsapp_status.status_rank AND excluded.provider_timestamp>relay_whatsapp_status.provider_timestamp)`, account, status.ID, status.Status, status.Rank, status.Timestamp)
		if e != nil {
			return e
		}
	}
	for _, status := range extractStatuses(body) {
		if status.TrackingID > 0 {
			_, e = tx.Exec(ctx, `UPDATE relay_outbox SET state='submitted',result=jsonb_build_object('messages',jsonb_build_array(jsonb_build_object('id',$3::text))),error='',updated_at=now() WHERE account=$1 AND id=$2 AND state IN ('submitting','uncertain')`, account, status.TrackingID, status.ID)
			if e != nil {
				return e
			}
		}
	}
	for _, message := range extractIncoming(body) {
		raw, _ := json.Marshal(message)
		_, e = tx.Exec(ctx, "INSERT INTO relay_whatsapp_incoming(account,provider_id,sender,payload) VALUES($1,$2,$3,$4) ON CONFLICT(account,provider_id) DO NOTHING", account, message["id"], message["from"], string(raw))
		if e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func (p PostgresEvents) List(ctx context.Context, account string, after int64) ([]Event, error) {
	c, release, e := p.connect(ctx)
	if e != nil {
		return nil, e
	}
	defer release()
	rows, e := c.Query(ctx, "WITH page AS (SELECT id,payload FROM relay_whatsapp_events WHERE account=$1 AND id>$2 ORDER BY id LIMIT 50), sized AS (SELECT id,payload,sum(octet_length(payload::text)) OVER (ORDER BY id) AS bytes,row_number() OVER (ORDER BY id) AS position FROM page) SELECT id,payload FROM sized WHERE bytes<=524288 OR position=1 ORDER BY id", account, after)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var event Event
		if e = rows.Scan(&event.ID, &event.Payload); e != nil {
			return nil, e
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
func (s *Service) Webhook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("account")
	accounts := s.AccountsSnapshot()
	findByID := func() (Account, bool) {
		for _, account := range accounts {
			if account.ID == id {
				return account, true
			}
		}
		return Account{}, false
	}
	legacyAccount, legacyFound := findByID()
	if id != "" && !legacyFound {
		http.Error(w, "webhook unauthorized", http.StatusForbidden)
		return
	}
	if r.Method == "GET" {
		var a Account
		found := false
		if id != "" {
			a, found = legacyAccount, legacyFound
		} else {
			token := r.URL.Query().Get("hub.verify_token")
			for _, account := range accounts {
				if account.VerifyToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(account.VerifyToken)) == 1 {
					a, found = account, true
					break
				}
			}
		}
		if !found || a.VerifyToken == "" || r.URL.Query().Get("hub.mode") != "subscribe" || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("hub.verify_token")), []byte(a.VerifyToken)) != 1 {
			http.Error(w, "invalid verification", 403)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(r.URL.Query().Get("hub.challenge")))
		return
	}
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	select {
	case s.webhookSlots <- struct{}{}:
		defer func() { <-s.webhookSlots }()
	default:
		http.Error(w, "webhook capacity reached; retry", http.StatusServiceUnavailable)
		return
	}
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if e != nil {
		http.Error(w, "invalid body", 413)
		return
	}
	var payload webhookEnvelope
	if json.Unmarshal(raw, &payload) != nil || payload.Object != "whatsapp_business_account" || len(payload.Entry) == 0 {
		http.Error(w, "invalid event", 400)
		return
	}
	if id != "" {
		accounts = []Account{legacyAccount}
	}
	// Authenticate each active destination against the original bytes.
	authenticated := make([]Account, 0, len(accounts))
	for _, a := range accounts {
		mac := hmac.New(sha256.New, []byte(a.AppSecret))
		mac.Write(raw)
		expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if a.AppSecret != "" && hmac.Equal([]byte(expected), []byte(r.Header.Get("X-Hub-Signature-256"))) {
			authenticated = append(authenticated, a)
		}
	}
	routed, err := routeEnvelope(payload, authenticated)
	if err != nil {
		http.Error(w, "account mismatch or invalid signature", 403)
		return
	}
	if s.Events == nil {
		http.Error(w, "event store unavailable", 503)
		return
	}
	for account, envelope := range routed {
		body, _ := json.Marshal(envelope)
		if store, ok := s.Events.(interface {
			SaveEnvelope(context.Context, string, []byte, []byte) error
		}); ok {
			e = store.SaveEnvelope(r.Context(), account, body, raw)
		} else {
			e = s.Events.Save(r.Context(), account, body)
		}
		if e != nil {
			http.Error(w, "event persistence failed", 503)
			return
		}
		if eventLane(body) == "realtime" {
			select {
			case s.callbackWake <- struct{}{}:
			default:
			}
		}
	}
	w.WriteHeader(200)
}
func (s *Service) events(ctx context.Context, a Account, after string) (map[string]any, error) {
	cursor := int64(0)
	var e error
	if after != "" {
		cursor, e = strconv.ParseInt(after, 10, 64)
		if e != nil || cursor < 0 {
			return nil, errors.New("invalid event cursor")
		}
	}
	if s.Events == nil {
		return nil, errors.New("event store unavailable")
	}
	events, e := s.Events.List(ctx, a.ID, cursor)
	if e != nil {
		return nil, errors.New("cannot read events")
	}
	next := cursor
	if len(events) > 0 {
		next = events[len(events)-1].ID
	}
	return map[string]any{"events": events, "next": strconv.FormatInt(next, 10)}, nil
}

// The raw event stays immutable; this projection tolerates duplicate and
// out-of-order callbacks without moving a read message back to sent.
type deliveryStatus struct {
	TrackingID int64
	ID, Status string
	Rank       int
	Timestamp  int64
}

func extractStatuses(raw []byte) []deliveryStatus {
	var event struct {
		Entry []struct {
			Changes []struct {
				Value struct {
					Statuses []struct {
						ID        string `json:"id"`
						Status    string `json:"status"`
						Timestamp string `json:"timestamp"`
						Tracking  string `json:"biz_opaque_callback_data"`
					} `json:"statuses"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return nil
	}
	out := []deliveryStatus{}
	ranks := map[string]int{"sent": 1, "failed": 2, "delivered": 3, "read": 4}
	for _, entry := range event.Entry {
		for _, change := range entry.Changes {
			for _, s := range change.Value.Statuses {
				timestamp, err := strconv.ParseInt(s.Timestamp, 10, 64)
				rank := ranks[s.Status]
				if rank == 0 || !callIdentifier.MatchString(s.ID) || err != nil || timestamp < 0 {
					continue
				}
				trackingID := int64(0)
				if strings.HasPrefix(s.Tracking, "relay-outbox:") {
					trackingID, _ = strconv.ParseInt(strings.TrimPrefix(s.Tracking, "relay-outbox:"), 10, 64)
				}
				out = append(out, deliveryStatus{ID: s.ID, Status: s.Status, Rank: rank, Timestamp: timestamp, TrackingID: trackingID})
			}
		}
	}
	return out
}

type webhookEnvelope struct {
	Object string         `json:"object"`
	Entry  []webhookEntry `json:"entry"`
}
type webhookEntry struct {
	ID      string          `json:"id"`
	Changes []webhookChange `json:"changes"`
}
type webhookChange struct {
	Field string                     `json:"field,omitempty"`
	Value map[string]json.RawMessage `json:"value"`
}

func routeEnvelope(envelope webhookEnvelope, accounts []Account) (map[string]webhookEnvelope, error) {
	routed := map[string]webhookEnvelope{}
	for _, entry := range envelope.Entry {
		if len(entry.Changes) == 0 {
			return nil, errors.New("empty changes")
		}
		for _, change := range entry.Changes {
			var metadata struct {
				PhoneID string `json:"phone_number_id"`
			}
			if raw := change.Value["metadata"]; len(raw) > 0 && json.Unmarshal(raw, &metadata) != nil {
				return nil, errors.New("invalid metadata")
			}
			if metadata.PhoneID == "" {
				for _, key := range []string{"messages", "statuses", "calls"} {
					if _, ok := change.Value[key]; ok {
						return nil, errors.New("phone metadata required")
					}
				}
			}
			// Disabled, unconfigured and unauthenticated destinations are omitted.
			for _, a := range accounts {
				if a.WABAID != entry.ID || (metadata.PhoneID != "" && a.PhoneID != metadata.PhoneID) {
					continue
				}
				target := routed[a.ID]
				target.Object = envelope.Object
				target.Entry = append(target.Entry, webhookEntry{ID: entry.ID, Changes: []webhookChange{change}})
				routed[a.ID] = target
			}
		}
	}
	if len(routed) == 0 {
		return nil, errors.New("no authenticated active webhook destination")
	}
	return routed, nil
}

func eventLane(body []byte) string {
	var event webhookEnvelope
	if json.Unmarshal(body, &event) != nil {
		return "realtime"
	}
	statuses, other := false, false
	for _, entry := range event.Entry {
		for _, change := range entry.Changes {
			for key := range change.Value {
				switch key {
				case "statuses":
					statuses = true
				case "metadata", "messaging_product", "contacts":
				default:
					other = true
				}
			}
		}
	}
	if statuses && !other {
		return "status"
	}
	return "realtime"
}

func realtimePayload(body []byte) []byte {
	var event webhookEnvelope
	if json.Unmarshal(body, &event) != nil || len(event.Entry) == 0 {
		return body
	}
	changed := false
	for i := range event.Entry {
		for j := range event.Entry[i].Changes {
			if _, ok := event.Entry[i].Changes[j].Value["statuses"]; ok {
				changed = true
			}
			delete(event.Entry[i].Changes[j].Value, "statuses")
		}
	}
	if !changed {
		return body
	}
	raw, _ := json.Marshal(event)
	return raw
}

func (p PostgresEvents) Latest(ctx context.Context, account string) ([]Event, error) {
	c, release, err := p.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := c.Query(ctx, `WITH page AS (SELECT id,payload FROM relay_whatsapp_events WHERE account=$1 ORDER BY id DESC LIMIT 50), sized AS (SELECT *,sum(octet_length(payload::text)) OVER(ORDER BY id DESC) bytes,row_number() OVER(ORDER BY id DESC) position FROM page) SELECT id,payload FROM sized WHERE bytes<=524288 OR position=1 ORDER BY id`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var event Event
		if err = rows.Scan(&event.ID, &event.Payload); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
func (s *Service) latestEvents(ctx context.Context, a Account) (map[string]any, error) {
	store, ok := s.Events.(interface {
		Latest(context.Context, string) ([]Event, error)
	})
	if !ok {
		return nil, errors.New("latest events unavailable")
	}
	events, err := store.Latest(ctx, a.ID)
	if err != nil {
		return nil, errors.New("cannot read latest events")
	}
	cursor := "0"
	if len(events) > 0 {
		cursor = strconv.FormatInt(events[len(events)-1].ID, 10)
	}
	return map[string]any{"events": events, "next": cursor}, nil
}

func extractIncoming(body []byte) []map[string]any {
	var event struct {
		Entry []struct {
			Changes []struct {
				Value struct {
					Messages []map[string]any `json:"messages"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}
	if json.Unmarshal(body, &event) != nil {
		return nil
	}
	out := []map[string]any{}
	for _, entry := range event.Entry {
		for _, change := range entry.Changes {
			for _, message := range change.Value.Messages {
				id, _ := message["id"].(string)
				sender, _ := message["from"].(string)
				if callIdentifier.MatchString(id) && digits.MatchString(sender) {
					out = append(out, message)
				}
			}
		}
	}
	return out
}
