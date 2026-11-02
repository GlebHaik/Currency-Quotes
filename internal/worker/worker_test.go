package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/GlebHaik/currency-quotes/internal/domain"
	"github.com/GlebHaik/currency-quotes/internal/provider"
	"github.com/GlebHaik/currency-quotes/internal/repository"
)

var _ Store = (*repository.QuoteRepository)(nil)

type fakeStore struct {
	mu      sync.Mutex
	rows    map[string]*fakeRow
	order   []string
	retried map[string]time.Duration
	claimed int
}

type fakeRow struct {
	op          domain.UpdateOperation
	lockedUntil time.Time
}

func newFakeStore(pairs ...string) *fakeStore {
	s := &fakeStore{rows: map[string]*fakeRow{}, retried: map[string]time.Duration{}}
	for _, p := range pairs {
		s.add(p, 0)
	}
	return s
}

func (s *fakeStore) add(pair string, attempts int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := uuid.New()
	s.rows[id.String()] = &fakeRow{op: domain.UpdateOperation{
		ID: id, CurrencyPair: pair, Status: domain.StatusPending, Attempts: attempts, CreatedAt: time.Now(),
	}}
	s.order = append(s.order, id.String())
	return id.String()
}

func (s *fakeStore) ClaimPending(ctx context.Context, limit int, lease time.Duration) ([]domain.UpdateOperation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.UpdateOperation
	now := time.Now()
	for _, id := range s.order {
		r := s.rows[id]
		if len(out) == limit {
			break
		}
		if r.op.Status != domain.StatusPending || now.Before(r.lockedUntil) {
			continue
		}
		r.lockedUntil = now.Add(lease)
		r.op.Attempts++
		s.claimed++
		out = append(out, r.op)
	}
	return out, nil
}

func (s *fakeStore) CompleteUpdates(ctx context.Context, ids []string, q domain.Quote) error {
	return s.set(ctx, ids, func(r *fakeRow) {
		r.op.Status = domain.StatusCompleted
		r.op.Quote = &q
	})
}

func (s *fakeStore) FailUpdates(ctx context.Context, ids []string, reason string) error {
	return s.set(ctx, ids, func(r *fakeRow) {
		r.op.Status = domain.StatusFailed
		r.op.Error = reason
	})
}

func (s *fakeStore) RetryLater(ctx context.Context, ids []string, delay time.Duration) error {
	return s.set(ctx, ids, func(r *fakeRow) {
		r.lockedUntil = time.Now().Add(delay)
		s.retried[r.op.ID.String()] = delay
	})
}

func (s *fakeStore) set(ctx context.Context, ids []string, f func(*fakeRow)) error {
	// Like a real DB driver: a cancelled context fails write.
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		f(s.rows[id])
	}
	return nil
}

func (s *fakeStore) get(id string) domain.UpdateOperation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[id].op
}

func (s *fakeStore) retryDelay(id string) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.retried[id]
	return d, ok
}

type providerFunc func(ctx context.Context, base, quote string) (string, error)

func (f providerFunc) Rate(ctx context.Context, base, quote string) (provider.Rate, error) {
	price, err := f(ctx, base, quote)
	return provider.Rate{Price: price, Date: testDate}, err
}

var testDate = time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)

func fixedPrice(price string) providerFunc {
	return func(context.Context, string, string) (string, error) { return price, nil }
}

// startRunner runs the pipeline in the background and returns a stop function that waits for it.
func startRunner(t *testing.T, store Store, provider CurrencyProvider, cfg Config) (*JobQueue, func()) {
	t.Helper()
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 10 * time.Millisecond
	}
	queue := NewJobQueue()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(store, provider, queue, cfg).Run(ctx)
	}()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Runner.Run did not return after cancel")
		}
	}
	t.Cleanup(stop)
	return queue, stop
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

func TestPendingUpdateIsCompleted(t *testing.T) {
	store := newFakeStore()
	id := store.add("EUR/MXN", 0)

	var gotBase, gotQuote string
	var mu sync.Mutex
	fake := providerFunc(func(_ context.Context, base, quote string) (string, error) {
		mu.Lock()
		gotBase, gotQuote = base, quote
		mu.Unlock()
		return "20.955", nil
	})
	startRunner(t, store, fake, Config{})

	waitFor(t, "completed", func() bool { return store.get(id).Status == domain.StatusCompleted })
	op := store.get(id)
	if op.Quote == nil || op.Quote.Price != "20.955" || op.Quote.CurrencyPair != "EUR/MXN" || !op.Quote.SourceDate.Equal(testDate) {
		t.Fatalf("unexpected quote: %+v", op.Quote)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotBase != "EUR" || gotQuote != "MXN" {
		t.Fatalf("provider called with %s/%s", gotBase, gotQuote)
	}
}

func TestPermanentErrorFailsUpdate(t *testing.T) {
	store := newFakeStore()
	id := store.add("EUR/XXX", 0)
	fake := providerFunc(func(context.Context, string, string) (string, error) {
		return "", &provider.Error{StatusCode: 422, Message: "invalid currency: XXX", Permanent: true}
	})
	startRunner(t, store, fake, Config{})

	waitFor(t, "failed", func() bool { return store.get(id).Status == domain.StatusFailed })
	if op := store.get(id); op.Error == "" || op.Attempts != 1 {
		t.Fatalf("expected one attempt with an error message, got %+v", op)
	}
}

func TestTransientErrorIsRetriedThenFails(t *testing.T) {
	store := newFakeStore()
	retryID := store.add("EUR/MXN", 0)
	lastID := store.add("EUR/USD", 2) // next claim is attempt 3 == MaxAttempts
	fake := providerFunc(func(context.Context, string, string) (string, error) {
		return "", &provider.Error{StatusCode: 503, Message: "unavailable"}
	})
	startRunner(t, store, fake, Config{MaxAttempts: 3, RetryBackoff: time.Hour, MaxBackoff: time.Hour})

	waitFor(t, "retry scheduled", func() bool { _, ok := store.retryDelay(retryID); return ok })
	if op := store.get(retryID); op.Status != domain.StatusPending {
		t.Fatalf("retried update must stay pending, got %s", op.Status)
	}
	waitFor(t, "last attempt failed", func() bool { return store.get(lastID).Status == domain.StatusFailed })
}

func TestRunStopsOnCancelWhileProviderIsBusy(t *testing.T) {
	store := newFakeStore("EUR/MXN")
	called := make(chan struct{})
	var once sync.Once
	fake := providerFunc(func(ctx context.Context, _, _ string) (string, error) {
		once.Do(func() { close(called) })
		<-ctx.Done()
		return "", ctx.Err()
	})
	_, stop := startRunner(t, store, fake, Config{})

	<-called
	stop()
	if op := store.get(store.order[0]); op.Status != domain.StatusPending {
		t.Fatalf("update interrupted by shutdown must stay pending, got %s", op.Status)
	}
}

func TestMalformedPairFails(t *testing.T) {
	store := newFakeStore()
	id := store.add("EURMXN", 0)
	startRunner(t, store, fixedPrice("1"), Config{})
	waitFor(t, "failed", func() bool { return store.get(id).Status == domain.StatusFailed })
	if !errors.Is(&provider.Error{Permanent: true}, provider.ErrPermanent) {
		t.Fatal("provider.Error with Permanent must match provider.ErrPermanent")
	}
}
