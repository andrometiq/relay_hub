package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	relayemail "github.com/andrometiq/relay_hub/internal/email"
	"github.com/andrometiq/relay_hub/internal/telegram"
)

func localTelegram(t *testing.T, account string, handler http.HandlerFunc) *telegram.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	client, err := telegram.NewWithHTTPClient(telegram.Config{AccountID: account, BotToken: "123456:synthetic_fixture_token_never_used", Enabled: true}, &http.Client{Transport: outboxTransport(func(r *http.Request) (*http.Response, error) {
		local := r.Clone(r.Context())
		u := *r.URL
		local.URL = &u
		local.URL.Scheme = target.Scheme
		local.URL.Host = target.Host
		return http.DefaultTransport.RoundTrip(local)
	})})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func seedDelivery(t *testing.T, o Outbox, account, key, operation string, input any) int64 {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	err = o.DB.QueryRow(t.Context(), `INSERT INTO relay_channel_outbox(account,request_key,operation,payload,payload_hash,destinations) VALUES($1,$2,$3,$4,'synthetic',$5) RETURNING id`, account, key, operation, raw, sendDestinations(input)).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func deliveryState(t *testing.T, o Outbox, id int64, want string) {
	t.Helper()
	var state string
	if err := o.DB.QueryRow(t.Context(), "SELECT state FROM relay_channel_outbox WHERE id=$1", id).Scan(&state); err != nil || state != want {
		t.Fatalf("state %q want %q: %v", state, want, err)
	}
}
func nextClaim(t *testing.T, o Outbox) *channelClaim {
	t.Helper()
	c, err := o.claim(t.Context(), 0, "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func sendClaim(t *testing.T, o Outbox, c *channelClaim) {
	t.Helper()
	if c == nil {
		t.Fatal("missing claim")
	}
	if _, err := o.send(t.Context(), *c); err != nil {
		t.Fatal(err)
	}
}
func expireClaim(t *testing.T, o Outbox, id int64) {
	t.Helper()
	if _, err := o.DB.Exec(t.Context(), `UPDATE relay_channel_outbox SET updated_at=now()-interval '3 minutes' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := o.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestChannelFairClaimsOrderingAndCrashFencing(t *testing.T) {
	pool, _ := channelTestDB(t)
	var calls atomic.Int32
	handler := func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input struct {
			ChatID int64 `json:"chat_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":99,"chat":{"id":%d}}}`, input.ChatID)
	}
	client := localTelegram(t, "hot", handler)
	o := Outbox{DB: pool, Registry: &Registry{Telegram: map[string]*telegram.Client{"hot": client, "cold": client}}}
	input := telegram.MessageInput{Operation: "text", ChatID: 123, Text: "local queue fixture"}
	for i := 0; i < 80; i++ {
		seedDelivery(t, o, "hot", fmt.Sprint("hot-", i), "telegram.message", input)
	}
	for i := 0; i < 2; i++ {
		seedDelivery(t, o, "cold", fmt.Sprint("cold-", i), "telegram.message", input)
	}
	for _, want := range []string{"hot", "cold", "hot", "cold"} {
		c := nextClaim(t, o)
		if c == nil || c.Account != want {
			t.Fatalf("unfair claim %+v want %s", c, want)
		}
		sendClaim(t, o, c)
	}
	// Many competing workers can claim only the head of a conversation.
	var wg sync.WaitGroup
	claimed := make(chan *channelClaim, 24)
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := o.claim(t.Context(), 0, "")
			if err != nil {
				t.Error(err)
			}
			if c != nil {
				claimed <- c
			}
		}()
	}
	wg.Wait()
	close(claimed)
	var first *channelClaim
	for c := range claimed {
		if first != nil {
			t.Fatal("overlapping conversation claims")
		}
		first = c
	}
	if first == nil {
		t.Fatal("no head claimed")
	}
	// A claim lost before the submission marker returns to queued after restart.
	expireClaim(t, o, first.ID)
	deliveryState(t, o, first.ID, "queued")
	recovered := nextClaim(t, o)
	if recovered == nil || recovered.ID != first.ID {
		t.Fatal("recovery reordered conversation")
	}
	if _, err := o.send(t.Context(), *first); err == nil {
		t.Fatal("stale worker escaped claim fence")
	}
	if calls.Load() != 4 {
		t.Fatal("stale worker performed provider I/O")
	}
	sendClaim(t, o, recovered)
	// Crash after the durable boundary is ambiguous and blocks this destination.
	active := nextClaim(t, o)
	if _, err := pool.Exec(t.Context(), `UPDATE relay_channel_outbox SET submission_started=true WHERE id=$1`, active.ID); err != nil {
		t.Fatal(err)
	}
	expireClaim(t, o, active.ID)
	deliveryState(t, o, active.ID, "uncertain")
	if c := nextClaim(t, o); c != nil {
		t.Fatalf("uncertain destination bypassed: %+v", c)
	}
	different := input
	different.ChatID = 456
	other := seedDelivery(t, o, "hot", "other-destination", "telegram.message", different)
	c := nextClaim(t, o)
	if c == nil || c.ID != other {
		t.Fatal("uncertain destination blocked independent send")
	}
	sendClaim(t, o, c)
	before := calls.Load()
	if err := o.Resolve(t.Context(), "hot", active.ID, "rejected", "Provider confirmed no accepted delivery"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before {
		t.Fatal("rejected resolution sent provider request")
	}
	c = nextClaim(t, o)
	if c == nil || c.ID <= active.ID || c.ID >= other {
		t.Fatal("resolution did not unblock original destination")
	}

}

func TestChannelEmailOverlappingDestinations(t *testing.T) {
	pool, _ := channelTestDB(t)
	o := Outbox{DB: pool}
	input := EmailInput{To: []string{"First <first@example.invalid>"}, Cc: []string{"Copy <COPY@example.invalid>"}, Bcc: []string{"hidden@example.invalid"}, Subject: "Local fixture", Body: "Meaningful body"}
	first := seedDelivery(t, o, "mail", "first", "email.send", input)
	for i, address := range []string{"first@example.invalid", "copy@example.invalid", "Hidden <HIDDEN@example.invalid>", "first@example.invalid (Comment)", "  Copy <copy@example.invalid>  "} {
		next := input
		next.To = []string{address}
		next.Cc = nil
		next.Bcc = nil
		seedDelivery(t, o, "mail", fmt.Sprint(i), "email.send", next)
	}
	c := nextClaim(t, o)
	if c == nil || c.ID != first {
		t.Fatal("missing email head")
	}
	if c = nextClaim(t, o); c != nil {
		t.Fatal("overlapping email recipients claimed concurrently")
	}
}

func TestChannelTelegramRetriesDisabledAndUnknown(t *testing.T) {
	pool, _ := channelTestDB(t)
	var calls atomic.Int32
	var mode atomic.Int32 // 0 rate limit, 1 success, 2 lost final reply
	client := localTelegram(t, "bot", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch mode.Load() {
		case 0:
			w.WriteHeader(429)
			fmt.Fprint(w, `{"ok":false,"error_code":429,"parameters":{"retry_after":30}}`)
		case 1:
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":99,"chat":{"id":123}}}`)
		default:
			h := w.(http.Hijacker)
			c, _, err := h.Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			c.Close()
		}
	})
	o := Outbox{DB: pool, Registry: &Registry{Telegram: map[string]*telegram.Client{"bot": client}}}
	input := telegram.MessageInput{Operation: "text", ChatID: 123, Text: "Local retry fixture"}
	if receipt, err := o.Submit(t.Context(), "bot", "rate-limit", "telegram.message", input); err != nil || receipt["pending"] != true || receipt["state"] != "queued" || receipt["request_key"] != "rate-limit" {
		t.Fatal("scheduled retry receipt missing", receipt, err)
	}
	receipt, err := o.Lookup(t.Context(), "bot", "rate-limit")
	if err != nil {
		t.Fatal(err)
	}
	id := receipt["id"].(int64)
	deliveryState(t, o, id, "queued")
	var scheduled bool
	if err = pool.QueryRow(t.Context(), `SELECT next_attempt>now()+interval '28 seconds' AND attempts=1 AND provider_error IS NOT NULL FROM relay_channel_outbox WHERE id=$1`, id).Scan(&scheduled); err != nil || !scheduled {
		t.Fatal("retry_after ignored", err)
	}
	o.Submit(t.Context(), "bot", "rate-limit", "telegram.message", input)
	if c := nextClaim(t, o); c != nil || calls.Load() != 1 {
		t.Fatal("backoff bypassed")
	}
	mode.Store(1)
	if _, err = pool.Exec(t.Context(), "UPDATE relay_channel_outbox SET next_attempt=now() WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	sendClaim(t, o, nextClaim(t, o))
	deliveryState(t, o, id, "submitted")
	// Eight proven non-acceptances exhaust the safe retry budget.
	mode.Store(0)
	exhausted := seedDelivery(t, o, "bot", "exhausted", "telegram.message", input)
	for i := 0; i < 8; i++ {
		if _, err = pool.Exec(t.Context(), "UPDATE relay_channel_outbox SET next_attempt=now() WHERE id=$1", exhausted); err != nil {
			t.Fatal(err)
		}
		c := nextClaim(t, o)
		if c == nil {
			t.Fatal("missing retry")
		}
		o.send(t.Context(), *c)
	}
	deliveryState(t, o, exhausted, "rejected")
	mode.Store(2)
	unknown := seedDelivery(t, o, "bot", "unknown", "telegram.message", input)
	c := nextClaim(t, o)
	o.send(t.Context(), *c)
	deliveryState(t, o, unknown, "uncertain")
	before := calls.Load()
	o.Submit(t.Context(), "bot", "unknown", "telegram.message", input)
	if calls.Load() != before {
		t.Fatal("unknown outcome resent")
	}
	// Missing and disabled configuration both fail before any provider call.
	disabled, err := telegram.New(telegram.Config{AccountID: "disabled", BotToken: "123456:synthetic_fixture_token_never_used"})
	if err != nil {
		t.Fatal(err)
	}
	o.Registry.Replace(&Registry{Telegram: map[string]*telegram.Client{"disabled": disabled}})
	for _, account := range []string{"disabled", "missing"} {
		_, err = o.Submit(t.Context(), account, "config", "telegram.message", input)
		if err == nil {
			t.Fatal("unavailable account accepted")
		}
		row, err := o.Lookup(t.Context(), account, "config")
		if err != nil || row["state"] != "rejected" {
			t.Fatal("configuration became uncertain", row, err)
		}
	}
	if calls.Load() != before {
		t.Fatal("configuration failure performed I/O")
	}
}

