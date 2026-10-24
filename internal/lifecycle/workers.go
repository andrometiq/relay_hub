// Package lifecycle joins runtime workers before their storage closes.
package lifecycle

import (
	"context"
	"sync"
)

type key struct{}
type Workers struct{ wg sync.WaitGroup }

func WithWorkers(ctx context.Context) (context.Context, *Workers) {
	workers := &Workers{}
	return context.WithValue(ctx, key{}, workers), workers
}

// Go tracks descendants with the same context, including final journal writes.
func Go(ctx context.Context, work func()) {
	workers, _ := ctx.Value(key{}).(*Workers)
	if workers == nil {
		go work()
		return
	}
	workers.wg.Add(1)
	go func() { defer workers.wg.Done(); work() }()
}

func (w *Workers) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() { w.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
