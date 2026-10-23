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

// Run refreshes the account snapshot before dispatch. A slow account consumes one
// slot, never the entire dispatcher. Keys cannot overlap within this scheduler;
// database leases/locks remain responsible for exclusion across replicas.
func Run(ctx context.Context, interval time.Duration, workers int, snapshot func() []Job) {
	RunWithWake(ctx, interval, workers, snapshot, nil)
}

// RunWithWake retains the ticker for retries and work committed by other replicas.
func RunWithWake(ctx context.Context, interval time.Duration, workers int, snapshot func() []Job, wake <-chan struct{}) {
	if workers < 1 || interval <= 0 {
		panic("invalid callback worker configuration")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	done := make(chan string, workers)
	busy := make(map[string]bool)
	queued := make(map[string]bool)
	var pending []string
	for {
		refresh := false
		select {
		case <-ctx.Done():
			return
		case key := <-done:
			delete(busy, key)
		case <-ticker.C:
			refresh = true
		case <-wake:
			refresh = true
		}
		jobs := make(map[string]Job)
		for _, job := range snapshot() {
			jobs[job.Key] = job
			if refresh && !queued[job.Key] {
				pending = append(pending, job.Key)
				queued[job.Key] = true
			}
		}
		remaining := pending[:0]
		for _, key := range pending {
			job, ok := jobs[key]
			if !ok {
				delete(queued, key)
				continue
			}
			if busy[key] || len(busy) >= workers {
				remaining = append(remaining, key)
				continue
			}
			delete(queued, key)
			busy[key] = true
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
		pending = remaining
	}
}
