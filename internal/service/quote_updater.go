package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/GlebHaik/currency-quotes/internal/domain"
)

//go:generate go tool mockgen -source=quote_updater.go -destination=mocks/quote_updater.go -package=mocks -typed -exclude_interfaces=retryAfterer

// UpdateStore is the update queue in Postgres; *repository.QuoteRepository satisfies it.
type UpdateStore interface {
	ClaimPending(ctx context.Context, limit int, lease time.Duration) ([]domain.UpdateOperation, error)
	CompleteUpdates(ctx context.Context, ids []uuid.UUID, q domain.Quote) error
	FailUpdates(ctx context.Context, ids []uuid.UUID, reason string) error
	RetryLater(ctx context.Context, ids []uuid.UUID, delay time.Duration) error
}

// RateProvider is the external source of prices; *frankfurter.Client satisfies it.
type RateProvider interface {
	Rate(ctx context.Context, base, quoteCurrency string) (domain.Rate, error)
}

// retryAfterer is an error that carries the delay the provider asked for, such as a 429 Retry-After.
type retryAfterer interface {
	RetryAfter() time.Duration
}

// Batch is one provider call: a currency pair and every pending update it will complete.
type Batch struct {
	Pair string
	IDs  []uuid.UUID
	// Attempts is the highest attempt count among IDs, including the current one.
	Attempts int
}

type UpdaterConfig struct {
	// LockTTL > channel wait + rate-limit wait + provider timeout, else the lease expires mid-flight and the update is claimed twice.
	LockTTL      time.Duration
	MaxAttempts  int
	RetryBackoff time.Duration
	MaxBackoff   time.Duration
	// FinalizeTimeout bounds the DB write that records a result while the service shuts down.
	FinalizeTimeout time.Duration
}

func (c UpdaterConfig) withDefaults() UpdaterConfig {
	if c.LockTTL <= 0 {
		c.LockTTL = 30 * time.Second
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 3
	}
	if c.RetryBackoff <= 0 {
		c.RetryBackoff = 2 * time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = time.Minute
	}
	if c.FinalizeTimeout <= 0 {
		c.FinalizeTimeout = 5 * time.Second
	}
	return c
}

// QuoteUpdater does the background part of a refresh: it leases pending updates,
// asks the provider for the price and records the outcome. It knows nothing about
// goroutines or channels; the caller decides when and how concurrently to run it.
type QuoteUpdater struct {
	store    UpdateStore
	provider RateProvider
	cfg      UpdaterConfig
	log      *slog.Logger
	// now is the clock for UpdatedAt; tests replace it to get exact times.
	now func() time.Time
}

// NewQuoteUpdater builds an updater; a nil log means slog.Default().
func NewQuoteUpdater(store UpdateStore, provider RateProvider, cfg UpdaterConfig, log *slog.Logger) *QuoteUpdater {
	if log == nil {
		log = slog.Default()
	}
	return &QuoteUpdater{store: store, provider: provider, cfg: cfg.withDefaults(), log: log, now: time.Now}
}

// Claim leases up to limit pending updates and groups them by pair,
// so every pair costs one provider call however many updates wait for it.
func (u *QuoteUpdater) Claim(ctx context.Context, limit int) ([]Batch, error) {
	if limit <= 0 {
		return nil, nil
	}
	updates, err := u.store.ClaimPending(ctx, limit, u.cfg.LockTTL)
	if err != nil {
		return nil, fmt.Errorf("claim pending updates: %w", err)
	}
	return groupByPair(updates), nil
}

func groupByPair(updates []domain.UpdateOperation) []Batch {
	batches := make([]Batch, 0, len(updates))
	pairs := make(map[string]int)
	for _, u := range updates {
		i, ok := pairs[u.CurrencyPair]
		if !ok {
			i = len(batches)
			pairs[u.CurrencyPair] = i
			batches = append(batches, Batch{Pair: u.CurrencyPair})
		}
		batches[i].IDs = append(batches[i].IDs, u.ID)
		batches[i].Attempts = max(batches[i].Attempts, u.Attempts)
	}
	return batches
}

// Process fetches the price for b and records the outcome: completed, failed for good, or retried later.
// A provider failure is an outcome, not an error: Process returns an error only when the outcome
// could not be recorded, and the updates then return to the queue when their lease expires.
func (u *QuoteUpdater) Process(ctx context.Context, b Batch) error {
	if ctx.Err() != nil {
		return nil
	}

	base, quoteCurrency, ok := strings.Cut(b.Pair, "/")
	if !ok {
		return u.finish(ctx, b, domain.Rate{}, fmt.Errorf("%w: malformed currency pair %q", domain.ErrPermanent, b.Pair))
	}

	rate, err := u.provider.Rate(ctx, base, quoteCurrency)
	if err != nil && ctx.Err() != nil {
		// Cut short by shutdown, not by the provider: leave the updates leased and record nothing.
		return nil
	}
	return u.finish(ctx, b, rate, err)
}

func (u *QuoteUpdater) finish(ctx context.Context, b Batch, rate domain.Rate, err error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), u.cfg.FinalizeTimeout)
	defer cancel()

	switch {
	case err == nil:
		q := domain.Quote{CurrencyPair: b.Pair, Price: rate.Price, UpdatedAt: u.now().UTC(), SourceDate: rate.Date}
		if err := u.store.CompleteUpdates(ctx, b.IDs, q); err != nil {
			return fmt.Errorf("complete updates of %s: %w", b.Pair, err)
		}
	case errors.Is(err, domain.ErrPermanent) || b.Attempts >= u.cfg.MaxAttempts:
		if err := u.store.FailUpdates(ctx, b.IDs, err.Error()); err != nil {
			return fmt.Errorf("fail updates of %s: %w", b.Pair, err)
		}
	default:
		delay := u.backoff(b.Attempts, err)
		u.log.Warn("provider call failed, retrying", "pair", b.Pair, "attempt", b.Attempts, "in", delay, "err", err)
		if err := u.store.RetryLater(ctx, b.IDs, delay); err != nil {
			return fmt.Errorf("retry updates of %s: %w", b.Pair, err)
		}
	}
	return nil
}

// attempt starts at 1
func (u *QuoteUpdater) backoff(attempt int, err error) time.Duration {
	ceiling := u.cfg.MaxBackoff
	attempt = max(1, attempt)

	if attempt <= 62 {
		factor := int64(1) << (attempt - 1)
		if int64(u.cfg.MaxBackoff)/factor >= int64(u.cfg.RetryBackoff) {
			ceiling = time.Duration(factor) * u.cfg.RetryBackoff
		}
	}

	delay := jitter(ceiling)

	var ra retryAfterer
	if errors.As(err, &ra) && ra.RetryAfter() > delay {
		delay = ra.RetryAfter()
	}

	return delay
}

func jitter(max time.Duration) time.Duration {
	if max < 2 {
		return max
	}

	half := max / 2
	return half + rand.N(half)
}
