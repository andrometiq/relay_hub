package callbackworker

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSlowAccountDoesNotBlockOthersAndWorkIsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	active, peak := 0, 0
	inflight := map[string]bool{}
	seen := map[string]bool{}
	overlap := false
	complete := make(chan struct{}, 1)
	jobs := make([]Job, 100)
	for i := range jobs {
		key := fmt.Sprint(i)
		jobs[i] = Job{Key: key, Run: func(ctx context.Context) {
			mu.Lock()
			overlap = overlap || inflight[key]
			inflight[key] = true
			active++
			peak = max(peak, active)
			seen[key] = true
			if len(seen) == 100 {
				select {
				case complete <- struct{}{}:
				default:
				}
			}
			mu.Unlock()
			if key == "0" {
				<-ctx.Done()
			} else {
				time.Sleep(2 * time.Millisecond)
			}
			mu.Lock()
			active--
			delete(inflight, key)
			mu.Unlock()
		}}
	}
	go Run(ctx, time.Millisecond, 8, func() []Job { return jobs })
	select {
	case <-complete:
	case <-time.After(3 * time.Second):
		t.Fatal("one blocked account prevented other accounts from running")
	}
	mu.Lock()
	defer mu.Unlock()
	if overlap || peak > 8 {
		t.Fatalf("overlap=%v peak=%d", overlap, peak)
	}
}
