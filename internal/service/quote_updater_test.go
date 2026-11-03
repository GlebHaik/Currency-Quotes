package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/GlebHaik/currency-quotes/internal/clients/frankfurter"
	"github.com/GlebHaik/currency-quotes/internal/domain"
	"github.com/GlebHaik/currency-quotes/internal/repository"
	"github.com/GlebHaik/currency-quotes/internal/service/mocks"
)

var (
	_ UpdateStore  = (*repository.QuoteRepository)(nil)
	_ RateProvider = (*frankfurter.Client)(nil)
)

var (
	testDate = time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	eurXXX   = domain.MustParseCurrencyPair("EUR/XXX")
)

type updaterMocks struct {
	store    *mocks.MockUpdateStore
	provider *mocks.MockRateProvider
}

func newTestUpdater(t *testing.T, cfg UpdaterConfig) (*QuoteUpdater, updaterMocks) {
	ctrl := gomock.NewController(t)
	m := updaterMocks{store: mocks.NewMockUpdateStore(ctrl), provider: mocks.NewMockRateProvider(ctrl)}
	u := NewQuoteUpdater(m.store, m.provider, cfg, slog.New(slog.DiscardHandler))
	u.now = func() time.Time { return testNow }
	return u, m
}

// retryAfterErr stands in for a provider error with a 429 Retry-After.
type retryAfterErr time.Duration

func (e retryAfterErr) Error() string             { return "rate limited (HTTP 429)" }
func (e retryAfterErr) RetryAfter() time.Duration { return time.Duration(e) }

