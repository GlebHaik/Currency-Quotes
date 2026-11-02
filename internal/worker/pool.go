package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/GlebHaik/currency-quotes/internal/domain"
	"github.com/GlebHaik/currency-quotes/internal/provider"
)

// Need to revise
const finalizeTimeout = 5 * time.Second

// WorkerPool reads jobs from the channel and calls the provider
type WorkerPool struct {
	queue    *JobQueue
	provider CurrencyProvider
	store    Store
	cfg      Config
}

func NewWorkerPool(queue *JobQueue, provider CurrencyProvider, store Store, cfg Config) *WorkerPool {
	return &WorkerPool{queue: queue, provider: provider, store: store, cfg: cfg}
}

func (p *WorkerPool) Run(ctx context.Context) {
	wg := &sync.WaitGroup{}

	for i := range p.cfg.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.work(ctx, i+1)
		}()
	}

	wg.Wait()
}

func (p *WorkerPool) work(ctx context.Context, id int) {
	for job := range p.queue.jobs {
		p.handle(ctx, id, job)
	}
}

func (p *WorkerPool) handle(ctx context.Context, id int, job Job) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in handle", "id", id, "pair", job.Pair, r)
		}
	}()
	p.process(ctx, job)
}

func (p *WorkerPool) process(ctx context.Context, job Job) {
	if ctx.Err() != nil {
		return
	}

	base, quote, ok := strings.Cut(job.Pair, "/")
	if !ok {
		p.finish(ctx, job, provider.Rate{}, fmt.Errorf("%w: malformed currency pair %q", provider.ErrPermanent, job.Pair))
		return
	}

	rate, err := p.provider.Rate(ctx, base, quote)
	if err != nil && ctx.Err() != nil {
		return
	}
	p.finish(ctx, job, rate, err)
}

func (p *WorkerPool) finish(ctx context.Context, job Job, rate provider.Rate, err error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalizeTimeout)
	defer cancel()

	switch {
	case err == nil:
		q := domain.Quote{CurrencyPair: job.Pair, Price: rate.Price, UpdatedAt: time.Now().UTC(), SourceDate: rate.Date}
		if err := p.store.CompleteUpdates(ctx, job.IDs, q); err != nil {
			slog.Error("complete updates", "pair", job.Pair, "err", err)
		}
	case errors.Is(err, provider.ErrPermanent) || job.Attempts >= p.cfg.MaxAttempts:
		if err := p.store.FailUpdates(ctx, job.IDs, err.Error()); err != nil {
			slog.Error("fail updates", "pair", job.Pair, "err", err)
		}
	default:
		delay := p.backoff(job.Attempts, err)
		slog.Warn("provider call failed, retrying", "pair", job.Pair, "attempt", job.Attempts, "in", delay, "err", err)
		if err := p.store.RetryLater(ctx, job.IDs, delay); err != nil {
			slog.Error("retry updates", "pair", job.Pair, "err", err)
		}
	}
}

// attempt starts at 1
func (p *WorkerPool) backoff(attempt int, err error) time.Duration {
	ceiling := p.cfg.MaxBackoff
	attempt = max(1, attempt)

	if attempt <= 62 {
		factor := int64(1) << (attempt - 1)
		if int64(p.cfg.MaxBackoff)/factor >= int64(p.cfg.RetryBackoff) {
			ceiling = time.Duration(factor) * p.cfg.RetryBackoff
		}
	}

	delay := jitter(ceiling)

	var perr *provider.Error
	if errors.As(err, &perr) && perr.RetryAfter > delay {
		delay = perr.RetryAfter
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
