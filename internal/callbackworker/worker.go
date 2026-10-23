// Package callbackworker schedules bounded, non-overlapping account callbacks.
package callbackworker

import (
	"context"
	"time"
)

type Job struct {
	Key string
	Run func(context.Context)
}

// Run refreshes the account snapshot every tick. A slow account consumes one
// slot, never the entire dispatcher. Keys cannot overlap within this scheduler;
// database leases/locks remain responsible for exclusion across replicas.
func Run(ctx context.Context, interval time.Duration, workers int, snapshot func() []Job) {
	if workers < 1 || interval <= 0 {
		panic("invalid callback worker configuration")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	done := make(chan string, workers)
	busy := make(map[string]bool)
	next := 0
	for {
		select {
		case <-ctx.Done():
			return
		case key := <-done:
			delete(busy, key)
		case <-ticker.C:
			jobs := snapshot()
			if len(jobs) == 0 {
				continue
			}
			start := next % len(jobs)
			for scanned := 0; scanned < len(jobs) && len(busy) < workers; scanned++ {
				index := (start + scanned) % len(jobs)
				next = (index + 1) % len(jobs)
				job := jobs[index]
				if busy[job.Key] {
					continue
				}
				busy[job.Key] = true
				go func() {
					jobCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
					defer cancel()
					job.Run(jobCtx)
					select {
					case done <- job.Key:
					case <-ctx.Done():
					}
				}()
			}
		}
	}
}