func TestChannelManualResolutionEmitsReceiptWithoutSend(t *testing.T) {
	pool, dsn := channelTestDB(t)
	o := Outbox{DB: pool}
	tg := seedDelivery(t, o, "fixture", "telegram-resolution", "telegram.message", telegram.MessageInput{Operation: "text", ChatID: -123, Text: "manual fixture"})
	mail := seedDelivery(t, o, "fixture", "email-resolution", "email.send", EmailInput{To: []string{"recipient@example.invalid"}, Subject: "local fixture", Body: "manual resolution"})
	if _, err := pool.Exec(t.Context(), "UPDATE relay_channel_outbox SET state='uncertain' WHERE id=ANY($1)", []int64{tg, mail}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []Resolution{{}, {ProviderID: "0", ChatID: -123}, {ProviderID: "22", ChatID: 999}, {ProviderID: "1e3", ChatID: -123}} {
		if err := o.Resolve(t.Context(), "fixture", tg, "submitted", "Verified provider evidence", e); err == nil {
			t.Fatal("invalid evidence accepted")
		}
	}
	if err := o.Resolve(t.Context(), "other", tg, "submitted", "Verified provider evidence", Resolution{ProviderID: "22", ChatID: -123}); err == nil {
		t.Fatal("cross-account resolution")
	}
	if err := o.Resolve(t.Context(), "fixture", tg, "submitted", "Verified provider evidence", Resolution{ProviderID: "22", ChatID: -123}); err != nil {
		t.Fatal(err)
	}
	if err := o.Resolve(t.Context(), "fixture", mail, "submitted", "Verified provider evidence", Resolution{ProviderID: "<confirmed@example.invalid>"}); err != nil {
		t.Fatal(err)
	}
	if err := o.Resolve(t.Context(), "fixture", mail, "submitted", "Duplicate evidence", Resolution{ProviderID: "<confirmed@example.invalid>"}); err == nil {
		t.Fatal("duplicate resolution")
	}
	var delivered atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Statuses []StatusReceipt }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		for _, s := range body.Statuses {
			if s.State != "submitted" {
				continue
			}
			var receipt map[string]any
			json.Unmarshal(s.Receipt, &receipt)
			if receipt["manually_reconciled"] != true {
				t.Error("resolution flag missing")
			}
			if s.DeliveryID == tg && (receipt["chat_id"] != float64(-123) || receipt["message_id"] != float64(22)) {
				t.Error("Telegram identifiers lost")
			}
			if s.DeliveryID == mail && (receipt["message_id"] != "<confirmed@example.invalid>" || receipt["sent"] != true) {
				t.Error("email identifier lost")
			}
			delivered.Add(1)
		}
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]int{"accepted": len(body.Statuses)}})
	}))
	defer server.Close()
	d := CallbackDispatcher{URL: dsn}
	a := RuntimeCallback{Account: "fixture", Site: server.URL}
	if err := d.deliverStatuses(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := d.deliverStatuses(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if delivered.Load() != 2 {
		t.Fatal("outcome callbacks missing or duplicated")
	}
	if c := nextClaim(t, o); c != nil {
		t.Fatal("manual resolution enqueued send")
	}
}

func TestSMTPOutcomeRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		e     *relayemail.SendError
		state string
	}{
		{&relayemail.SendError{Stage: "connect", Retryable: true}, "queued"},
		{&relayemail.SendError{Stage: "auth", Code: 535}, "rejected"},
		{&relayemail.SendError{Stage: "rcpt", Code: 450, Retryable: true}, "queued"},
		{&relayemail.SendError{Stage: "rcpt", Code: 550}, "rejected"},
		{&relayemail.SendError{Stage: "data_reply", OutcomeUnknown: true}, "uncertain"},
		{&relayemail.SendError{Stage: "data_reply", Code: 451}, "rejected"},
	} {
		state, _, delay, metadata := channelSendOutcome(tc.e, 0, false)
		if state != tc.state || len(metadata) == 0 || (state == "queued" && delay < time.Second) {
			t.Fatalf("%+v -> %s", tc.e, state)
		}
	}
	state, _, _, _ := channelSendOutcome(errors.New("lost response"), 0, false)
	if state != "uncertain" {
		t.Fatal("unknown generic outcome retried")
	}
}

