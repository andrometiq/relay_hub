package forward

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// Retention is how long forward records are kept.
	Retention = 7 * 24 * time.Hour
	// Attempts is the number of tries for one forward.
	Attempts = 3
	timeout  = 10 * time.Second
	batch    = 50
	// parallel bounds how many targets receive at the same time. Each target
	// gets its forwards one at a time, in the order they were received.
	parallel = 8
)

// retryDelays is the wait before the second and the third try.
var retryDelays = []time.Duration{30 * time.Second, 2 * time.Minute}

// Worker sends queued forwards in the background, so the provider never waits
// for a target.
type Worker struct {
	DB     *pgxpool.Pool
	Wake   <-chan struct{}
	Client *http.Client
	// Tick is how often due retries and pruning are checked.
	Tick time.Duration
}

// NewClient does not follow redirects: a redirect is recorded as a failure.
func NewClient() *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type job struct {
	id       int64
	target   int64
	address  string
	headers  map[string]string
	body     []byte
	attempts int
}

func (w Worker) Run(ctx context.Context) {
	tick := w.Tick
	if tick == 0 {
		tick = 15 * time.Second
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		for w.DeliverDue(ctx) == batch {
		}
		if err := w.Prune(ctx); err != nil && ctx.Err() == nil {
			log.Print("forward prune failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-w.Wake:
		case <-ticker.C:
		}
	}
}

// DeliverDue sends the due forwards once and returns how many it claimed.
func (w Worker) DeliverDue(ctx context.Context) int {
	jobs, err := w.claim(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Print("forward claim failed")
		}
		return 0
	}
	byTarget := map[int64][]job{}
	order := []int64{}
	for _, j := range jobs {
		if byTarget[j.target] == nil {
			order = append(order, j.target)
		}
		byTarget[j.target] = append(byTarget[j.target], j)
	}
	slots := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, target := range order {
		slots <- struct{}{}
		wg.Add(1)
		go func(list []job) {
			defer func() { <-slots; wg.Done() }()
			for _, j := range list {
				w.deliver(ctx, j)
			}
		}(byTarget[target])
	}
	wg.Wait()
	return len(jobs)
}

// claim leases due rows for longer than a batch can take, so a crash leaves
// them due again and a slow batch is never sent twice.
func (w Worker) claim(ctx context.Context) ([]job, error) {
	rows, err := w.DB.Query(ctx, `WITH due AS (
 SELECT id FROM relay_forwards WHERE state='pending' AND next_attempt_at<=now() ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED)
 UPDATE relay_forwards f SET next_attempt_at=now()+$2::interval FROM due, relay_forward_targets t
 WHERE f.id=due.id AND t.id=f.target_id RETURNING f.id,f.target_id,t.url,f.headers,f.body,f.attempts`, batch, batch*timeout+time.Minute)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []job{}
	for rows.Next() {
		var j job
		if err = rows.Scan(&j.id, &j.target, &j.address, &j.headers, &j.body, &j.attempts); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(jobs, func(a, b job) int { return cmp.Compare(a.id, b.id) })
	return jobs, nil
}

func (w Worker) deliver(ctx context.Context, j job) {
	started := time.Now()
	status, problem := w.post(ctx, j)
	latency := time.Since(started).Milliseconds()
	attempts := j.attempts + 1
	state, delay := "delivered", time.Duration(0)
	if problem != "" {
		state = "failed"
		if attempts < Attempts {
			state, delay = "pending", retryDelays[attempts-1]
		}
	}
	_, err := w.DB.Exec(context.WithoutCancel(ctx), `UPDATE relay_forwards SET state=$2,attempts=$3,status_code=$4,error=$5,latency_ms=$6,
 next_attempt_at=now()+$7::interval,delivered_at=CASE WHEN $2='delivered' THEN now() END WHERE id=$1`,
		j.id, state, attempts, status, problem, latency, delay)
	if err != nil {
		log.Printf("forward %d result not saved", j.id)
	}
}

// post sends the exact body and headers. The error text never contains the
// URL, which may carry a token in its query.
func (w Worker) post(ctx context.Context, j job) (int, string) {
	client := w.Client
	if client == nil {
		client = NewClient()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, j.address, bytes.NewReader(j.body))
	if err != nil {
		return 0, "invalid target URL"
	}
	request.Header.Set("User-Agent", "")
	for name, value := range j.headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		if errors.Is(err, context.DeadlineExceeded) || (urlErr != nil && urlErr.Timeout()) {
			return 0, "timeout"
		}
		return 0, shorten(err.Error())
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return response.StatusCode, fmt.Sprintf("HTTP %d", response.StatusCode)
	}
	return response.StatusCode, ""
}

func shorten(text string) string {
	if len(text) > 200 {
		return text[:200]
	}
	return text
}

// Prune deletes records older than the retention.
func (w Worker) Prune(ctx context.Context) error {
	_, err := w.DB.Exec(ctx, `DELETE FROM relay_forwards WHERE created_at<now()-$1::interval`, Retention)
	return err
}
