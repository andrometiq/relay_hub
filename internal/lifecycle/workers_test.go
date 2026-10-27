package lifecycle

import (
	"context"
	"testing"
	"time"
)

func TestWaitIncludesNestedFinalJournalWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	ctx, workers := WithWorkers(ctx)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	Go(ctx, func() { Go(ctx, func() { close(entered); <-ctx.Done(); <-release; close(finished) }) })
	<-entered
	cancel()
	short, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if workers.Wait(short) == nil {
		t.Fatal("returned before journal write")
	}
	close(release)
	deadline, done := context.WithTimeout(context.Background(), time.Second)
	defer done()
	if err := workers.Wait(deadline); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("journal not finished")
	}
}