func TestQuoteUpdater_Claim(t *testing.T) {
	op := func(pair domain.CurrencyPair, attempts int) domain.UpdateOperation {
		return domain.UpdateOperation{ID: uuid.New(), CurrencyPair: pair, Attempts: attempts}
	}
	mxn1, usd, mxn3, mxn2 := op(eurMXN, 1), op(eurUSD, 1), op(eurMXN, 3), op(eurMXN, 2)
	errDB := errors.New("connection refused")
	const lease = 45 * time.Second

	tests := []struct {
		name    string
		limit   int
		setup   func(m updaterMocks)
		want    []Batch
		wantErr error
	}{
		{
			name:  "updates of one pair share one batch with their highest attempt",
			limit: 100,
			setup: func(m updaterMocks) {
				m.store.EXPECT().ClaimPending(gomock.Any(), 100, lease).
					Return([]domain.UpdateOperation{mxn1, usd, mxn3, mxn2}, nil)
			},
			want: []Batch{
				{Pair: eurMXN, IDs: []uuid.UUID{mxn1.ID, mxn3.ID, mxn2.ID}, Attempts: 3},
				{Pair: eurUSD, IDs: []uuid.UUID{usd.ID}, Attempts: 1},
			},
		},
		{
			name:  "no room claims nothing and leases nothing",
			limit: 0,
			setup: func(updaterMocks) {},
		},
		{
			name:  "storage error is returned",
			limit: 10,
			setup: func(m updaterMocks) {
				m.store.EXPECT().ClaimPending(gomock.Any(), 10, lease).Return(nil, errDB)
			},
			wantErr: errDB,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, m := newTestUpdater(t, UpdaterConfig{LockTTL: lease})
			tt.setup(m)

			got, err := u.Claim(context.Background(), tt.limit)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestQuoteUpdater_Process(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New()}
	cfg := UpdaterConfig{MaxAttempts: 3, RetryBackoff: time.Second, MaxBackoff: 10 * time.Second}
	unavailable := errors.New("unavailable (HTTP 503)")
	errDB := errors.New("connection refused")

	savedQuote := gomock.Cond(func(q domain.Quote) bool {
		return q.CurrencyPair == eurMXN && q.Price == "20.955" && q.SourceDate.Equal(testDate) && q.UpdatedAt.Equal(testNow)
	})
	delayIn := func(lo, hi time.Duration) gomock.Matcher {
		return gomock.Cond(func(d time.Duration) bool { return d >= lo && d <= hi })
	}
	reasonHas := func(s string) gomock.Matcher {
		return gomock.Cond(func(reason string) bool { return strings.Contains(reason, s) })
	}
	// alive checks the store write is not cut short by a cancelled request context.
	alive := gomock.Cond(func(ctx context.Context) bool { return ctx.Err() == nil })

	tests := []struct {
		name  string
		batch Batch
		// cancelFirst cancels ctx before Process starts, like a shutdown that came first.
		cancelFirst bool
		// setup gets cancel to simulate a shutdown in the middle of Process.
		setup func(m updaterMocks, cancel context.CancelFunc)
		// wantErr: Process could not record the outcome.
		wantErr error
	}{
		{
			name:  "price is saved for every update of the pair",
			batch: Batch{Pair: eurMXN, IDs: ids, Attempts: 1},
			setup: func(m updaterMocks, _ context.CancelFunc) {
				m.provider.EXPECT().Rate(gomock.Any(), eurMXN).Return(domain.Rate{Price: "20.955", Date: testDate}, nil)
				m.store.EXPECT().CompleteUpdates(gomock.Any(), ids, savedQuote).Return(nil)
			},
		},
		{
			name:  "permanent error fails the updates at once",
			batch: Batch{Pair: eurXXX, IDs: ids, Attempts: 1},
			setup: func(m updaterMocks, _ context.CancelFunc) {
				m.provider.EXPECT().Rate(gomock.Any(), eurXXX).
					Return(domain.Rate{}, fmt.Errorf("%w: invalid currency: XXX", domain.ErrPermanent))
				m.store.EXPECT().FailUpdates(gomock.Any(), ids, reasonHas("invalid currency: XXX")).Return(nil)
			},
		},
		{
			name:  "transient error is retried with a backoff within the first ceiling",
			batch: Batch{Pair: eurMXN, IDs: ids, Attempts: 1},
			setup: func(m updaterMocks, _ context.CancelFunc) {
				m.provider.EXPECT().Rate(gomock.Any(), eurMXN).Return(domain.Rate{}, unavailable)
				m.store.EXPECT().RetryLater(gomock.Any(), ids, delayIn(time.Nanosecond, time.Second)).Return(nil)
			},
		},
		{
			name:  "transient error on the last attempt fails the updates",
			batch: Batch{Pair: eurMXN, IDs: ids, Attempts: 3},
			setup: func(m updaterMocks, _ context.CancelFunc) {
				m.provider.EXPECT().Rate(gomock.Any(), eurMXN).Return(domain.Rate{}, unavailable)
				m.store.EXPECT().FailUpdates(gomock.Any(), ids, reasonHas("503")).Return(nil)
			},
		},
		{
			name:  "Retry-After from the provider is respected",
			batch: Batch{Pair: eurMXN, IDs: ids, Attempts: 1},
			setup: func(m updaterMocks, _ context.CancelFunc) {
				m.provider.EXPECT().Rate(gomock.Any(), eurMXN).
					Return(domain.Rate{}, fmt.Errorf("frankfurter: %w", retryAfterErr(30*time.Second)))
				m.store.EXPECT().RetryLater(gomock.Any(), ids, delayIn(30*time.Second, time.Hour)).Return(nil)
			},
		},
		{
			name:  "store error is returned, so the caller can log it",
			batch: Batch{Pair: eurMXN, IDs: ids, Attempts: 1},
			setup: func(m updaterMocks, _ context.CancelFunc) {
				m.provider.EXPECT().Rate(gomock.Any(), eurMXN).Return(domain.Rate{Price: "20.955", Date: testDate}, nil)
				m.store.EXPECT().CompleteUpdates(gomock.Any(), ids, savedQuote).Return(errDB)
			},
			wantErr: errDB,
		},
		{
			name:        "shutdown before start does nothing",
			batch:       Batch{Pair: eurMXN, IDs: ids, Attempts: 1},
			cancelFirst: true,
			setup:       func(updaterMocks, context.CancelFunc) {},
		},
		{
			name:  "shutdown during the provider call writes nothing, so no attempt is wasted on a retry",
			batch: Batch{Pair: eurMXN, IDs: ids, Attempts: 1},
			setup: func(m updaterMocks, cancel context.CancelFunc) {
				m.provider.EXPECT().Rate(gomock.Any(), eurMXN).
					DoAndReturn(func(ctx context.Context, _ domain.CurrencyPair) (domain.Rate, error) {
						cancel()
						return domain.Rate{}, ctx.Err()
					})
			},
		},
		{
			name:  "price fetched right before shutdown is still saved",
			batch: Batch{Pair: eurMXN, IDs: ids, Attempts: 1},
			setup: func(m updaterMocks, cancel context.CancelFunc) {
				m.provider.EXPECT().Rate(gomock.Any(), eurMXN).
					DoAndReturn(func(context.Context, domain.CurrencyPair) (domain.Rate, error) {
						cancel()
						return domain.Rate{Price: "20.955", Date: testDate}, nil
					})
				m.store.EXPECT().CompleteUpdates(alive, ids, savedQuote).Return(nil)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, m := newTestUpdater(t, cfg)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tt.setup(m, cancel)
			if tt.cancelFirst {
				cancel()
			}

			// Any call the setup did not expect fails the test through the gomock controller.
			if err := u.Process(ctx, tt.batch); !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestExponentialBackoffWithJitter(t *testing.T) {
	u := &QuoteUpdater{cfg: UpdaterConfig{RetryBackoff: time.Second, MaxBackoff: 10 * time.Second}}
	plain := errors.New("unavailable (HTTP 503)")

	for attempt := 1; attempt <= 6; attempt++ {
		ceiling := min(time.Second<<(attempt-1), 10*time.Second)
		var biggest time.Duration
		for range 200 {
			d := u.backoff(attempt, plain)
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
	first := u.backoff(4, plain)
	for range 20 {
		if u.backoff(4, plain) != first {
			same = false
		}
	}
	if same {
		t.Fatal("no jitter: every update would retry at the same moment")
	}

	if d := u.backoff(1, retryAfterErr(30*time.Second)); d < 30*time.Second {
		t.Fatalf("Retry-After of 30s must be respected, got %v", d)
	}
	if d := u.backoff(10, fmt.Errorf("frankfurter: %w", retryAfterErr(30*time.Second))); d < 30*time.Second {
		t.Fatalf("Retry-After of 30s must be respected even when the backoff is capped, got %v", d)
	}

	// Network errors and timeouts carry no Retry-After, but they are retried too.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("backoff panics on an error without Retry-After: %v", r)
		}
	}()
	netErr := errors.New("frankfurter: dial tcp: connection refused")
	for _, attempt := range []int{1, 3, 20, 100} {
		if d := u.backoff(attempt, netErr); d <= 0 || d > 10*time.Second {
			t.Fatalf("attempt %d with a network error: delay %v outside (0, 10s]", attempt, d)
		}
	}
}
