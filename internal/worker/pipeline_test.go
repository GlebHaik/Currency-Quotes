package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GlebHaik/currency-quotes/internal/domain"
	"github.com/GlebHaik/currency-quotes/internal/provider"
	"github.com/google/uuid"
)

func TestNotifyWakesDispatcherWithoutBlocking(t *testing.T) {
	q := NewJobQueue()
	done := make(chan struct{})
	go func() {
		for range 1000 {
			q.Notify()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Notify blocks: nobody reads wake in this test")
	}
	if len(q.wake) != 1 {
		t.Fatalf("expected exactly one pending wake-up, got %d", len(q.wake))
	}
}

func TestBuildJobsGroupsByPair(t *testing.T) {
	op := func(pair string, attempts int) domain.UpdateOperation {
		return domain.UpdateOperation{ID: uuid.New(), CurrencyPair: pair, Attempts: attempts}
	}
	jobs := buildJobs([]domain.UpdateOperation{
		op("EUR/MXN", 1), op("EUR/USD", 1), op("EUR/MXN", 3), op("EUR/MXN", 2),
	})
	if len(jobs) != 2 {
		t.Fatalf("expected one job per pair, got %d jobs", len(jobs))
	}
	mxn := jobs[0]
	if mxn.Pair != "EUR/MXN" || len(mxn.IDs) != 3 || mxn.Attempts != 3 {
		t.Fatalf("EUR/MXN job must carry 3 IDs and the max attempts (3), got %+v", mxn)
	}
}

func TestSendGivesUpOnCancel(t *testing.T) {
	q := NewJobQueue()
	for range cap(q.jobs) {
		q.jobs <- Job{}
	}
	d := NewDispatcher(newFakeStore(), q, Config{}.withDefaults())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := make(chan bool, 1)
	go func() { res <- d.send(ctx, Job{Pair: "EUR/MXN"}) }()
	select {
	case ok := <-res:
		if ok {
			t.Fatal("send must report false when ctx is cancelled and the channel is full")
		}
	case <-time.After(time.Second):
		t.Fatal("send blocks on a full channel and ignores ctx: the dispatcher can't shut down")
	}
}

func TestWorkersRunInParallel(t *testing.T) {
	store := newFakeStore("EUR/MXN", "EUR/USD", "EUR/GBP", "EUR/JPY")
	fake := providerFunc(func(context.Context, string, string) (string, error) {
		time.Sleep(200 * time.Millisecond)
		return "1", nil
	})
	start := time.Now()
	startRunner(t, store, fake, Config{Workers: 4})
	waitFor(t, "all completed", func() bool {
		for _, id := range store.order {
			if store.get(id).Status != domain.StatusCompleted {
				return false
			}
		}
		return true
	})
	if took := time.Since(start); took > 600*time.Millisecond {
		t.Fatalf("4 jobs × 200ms with 4 workers took %v: they run one by one", took)
	}
}

func TestPanicDoesNotKillPool(t *testing.T) {
	store := newFakeStore()
	store.add("EUR/MXN", 0)
	okID := store.add("EUR/USD", 0)
	fake := providerFunc(func(_ context.Context, _, quote string) (string, error) {
		if quote == "MXN" {
			panic("boom")
		}
		return "1.1", nil
	})
	startRunner(t, store, fake, Config{Workers: 1})
	waitFor(t, "second job completed after a panic in the first", func() bool {
		return store.get(okID).Status == domain.StatusCompleted
	})
}

func TestExponentialBackoffWithJitter(t *testing.T) {
	p := &WorkerPool{cfg: Config{RetryBackoff: time.Second, MaxBackoff: 10 * time.Second}}
	plain := &provider.Error{StatusCode: 503}

	for attempt := 1; attempt <= 6; attempt++ {
		ceiling := min(time.Second<<(attempt-1), 10*time.Second)
		var biggest time.Duration
		for range 200 {
			d := p.backoff(attempt, plain)
			if d <= 0 || d > ceiling {
				t.Fatalf("attempt %d: delay %v outside (0, %v]", attempt, d, ceiling)
			}
			biggest = max(biggest, d)
		}
		if attempt >= 3 && biggest <= ceiling/2 {
			t.Fatalf("attempt %d: delays never exceed %v, they don't grow", attempt, ceiling/2)
		}
	}

	same := true
	first := p.backoff(4, plain)
	for range 20 {
		if p.backoff(4, plain) != first {
			same = false
		}
	}
	if same {
		t.Fatal("no jitter: every update would retry at the same moment")
	}

	if d := p.backoff(1, &provider.Error{StatusCode: 429, RetryAfter: 30 * time.Second}); d < 30*time.Second {
		t.Fatalf("Retry-After of 30s must be respected, got %v", d)
	}
	if d := p.backoff(10, &provider.Error{StatusCode: 429, RetryAfter: 30 * time.Second}); d < 30*time.Second {
		t.Fatalf("Retry-After of 30s must be respected even when the backoff is capped, got %v", d)
	}

	// Network errors and timeouts are not *provider.Error, but they are retried too.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("backoff panics on an error that is not *provider.Error: %v", r)
		}
	}()
	netErr := errors.New("frankfurter: dial tcp: connection refused")
	for _, attempt := range []int{1, 3, 20, 100} {
		if d := p.backoff(attempt, netErr); d <= 0 || d > 10*time.Second {
			t.Fatalf("attempt %d with a network error: delay %v outside (0, 10s]", attempt, d)
		}
	}
}

func TestResultSurvivesShutdown(t *testing.T) {
	store := newFakeStore()
	id := store.add("EUR/MXN", 0)
	ctx, cancel := context.WithCancel(context.Background())
	fake := providerFunc(func(context.Context, string, string) (string, error) {
		cancel()
		return "20.955", nil
	})
	p := NewWorkerPool(NewJobQueue(), fake, store, Config{}.withDefaults())
	ops, _ := store.ClaimPending(context.Background(), 1, time.Minute)
	p.process(ctx, buildJobs(ops)[0])

	if op := store.get(id); op.Status != domain.StatusCompleted {
		t.Fatalf("the price was fetched but not saved because ctx was cancelled: status %s", op.Status)
	}
}
