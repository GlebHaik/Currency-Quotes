package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GlebHaik/currency-quotes/internal/service"
)

var (
	_ batchClaimer     = (*service.QuoteUpdater)(nil)
	_ batchProcessor   = (*service.QuoteUpdater)(nil)
	_ service.Notifier = (*background)(nil)
)

// fakeClaimer hands out queued batches, at most limit per claim, like a leasing store would.
type fakeClaimer struct {
	mu     sync.Mutex
	queue  []service.Batch
	limits []int
}

func (c *fakeClaimer) add(pairs ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range pairs {
		c.queue = append(c.queue, service.Batch{Pair: p})
	}
}

func (c *fakeClaimer) Claim(_ context.Context, limit int) ([]service.Batch, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.limits = append(c.limits, limit)
	n := min(limit, len(c.queue))
	out := c.queue[:n:n]
	c.queue = c.queue[n:]
	return out, nil
}

func (c *fakeClaimer) firstLimit() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.limits) == 0 {
		return -1
	}
	return c.limits[0]
}

type processorFunc func(ctx context.Context, b service.Batch) error

func (f processorFunc) Process(ctx context.Context, b service.Batch) error { return f(ctx, b) }

// done adapts a processor that never fails.
func done(f func(ctx context.Context, b service.Batch)) processorFunc {
	return func(ctx context.Context, b service.Batch) error { f(ctx, b); return nil }
}

// startBackground runs b and returns a stop function that cancels it and waits for Run to return.
func startBackground(t *testing.T, b *background) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Run(ctx)
	}()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("background.Run did not return after cancel")
		}
	}
	t.Cleanup(stop)
	return stop
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNotifyDoesNotBlock(t *testing.T) {
	b := newBackground(&fakeClaimer{}, done(func(context.Context, service.Batch) {}), backgroundConfig{})
	done := make(chan struct{})
	go func() {
		for range 1000 {
			b.Notify()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Notify blocks: nobody reads wake in this test")
	}
	if len(b.wake) != 1 {
		t.Fatalf("expected exactly one pending wake-up, got %d", len(b.wake))
	}
}

func TestNotifyWakesDispatcherBeforeTick(t *testing.T) {
	claimer := &fakeClaimer{}
	var processed atomic.Int32
	b := newBackground(claimer, done(func(context.Context, service.Batch) { processed.Add(1) }),
		backgroundConfig{PollInterval: time.Hour})
	startBackground(t, b)

	claimer.add("EUR/MXN")
	b.Notify()
	waitFor(t, "batch processed right after Notify, not after the hourly tick", func() bool { return processed.Load() == 1 })
}

func TestClaimIsLimitedByFreeChannelSpace(t *testing.T) {
	claimer := &fakeClaimer{}
	b := newBackground(claimer, done(func(context.Context, service.Batch) {}),
		backgroundConfig{BatchSize: 1000, PollInterval: time.Hour})
	startBackground(t, b)

	waitFor(t, "first claim", func() bool { return claimer.firstLimit() >= 0 })
	if got := claimer.firstLimit(); got != batchQueueSize {
		t.Fatalf("an empty channel fits %d batches, but the claim asked for %d", batchQueueSize, got)
	}
}

func TestSendGivesUpOnCancel(t *testing.T) {
	b := newBackground(&fakeClaimer{}, done(func(context.Context, service.Batch) {}), backgroundConfig{})
	for range cap(b.batches) {
		b.batches <- service.Batch{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := make(chan bool, 1)
	go func() { res <- b.send(ctx, service.Batch{Pair: "EUR/MXN"}) }()
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
	claimer := &fakeClaimer{}
	claimer.add("EUR/MXN", "EUR/USD", "EUR/GBP", "EUR/JPY")
	var processed atomic.Int32
	b := newBackground(claimer, done(func(context.Context, service.Batch) {
		time.Sleep(200 * time.Millisecond)
		processed.Add(1)
	}), backgroundConfig{Workers: 4, PollInterval: 10 * time.Millisecond})

	start := time.Now()
	startBackground(t, b)
	waitFor(t, "all processed", func() bool { return processed.Load() == 4 })
	if took := time.Since(start); took > 600*time.Millisecond {
		t.Fatalf("4 batches × 200ms with 4 workers took %v: they run one by one", took)
	}
}

func TestPanicDoesNotKillWorkers(t *testing.T) {
	claimer := &fakeClaimer{}
	claimer.add("EUR/MXN", "EUR/USD")
	var okDone atomic.Bool
	b := newBackground(claimer, done(func(_ context.Context, batch service.Batch) {
		if batch.Pair == "EUR/MXN" {
			panic("boom")
		}
		okDone.Store(true)
	}), backgroundConfig{Workers: 1, PollInterval: 10 * time.Millisecond})

	startBackground(t, b)
	waitFor(t, "second batch processed after a panic in the first", okDone.Load)
}

func TestFailedBatchDoesNotStopWorker(t *testing.T) {
	claimer := &fakeClaimer{}
	claimer.add("EUR/MXN", "EUR/USD")
	var okDone atomic.Bool
	b := newBackground(claimer, processorFunc(func(_ context.Context, batch service.Batch) error {
		if batch.Pair == "EUR/MXN" {
			return errors.New("complete updates of EUR/MXN: connection refused")
		}
		okDone.Store(true)
		return nil
	}), backgroundConfig{Workers: 1, PollInterval: 10 * time.Millisecond})

	startBackground(t, b)
	waitFor(t, "second batch processed after the first one failed", okDone.Load)
}

func TestRunStopsOnCancelWhileProcessorIsBusy(t *testing.T) {
	claimer := &fakeClaimer{}
	claimer.add("EUR/MXN")
	called := make(chan struct{})
	var once sync.Once
	b := newBackground(claimer, done(func(ctx context.Context, _ service.Batch) {
		once.Do(func() { close(called) })
		<-ctx.Done()
	}), backgroundConfig{PollInterval: 10 * time.Millisecond})

	stop := startBackground(t, b)
	<-called
	stop() // fails the test if Run doesn't return
}
