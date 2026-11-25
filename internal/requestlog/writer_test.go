package requestlog

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// memoryCopier stores copied rows; block holds each copy until it is closed.
type memoryCopier struct {
	mu      sync.Mutex
	batches []int
	rows    [][]any
	block   chan struct{}
	err     error
}

func (m *memoryCopier) CopyFrom(ctx context.Context, table pgx.Identifier, cols []string, source pgx.CopyFromSource) (int64, error) {
	if m.block != nil {
		<-m.block
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return 0, m.err
	}
	n := 0
	for source.Next() {
		values, err := source.Values()
		if err != nil {
			return 0, err
		}
		if len(values) != len(cols) {
			return 0, errors.New("column count")
		}
		m.rows = append(m.rows, values)
		n++
	}
	m.batches = append(m.batches, n)
	return int64(n), nil
}

func (m *memoryCopier) stored() (int, []int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows), append([]int(nil), m.batches...)
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWriterFlushesFullBatch(t *testing.T) {
	db := &memoryCopier{}
	w := newWriter(db, 100, 5, time.Hour)
	for range 12 {
		w.Record(Entry{Method: "GET", Path: "/"})
	}
	waitFor(t, func() bool { n, _ := db.stored(); return n == 10 })
	if _, batches := db.stored(); len(batches) != 2 || batches[0] != 5 {
		t.Fatalf("batches %v", batches)
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.stored(); n != 12 {
		t.Fatalf("stored %d", n)
	}
}

func TestWriterFlushesOnInterval(t *testing.T) {
	db := &memoryCopier{}
	w := newWriter(db, 100, 500, 20*time.Millisecond)
	defer w.Close(context.Background())
	w.Record(Entry{Method: "GET"})
	w.Record(Entry{Method: "POST"})
	waitFor(t, func() bool { n, _ := db.stored(); return n == 2 })
}

func TestWriterFlushesOnClose(t *testing.T) {
	db := &memoryCopier{}
	w := newWriter(db, 100, 500, time.Hour)
	for range 7 {
		w.Record(Entry{})
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.stored(); n != 7 {
		t.Fatalf("stored %d", n)
	}
	w.Record(Entry{})
	if w.dropped.Load() != 1 {
		t.Fatalf("a record after close must count as dropped, got %d", w.dropped.Load())
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal("second close", err)
	}
}

func TestWriterDropsWhenFullWithoutBlocking(t *testing.T) {
	db := &memoryCopier{block: make(chan struct{})}
	w := newWriter(db, 3, 1, time.Hour)
	w.Record(Entry{})
	waitFor(t, func() bool { return len(w.entries) == 0 }) // the writer holds it in a blocked copy
	start := time.Now()
	for range 10 {
		w.Record(Entry{})
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("Record blocked")
	}
	if w.dropped.Load() != 7 {
		t.Fatalf("dropped %d", w.dropped.Load())
	}
	close(db.block)
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.stored(); n != 4 {
		t.Fatalf("stored %d", n)
	}
}

func TestWriterCountsFailedBatchAndContinues(t *testing.T) {
	db := &memoryCopier{err: errors.New("database down")}
	w := newWriter(db, 100, 2, time.Hour)
	w.Record(Entry{})
	w.Record(Entry{})
	waitFor(t, func() bool { return w.failed.Load() == 2 })
	db.mu.Lock()
	db.err = nil
	db.mu.Unlock()
	w.Record(Entry{})
	w.Close(context.Background())
	if n, _ := db.stored(); n != 1 {
		t.Fatalf("stored %d", n)
	}
}

func TestWriterCloseHonoursDeadline(t *testing.T) {
	db := &memoryCopier{block: make(chan struct{})}
	defer close(db.block)
	w := newWriter(db, 10, 500, time.Hour)
	w.Record(Entry{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := w.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}
