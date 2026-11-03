package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"

	"github.com/GlebHaik/currency-quotes/internal/domain"
)

//go:generate go tool mockgen -source=quote_updater.go -destination=mocks/quote_updater.go -package=mocks -typed -exclude_interfaces=retryAfterer

type UpdateStore interface {
	ClaimPending(ctx context.Context, limit int, lease time.Duration) ([]domain.UpdateOperation, error)
	CompleteUpdates(ctx context.Context, ids []uuid.UUID, q domain.Quote) error
	FailUpdates(ctx context.Context, ids []uuid.UUID, reason string) error
	RetryLater(ctx context.Context, ids []uuid.UUID, delay time.Duration) error
}

type RateProvider interface {
	Rate(ctx context.Context, pair domain.CurrencyPair) (domain.Rate, error)
}

// retryAfterer is an error carrying the delay the provider asked for, such as a 429 Retry-After.
type retryAfterer interface {
	RetryAfter() time.Duration
}

// Batch is one provider call: a currency pair and every pending update it will complete.
type Batch struct {
	Pair domain.CurrencyPair
	IDs  []uuid.UUID
	// Attempts is the highest attempt count among IDs, including the current one.
	Attempts int
}

// Failure reasons stored for clients. The provider's own error may carry URLs and network
// details, so it goes to the log only.
const (
	reasonUnsupportedPair     = "currency pair is not supported by the provider"
	reasonProviderUnavailable = "quote provider is unavailable, try again later"
)

type UpdaterConfig struct {
	// LockTTL must exceed channel wait + rate-limit wait + provider timeout,
	// or a lease expires mid-flight and the update is claimed twice.
	LockTTL      time.Duration
	MaxAttempts  int
	RetryBackoff time.Duration
	MaxBackoff   time.Duration
	// FinalizeTimeout bounds the write of a result that arrives during shutdown.
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

// QuoteUpdater is the background half of a refresh: claim pending updates, fetch prices,
// record outcomes. Goroutines and channels are the caller's business.
type QuoteUpdater struct {
	store    UpdateStore
	provider RateProvider
	cfg      UpdaterConfig
	log      *slog.Logger
	now      func() time.Time // replaced in tests
}

// NewQuoteUpdater uses slog.Default() when log is nil.
func NewQuoteUpdater(store UpdateStore, provider RateProvider, cfg UpdaterConfig, log *slog.Logger) *QuoteUpdater {
	if log == nil {
		log = slog.Default()
	}
	return &QuoteUpdater{store: store, provider: provider, cfg: cfg.withDefaults(), log: log, now: time.Now}
}

// Claim leases up to limit pending updates and groups them by pair:
// one provider call per pair, however many updates wait for it.
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
	pairs := make(map[domain.CurrencyPair]int)
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

// Process fetches the price for b and records the outcome: completed, failed, or retried later.
// A provider failure is an outcome, not an error. The returned error means the outcome could not
// be recorded; the updates then return to the queue when their lease expires.
func (u *QuoteUpdater) Process(ctx context.Context, b Batch) error {
	if ctx.Err() != nil {
		return nil
	}

	rate, err := u.provider.Rate(ctx, b.Pair)
	if err != nil && ctx.Err() != nil {
		// Cut short by shutdown, not by the provider: record nothing, so no attempt is wasted.
		return nil
	}
	return u.finish(ctx, b, rate, err)
}

func (u *QuoteUpdater) finish(ctx context.Context, b Batch, rate domain.Rate, err error) error {
	// Detached from ctx: a price fetched just before shutdown is still saved.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), u.cfg.FinalizeTimeout)
	defer cancel()

	log := u.log.With("pair", b.Pair.String(), "updates", len(b.IDs), "attempt", b.Attempts)

	switch {
	case err == nil:
		q := domain.Quote{CurrencyPair: b.Pair, Price: rate.Price, UpdatedAt: u.now().UTC(), SourceDate: rate.Date}
		if err := u.store.CompleteUpdates(ctx, b.IDs, q); err != nil {
			return fmt.Errorf("complete updates of %s: %w", b.Pair, err)
		}
		log.Info("quote updated", "price", rate.Price)
	case errors.Is(err, domain.ErrPermanent) || b.Attempts >= u.cfg.MaxAttempts:
		reason := reasonProviderUnavailable
		if errors.Is(err, domain.ErrPermanent) {
			reason = reasonUnsupportedPair
		}
		if err := u.store.FailUpdates(ctx, b.IDs, reason); err != nil {
			return fmt.Errorf("fail updates of %s: %w", b.Pair, err)
		}
		log.Warn("updates failed", "reason", reason, "err", err)
	default:
		delay := u.backoff(b.Attempts, err)
		if err := u.store.RetryLater(ctx, b.IDs, delay); err != nil {
			return fmt.Errorf("retry updates of %s: %w", b.Pair, err)
		}
		log.Warn("provider call failed, retrying", "in", delay, "err", err)
	}
	return nil
}

// backoff is exponential with jitter, capped at MaxBackoff, and never shorter than the
// provider's Retry-After. attempt starts at 1.
func (u *QuoteUpdater) backoff(attempt int, err error) time.Duration {
	ceiling := u.cfg.MaxBackoff
	attempt = max(1, attempt)

	if attempt <= 62 { // a longer shift overflows int64
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

// jitter returns a random delay in [max/2, max), so retries of many updates spread out.
func jitter(max time.Duration) time.Duration {
	if max < 2 {
		return max
	}

	half := max / 2
	return half + rand.N(half)
}
