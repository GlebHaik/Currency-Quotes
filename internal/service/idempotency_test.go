package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/GlebHaik/currency-quotes/internal/domain"
)

// fakeRepo behaves like the table with a unique index on idempotency_key.
type fakeRepo struct {
	mu    sync.Mutex
	byKey map[string]domain.UpdateOperation
}

func (r *fakeRepo) SaveUpdate(_ context.Context, u domain.UpdateOperation) (domain.UpdateOperation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if u.IdempotencyKey == "" {
		return u, nil
	}
	if existing, ok := r.byKey[u.IdempotencyKey]; ok {
		return existing, nil
	}
	r.byKey[u.IdempotencyKey] = u
	return u, nil
}

func (r *fakeRepo) GetUpdate(context.Context, uuid.UUID) (domain.UpdateOperation, error) {
	return domain.UpdateOperation{}, domain.ErrNotFound
}

func (r *fakeRepo) GetLatestQuote(context.Context, string) (domain.Quote, error) {
	return domain.Quote{}, domain.ErrNotFound
}

type fakeNotifier struct{ notified atomic.Int32 }

func (n *fakeNotifier) Notify() { n.notified.Add(1) }

func newTestService() (*QuoteService, *fakeNotifier) {
	q := &fakeNotifier{}
	return NewQuoteService(&fakeRepo{byKey: map[string]domain.UpdateOperation{}}, q), q
}

func TestIdempotency_SameKeyReturnsSameID(t *testing.T) {
	s, q := newTestService()
	ctx := context.Background()

	first, err := s.RequestUpdate(ctx, "EUR/MXN", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RequestUpdate(ctx, "EUR/MXN", "key-1")
	if err != nil {
		t.Fatalf("a retry with the same key and pair must succeed, got %v", err)
	}
	if first != second {
		t.Fatalf("a retry must return the original updateId %s, got %s", first, second)
	}
	if n := q.notified.Load(); n != 1 {
		t.Fatalf("a retry must have no side effects, but the workers were notified %d times", n)
	}
}

func TestIdempotency_SameKeyOtherPairConflicts(t *testing.T) {
	s, _ := newTestService()
	ctx := context.Background()

	if _, err := s.RequestUpdate(ctx, "EUR/MXN", "key-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestUpdate(ctx, "EUR/USD", "key-1"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("the key was reused for another pair: want ErrIdempotencyConflict, got %v", err)
	}
}

func TestIdempotency_NoKeyCreatesNewUpdates(t *testing.T) {
	s, q := newTestService()
	ctx := context.Background()

	first, err := s.RequestUpdate(ctx, "EUR/MXN", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RequestUpdate(ctx, "EUR/MXN", "")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("without a key every request is a new update")
	}
	if n := q.notified.Load(); n != 2 {
		t.Fatalf("workers must be notified about both updates, got %d", n)
	}
}

func TestIdempotency_KeyTooLong(t *testing.T) {
	s, _ := newTestService()
	if _, err := s.RequestUpdate(context.Background(), "EUR/MXN", strings.Repeat("k", 256)); !errors.Is(err, ErrInvalidIdempotencyKey) {
		t.Fatalf("a 256-char key must be rejected with ErrInvalidIdempotencyKey, got %v", err)
	}
	if _, err := s.RequestUpdate(context.Background(), "EUR/MXN", strings.Repeat("k", 255)); err != nil {
		t.Fatalf("a 255-char key is allowed, got %v", err)
	}
}
