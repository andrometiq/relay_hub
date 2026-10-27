package callbackworker

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestWakeRetainedWhileBusyAndCapacityIsFull(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := make(chan struct{}, 1)
	started := make(chan string, 8)
	release := make(chan struct{}, 8)
	jobs := []Job{}
	for _, key := range []string{"a", "b"} {
		jobs = append(jobs, Job{Key: key, Run: func(ctx context.Context) {
			started <- key
			select {
			case <-release:
			case <-ctx.Done():
			}
		}})
	}
	refreshed := make(chan struct{}, 8)
	go RunWithWake(ctx, time.Hour, 1, func() []Job {
		refreshed <- struct{}{}
		return jobs
	}, wake)
	wait := func(expected string) {
		t.Helper()
		select {
		case got := <-started:
			if got != expected {
				t.Fatalf("got %s, want %s", got, expected)
			}
		case <-time.After(time.Second):
			t.Fatal("work waited for the hourly ticker")
		}
	}
	wake <- struct{}{}
	wait("a")
	<-refreshed
	wake <- struct{}{}
	<-refreshed
	select {
	case <-started:
		t.Fatal("overlapping work exceeded capacity")
	default:
	}
	release <- struct{}{}
	wait("b")
	release <- struct{}{}
	wait("a")
}

func TestTickerRecoversWithoutWake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 1)
	go RunWithWake(ctx, time.Millisecond, 1, func() []Job {
		return []Job{{Key: "recovered", Run: func(ctx context.Context) {
			started <- struct{}{}
			<-ctx.Done()
		}}}
	}, nil)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("durable work did not recover on the ticker")
	}
}

func TestPendingAccountsUseCurrentSnapshot(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		for _, removed := range []bool{false, true} {
			t.Run(fmt.Sprintf("refresh=%v/removed=%v", refresh, removed), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				wake := make(chan struct{}, 1)
				started := make(chan string, 8)
				release := make(chan struct{})
				job := func(key, configuration string) Job {
					return Job{Key: key, Run: func(ctx context.Context) {
						started <- configuration
						if key == "blocker" {
							select {
							case <-release:
							case <-ctx.Done():
							}
						}
					}}
				}
				var mu sync.Mutex
				jobs := []Job{job("blocker", "blocker"), job("account", "old URL and credentials"), job("tail", "tail")}
				refreshed := make(chan struct{}, 8)
				go RunWithWake(ctx, time.Hour, 1, func() []Job {
					mu.Lock()
					defer mu.Unlock()
					refreshed <- struct{}{}
					return append([]Job(nil), jobs...)
				}, wake)
				wait := func(expected string) {
					t.Helper()
					select {
					case got := <-started:
						if got != expected {
							t.Fatalf("callback used %q, want %q", got, expected)
						}
					case <-time.After(time.Second):
						t.Fatal("pending work waited for the hourly ticker")
					}
				}
				wake <- struct{}{}
				wait("blocker")
				<-refreshed
				mu.Lock()
				jobs = []Job{job("tail", "tail")}
				if !removed {
					jobs = append(jobs, job("account", "new URL and credentials"))
				}
				mu.Unlock()
				if refresh {
					wake <- struct{}{}
					select {
					case <-refreshed:
					case <-time.After(time.Second):
						t.Fatal("snapshot did not refresh while capacity was full")
					}
				}
				close(release)
				if !removed {
					wait("new URL and credentials")
				}
				wait("tail")
			})
		}
	}
}

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
