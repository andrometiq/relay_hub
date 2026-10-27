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
	// ChannelKey and To name the number that received or sent the event.
	ChannelKey string `json:"channel_key,omitempty"`
	To         string `json:"to,omitempty"`
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

// Route names where one envelope belongs: the channel key, its site (empty
// while unassigned) and its container. An empty Account is an unknown number.
type Route struct {
	Account, Site, Container string
}

func (p PostgresEvents) Save(ctx context.Context, account string, body []byte) error {
	return p.SaveEnvelope(ctx, account, body, body)
}

// SaveEnvelope journals an envelope for a single-number setup, where the
// channel key, the site and the container share one id.
func (p PostgresEvents) SaveEnvelope(ctx context.Context, account string, body, original []byte) error {
	return p.SaveRouted(ctx, Route{Account: account, Site: account, Container: account}, body, original)
}

// SaveRouted journals one routed envelope. Statuses of earlier sends belong to
// the site that queued them, so a status-only event takes that site.
func (p PostgresEvents) SaveRouted(ctx context.Context, route Route, body, original []byte) error {
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
	account := route.Account
	if account == "" {
		_, e = tx.Exec(ctx, "INSERT INTO relay_whatsapp_events(account,digest,payload,lane,raw_payload,site_id,container_id) VALUES('',$1,$2,'realtime',$3,NULL,nullif($4,'')) ON CONFLICT(account,digest) DO NOTHING", hex.EncodeToString(digest[:]), string(body), string(original), route.Container)
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	statuses := extractStatuses(body)
	bulk, matched, e := bulkDeliveries(ctx, tx, account, statuses)
	if e != nil {
		return e
	}
	lane := eventLane(body)
	if lane == "status" && matched > 0 && matched == len(statuses) && matched == statusCount(body) {
		lane = "bulk"
	}
	site, e := statusSite(ctx, tx, account, route.Site, lane, statuses)
	if e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, "INSERT INTO relay_whatsapp_events(account,digest,payload,lane,raw_payload,site_id,container_id) VALUES($1,$2,$3,$4,$5,nullif($6,''),nullif($7,'')) ON CONFLICT(account,digest) DO NOTHING", account, hex.EncodeToString(digest[:]), string(body), lane, string(original), site, route.Container)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	for _, status := range statuses {
		_, e = tx.Exec(ctx, `INSERT INTO relay_whatsapp_status(account,provider_id,status,status_rank,provider_timestamp,error) VALUES($1,$2,$3,$4,$5,$6)
      ON CONFLICT(account,provider_id) DO UPDATE SET status=excluded.status,status_rank=excluded.status_rank,provider_timestamp=excluded.provider_timestamp,error=excluded.error,updated_at=now()
      WHERE excluded.status_rank>relay_whatsapp_status.status_rank OR (excluded.status_rank=relay_whatsapp_status.status_rank AND excluded.provider_timestamp>relay_whatsapp_status.provider_timestamp)`, account, status.ID, status.Status, status.Rank, status.Timestamp, status.Error)
		if e != nil {
			return e
		}
	}
	if len(bulk) > 0 {
		_, e = tx.Exec(ctx, `INSERT INTO relay_bulk_pending(outbox_id,account) SELECT DISTINCT unnest($2::bigint[]),$1 ORDER BY 1
      ON CONFLICT(outbox_id) DO UPDATE SET changed_at=clock_timestamp()`, account, bulk)
		if e != nil {
			return e
		}
	}
	for _, status := range statuses {
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

// statusSite returns the site of the sends a status-only envelope reports on,
// when they share one site; otherwise the channel's current site.
func statusSite(ctx context.Context, tx pgx.Tx, account, current, lane string, statuses []deliveryStatus) (string, error) {
	if lane == "realtime" || len(statuses) == 0 {
		return current, nil
	}
	tracking, providers := []int64{}, []string{}
	for _, status := range statuses {
		tracking = append(tracking, status.TrackingID)
		providers = append(providers, status.ID)
	}
	var sites []string
	err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(DISTINCT site_id) FILTER (WHERE site_id IS NOT NULL),'{}') FROM relay_outbox
 WHERE account=$1 AND (id=ANY($2) OR result->'messages'->0->>'id'=ANY($3))`, account, tracking, providers).Scan(&sites)
	if err != nil || len(sites) != 1 {
		return current, err
	}
	return sites[0], nil
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

// Webhook receives Meta webhooks for one container (/webhooks/whatsapp/{container})
// or, without an id, for any container. One signature check per app secret
// authenticates the request. Changes for a number of the container go to its
// channel key with its current site; changes for an unknown number are stored
// unrouted and still answered 200, so Meta does not retry or disable the webhook.
func (s *Service) Webhook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("container")
	if id == "" {
		id = r.PathValue("account")
	}
	accounts := s.AccountsSnapshot()
	if id != "" {
		accounts = webhookAccounts(accounts, id)
		if len(accounts) == 0 {
			http.NotFound(w, r)
			return
		}
	}
	if r.Method == "GET" {
		s.verifyWebhook(w, r, accounts)
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
	authenticated := signedBy(accounts, raw, r.Header.Get("X-Hub-Signature-256"))
	if len(authenticated) == 0 {
		http.Error(w, "invalid signature", 403)
		return
	}
	routed, unrouted, err := routeEnvelope(payload, authenticated)
	if err != nil {
		http.Error(w, "invalid event", 400)
		return
	}
	if s.Events == nil {
		http.Error(w, "event store unavailable", 503)
		return
	}
	byKey := map[string]Account{}
	for _, a := range authenticated {
		byKey[a.ID] = a
	}
	for key, envelope := range routed {
		a := byKey[key]
		if e = s.saveRouted(r.Context(), Route{Account: key, Site: a.SiteID, Container: a.ContainerID}, envelope, raw); e != nil {
			http.Error(w, "event persistence failed", 503)
			return
		}
	}
	if len(unrouted.Entry) > 0 {
		if e = s.saveRouted(r.Context(), Route{Container: authenticated[0].ContainerID}, unrouted, raw); e != nil {
			http.Error(w, "event persistence failed", 503)
			return
		}
	}
	for _, entry := range payload.Entry {
		if templateChange(entry) {
			_ = s.Metadata.MarkTemplatesStale(r.Context(), entry.ID)
		}
	}
	w.WriteHeader(200)
}

// webhookAccounts lists the numbers of the container behind a public webhook path.
func webhookAccounts(accounts []Account, path string) []Account {
	result := []Account{}
	for _, a := range accounts {
		normalizeAccount(&a)
		id := a.WebhookID
		if id == "" {
			id = a.ContainerID
		}
		if id == path || (a.LegacyWebhook && a.ContainerID == path) {
			result = append(result, a)
		}
	}
	return result
}

// templateChange reports a template status, quality or category webhook.
func templateChange(entry webhookEntry) bool {
	for _, change := range entry.Changes {
		if strings.Contains(change.Field, "template") {
			return true
		}
	}
	return false
}

func containerAccounts(accounts []Account, container string) []Account {
	result := []Account{}
	for _, a := range accounts {
		if a.ContainerID == container {
			result = append(result, a)
		}
	}
	return result
}

// signedBy returns the numbers whose app secret signed the raw body.
func signedBy(accounts []Account, raw []byte, signature string) []Account {
	result := []Account{}
	for _, a := range accounts {
		if a.AppSecret == "" {
			continue
		}
		mac := hmac.New(sha256.New, []byte(a.AppSecret))
		mac.Write(raw)
		if hmac.Equal([]byte("sha256="+hex.EncodeToString(mac.Sum(nil))), []byte(signature)) {
			result = append(result, a)
		}
	}
	return result
}

func (s *Service) verifyWebhook(w http.ResponseWriter, r *http.Request, accounts []Account) {
	token := r.URL.Query().Get("hub.verify_token")
	for _, a := range accounts {
		if a.VerifyToken != "" && r.URL.Query().Get("hub.mode") == "subscribe" && subtle.ConstantTimeCompare([]byte(token), []byte(a.VerifyToken)) == 1 {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(r.URL.Query().Get("hub.challenge")))
			return
		}
	}
	http.Error(w, "invalid verification", 403)
}

func (s *Service) saveRouted(ctx context.Context, route Route, envelope webhookEnvelope, raw []byte) error {
	body, _ := json.Marshal(envelope)
	var err error
	switch store := s.Events.(type) {
	case interface {
		SaveRouted(context.Context, Route, []byte, []byte) error
	}:
		err = store.SaveRouted(ctx, route, body, raw)
	case interface {
		SaveEnvelope(context.Context, string, []byte, []byte) error
	}:
		err = store.SaveEnvelope(ctx, route.Account, body, raw)
	default:
		err = s.Events.Save(ctx, route.Account, body)
	}
	if err == nil && route.Site != "" && eventLane(body) == "realtime" {
		select {
		case s.callbackWake <- struct{}{}:
		default:
		}
	}
	return err
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
	Error      []byte
}

// bulkDeliveries maps receipts to bulk outbox rows by tracking ID or provider
// message ID, and returns the rows plus how many receipts matched.
func bulkDeliveries(ctx context.Context, tx pgx.Tx, account string, statuses []deliveryStatus) ([]int64, int, error) {
	if len(statuses) == 0 {
		return nil, 0, nil
	}
	tracking, providers := []int64{}, []string{}
	for _, status := range statuses {
		if status.TrackingID > 0 {
			tracking = append(tracking, status.TrackingID)
		}
		providers = append(providers, status.ID)
	}
	rows, err := tx.Query(ctx, `SELECT id,coalesce(result->'messages'->0->>'id','') FROM relay_outbox
 WHERE account=$1 AND bulk AND (id=ANY($2) OR result->'messages'->0->>'id'=ANY($3))`, account, tracking, providers)
	if err != nil {
		return nil, 0, err
	}
	byID, byProvider := map[int64]bool{}, map[string]int64{}
	for rows.Next() {
		var id int64
		var provider string
		if err = rows.Scan(&id, &provider); err != nil {
			rows.Close()
			return nil, 0, err
		}
		byID[id] = true
		if provider != "" {
			byProvider[provider] = id
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	ids, matched := []int64{}, 0
	for _, status := range statuses {
		id := byProvider[status.ID]
		if byID[status.TrackingID] {
			id = status.TrackingID
		}
		if id > 0 {
			ids = append(ids, id)
			matched++
		}
	}
	return ids, matched, nil
}

func statusCount(raw []byte) int {
	var event struct {
		Entry []struct {
			Changes []struct {
				Value struct {
					Statuses []json.RawMessage `json:"statuses"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return -1
	}
	count := 0
	for _, entry := range event.Entry {
		for _, change := range entry.Changes {
			count += len(change.Value.Statuses)
		}
	}
	return count
}