func TestChannelCompetingWorkersDrainOrderedLoad(t *testing.T) {
	pool, _ := channelTestDB(t)
	const accounts = 4
	const perAccount = 32
	var mu sync.Mutex
	seen := map[string]int{}
	var accepted atomic.Int32
	client := localTelegram(t, "load", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		var account, sequence int
		if _, err := fmt.Sscanf(input.Text, "account=%d sequence=%d", &account, &sequence); err != nil {
			t.Error(err)
		}
		key := fmt.Sprintf("%d:%d", account, input.ChatID)
		mu.Lock()
		if sequence != seen[key]+1 {
			t.Errorf("destination order %s: got %d after %d", key, sequence, seen[key])
		}
		seen[key] = sequence
		mu.Unlock()
		accepted.Add(1)
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"chat":{"id":%d}}}`, sequence, input.ChatID)
	})
	registry := &Registry{Telegram: map[string]*telegram.Client{}}
	for i := 0; i < accounts; i++ {
		registry.Telegram[fmt.Sprint(i)] = client
	}
	o := Outbox{DB: pool, Registry: registry}
	for a := 0; a < accounts; a++ {
		for i := 1; i <= perAccount; i++ {
			seedDelivery(t, o, fmt.Sprint(a), fmt.Sprint(i), "telegram.message", telegram.MessageInput{Operation: "text", ChatID: 123, Text: fmt.Sprintf("account=%d sequence=%d", a, i)})
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				c, err := o.claim(ctx, 0, "")
				if err != nil {
					if ctx.Err() == nil {
						t.Error(err)
					}
					return
				}
				if c == nil {
					if accepted.Load() == accounts*perAccount {
						return
					}
					time.Sleep(time.Millisecond)
					continue
				}
				if _, err = o.send(ctx, *c); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	var submitted int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM relay_channel_outbox WHERE state='submitted'").Scan(&submitted); err != nil || submitted != accounts*perAccount || accepted.Load() != accounts*perAccount {
		t.Fatalf("incomplete load: submitted=%d accepted=%d err=%v", submitted, accepted.Load(), err)
	}
}

func TestChannelEmailConnectFailureIsDurablyRetryable(t *testing.T) {
	pool, _ := channelTestDB(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	client, err := relayemail.New(relayemail.Config{Enabled: true, AccountID: "mail", Host: "127.0.0.1", Port: port, From: "fixture@example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	o := Outbox{DB: pool, Registry: &Registry{Email: map[string]*relayemail.Client{"mail": client}}}
	receipt, err := o.Submit(t.Context(), "mail", "connect-refused", "email.send", EmailInput{To: []string{"recipient@example.invalid"}, Subject: "Local refusal", Body: "A representative message"})
	if err != nil || receipt["pending"] != true || receipt["state"] != "queued" || receipt["request_key"] != "connect-refused" {
		t.Fatal("scheduled retry receipt missing", receipt, err)
	}
	var state, stage string
	var attempts int
	var started bool
	if err = pool.QueryRow(t.Context(), `SELECT state,provider_error->>'stage',attempts,submission_started FROM relay_channel_outbox WHERE request_key='connect-refused'`).Scan(&state, &stage, &attempts, &started); err != nil || state != "queued" || stage != "connect" || attempts != 1 || started {
		t.Fatal("connect failure did not schedule safe retry", state, stage, attempts, started, err)
	}
}
