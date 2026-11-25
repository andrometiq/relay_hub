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
	done      chan struct{}
	stopOnce  sync.Once
	dropped   atomic.Int64
	failed    atomic.Int64
}

// NewPool opens the writer's own single connection, so request log writes
// never wait for, or hold, a connection of the auth and admin pool.
func NewPool(ctx context.Context, config *pgxpool.Config) (*pgxpool.Pool, error) {
	config = config.Copy()
	config.MaxConns, config.MinConns = 1, 0
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

// Close stops new entries, writes the buffered ones and waits for that or ctx.
func (w *Writer) Close(ctx context.Context) error {
	w.stopOnce.Do(func() { close(w.stopping) })
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
	add := func(entry Entry) {
		if batch = append(batch, entry); len(batch) >= w.batchSize {
			batch = w.flush(batch)
		}
	}
	for {
		select {
		case entry := <-w.entries:
			add(entry)
		case <-ticker.C:
			batch = w.flush(batch)
			if time.Since(reported) >= reportEvery {
				w.report()
				reported = time.Now()
			}
		case <-w.stopping:
			for {
				select {
				case entry := <-w.entries:
					add(entry)
				default:
					w.flush(batch)
					w.report()
					return
				}
			}
		}
	}
}

// flush writes one batch with COPY. A failed batch is counted, not retried, so
// a database outage cannot grow memory or delay requests. Only the first error
// of a report period is logged.
func (w *Writer) flush(batch []Entry) []Entry {
	if len(batch) == 0 {
		return batch
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
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
	}
	return batch[:0]
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