type providerReceiptError struct {
	Code      int    `json:"code"`
	Title     string `json:"title"`
	Message   string `json:"message"`
	ErrorData struct {
		Details string `json:"details"`
	} `json:"error_data"`
}

// statusError keeps the first receipt error as code and bounded Meta text.
func statusError(list []providerReceiptError) []byte {
	if len(list) == 0 || list[0].Code == 0 {
		return nil
	}
	message := list[0].ErrorData.Details
	if message == "" {
		message = list[0].Message
	}
	raw, _ := json.Marshal(map[string]any{"code": list[0].Code, "title": safeText(list[0].Title), "message": safeText(message)})
	return raw
}

func extractStatuses(raw []byte) []deliveryStatus {
	var event struct {
		Entry []struct {
			Changes []struct {
				Value struct {
					Statuses []struct {
						ID        string                 `json:"id"`
						Status    string                 `json:"status"`
						Timestamp string                 `json:"timestamp"`
						Tracking  string                 `json:"biz_opaque_callback_data"`
						Errors    []providerReceiptError `json:"errors"`
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
				out = append(out, deliveryStatus{ID: s.ID, Status: s.Status, Rank: rank, Timestamp: timestamp, TrackingID: trackingID, Error: statusError(s.Errors)})
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

// routeEnvelope splits an envelope by WABA (entry.id) and phone number id.
// A change without phone metadata (template and account events) goes to every
// number of that WABA. Changes that match no number are returned as unrouted.
func routeEnvelope(envelope webhookEnvelope, accounts []Account) (map[string]webhookEnvelope, webhookEnvelope, error) {
	routed := map[string]webhookEnvelope{}
	unrouted := webhookEnvelope{Object: envelope.Object}
	for _, entry := range envelope.Entry {
		if len(entry.Changes) == 0 {
			return nil, unrouted, errors.New("empty changes")
		}
		for _, change := range entry.Changes {
			var metadata struct {
				PhoneID string `json:"phone_number_id"`
			}
			if raw := change.Value["metadata"]; len(raw) > 0 && json.Unmarshal(raw, &metadata) != nil {
				return nil, unrouted, errors.New("invalid metadata")
			}
			if metadata.PhoneID == "" {
				for _, key := range []string{"messages", "statuses", "calls"} {
					if _, ok := change.Value[key]; ok {
						return nil, unrouted, errors.New("phone metadata required")
					}
				}
			}
			matched := false
			for _, a := range accounts {
				if a.WABAID != entry.ID || (metadata.PhoneID != "" && a.PhoneID != metadata.PhoneID) {
					continue
				}
				matched = true
				target := routed[a.ID]
				target.Object = envelope.Object
				target.Entry = append(target.Entry, webhookEntry{ID: entry.ID, Changes: []webhookChange{change}})
				routed[a.ID] = target
			}
			if !matched {
				unrouted.Entry = append(unrouted.Entry, webhookEntry{ID: entry.ID, Changes: []webhookChange{change}})
			}
		}
	}
	return routed, unrouted, nil
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
