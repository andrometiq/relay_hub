package requestlog

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Copier is the COPY part of a pgx pool or connection.
type Copier interface {
	CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error)
}

const (
	// BufferSize holds about 30 s of a 300 requests/s peak while a write waits.
	BufferSize    = 10000
	BatchSize     = 500
	FlushInterval = time.Second
	writeTimeout  = 10 * time.Second
	reportEvery   = time.Minute
)

var columns = []string{"occurred_at", "method", "path", "route", "status", "duration_ms", "request_bytes", "response_bytes",
	"remote_ip", "user_agent", "site_id", "account", "request_id", "error"}

// Writer stores entries in batches from one goroutine. Record never blocks:
// when the buffer is full, or after Close starts, it counts the entry as dropped.
type Writer struct {
	db        Copier
	entries   chan Entry
	batchSize int
	interval  time.Duration
	stopping  chan struct{}
	stopCtx   context.Context
	done      chan struct{}
	stopOnce  sync.Once
	dropped   atomic.Int64
	failed    atomic.Int64
}

// NewPool opens the request log's own pool: one connection for the writer and
// one for admin reads, so neither waits for, or holds, an auth pool connection.
func NewPool(ctx context.Context, config *pgxpool.Config) (*pgxpool.Pool, error) {
	config = config.Copy()
	config.MaxConns, config.MinConns = 2, 0
	return pgxpool.NewWithConfig(ctx, config)
}

func NewWriter(db Copier) *Writer {
	return newWriter(db, BufferSize, BatchSize, FlushInterval)
}

func newWriter(db Copier, buffer, batch int, interval time.Duration) *Writer {
	w := &Writer{db: db, entries: make(chan Entry, buffer), batchSize: batch, interval: interval, stopping: make(chan struct{}), done: make(chan struct{})}
	go w.run()
	return w
}

func (w *Writer) Record(entry Entry) {
	select {
	case <-w.stopping:
		w.dropped.Add(1)
		return
	default:
	}
	select {
	case w.entries <- entry:
	default:
		w.dropped.Add(1)
	}
}

// Close stops new entries, writes the buffered ones within ctx and waits for
// that or ctx.
func (w *Writer) Close(ctx context.Context) error {
	w.stopOnce.Do(func() {
		w.stopCtx = ctx
		close(w.stopping)
	})
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Writer) run() {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	batch := make([]Entry, 0, w.batchSize)
	reported := time.Now()
	for {
		select {
		case entry := <-w.entries:
			if batch = append(batch, entry); len(batch) >= w.batchSize {
				w.flush(context.Background(), batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			w.flush(context.Background(), batch)
			batch = batch[:0]
			if time.Since(reported) >= reportEvery {
				w.report()
				reported = time.Now()
			}
		case <-w.stopping:
			w.drain(batch)
			w.report()
			return
		}
	}
}

// drain writes the buffered entries within the Close deadline. After a failed
// batch it stops writing and counts the rest as dropped, so a database outage
// cannot hold up shutdown.
func (w *Writer) drain(batch []Entry) {
	stored := true
	for stored {
		select {
		case entry := <-w.entries:
			if batch = append(batch, entry); len(batch) >= w.batchSize {
				stored = w.flush(w.stopCtx, batch)
				batch = batch[:0]
			}
		default:
			w.flush(w.stopCtx, batch)
			stored = false
		}
	}
	// Record can still send an entry that passed its stopping check.
	for {
		select {
		case <-w.entries:
			w.dropped.Add(1)
		default:
			return
		}
	}
}

// flush writes one batch with COPY and reports whether it was stored. A failed
// batch is counted, not retried, so a database outage cannot grow memory or
// delay requests. Only the first error of a report period is logged.
func (w *Writer) flush(parent context.Context, batch []Entry) bool {
	if len(batch) == 0 {
		return true
	}
	ctx, cancel := context.WithTimeout(parent, writeTimeout)
	defer cancel()
	if _, err := w.db.CopyFrom(ctx, pgx.Identifier{"relay_request_log"}, columns, pgx.CopyFromSlice(len(batch), func(i int) ([]any, error) {
		e := batch[i]
		var ip any
		if e.RemoteIP.IsValid() {
			ip = e.RemoteIP
		}
		return []any{e.OccurredAt, e.Method, e.Path, nullable(e.Route), e.Status, e.DurationMS, e.RequestBytes, e.ResponseBytes,
			ip, e.UserAgent, nullable(e.SiteID), nullable(e.Account), e.RequestID, nullable(e.Error)}, nil
	})); err != nil {
		if w.failed.Add(int64(len(batch))) == int64(len(batch)) {
			log.Printf("request log: cannot store entries: %v", err)
		}
		return false
	}
	return true
}

// report logs one line for the entries lost since the last report.
func (w *Writer) report() {
	dropped, failed := w.dropped.Swap(0), w.failed.Swap(0)
	if dropped > 0 || failed > 0 {
		log.Printf("request log: %d entries dropped (buffer full or closing), %d entries not stored (database error)", dropped, failed)
	}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
