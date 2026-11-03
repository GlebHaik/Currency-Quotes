package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/GlebHaik/currency-quotes/internal/service"
)

const batchQueueSize = 64

// batchClaimer is what the dispatcher needs; *service.QuoteUpdater satisfies it.
type batchClaimer interface {
	Claim(ctx context.Context, limit int) ([]service.Batch, error)
}

// batchProcessor is what the workers need; *service.QuoteUpdater satisfies it.
type batchProcessor interface {
	Process(ctx context.Context, b service.Batch) error
}

type backgroundConfig struct {
	Workers      int
	PollInterval time.Duration
	BatchSize    int
}

func (c backgroundConfig) withDefaults() backgroundConfig {
	if c.Workers <= 0 {
		c.Workers = 4
	}
	if c.PollInterval <= 0 {
		c.PollInterval = time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 100
	}
	return c
}

// background runs quote updates outside the HTTP requests:
//
//	QuoteService --Notify--> dispatcher --batches--> workers --> QuoteUpdater.Process
//
// It only owns the concurrency: when to poll, how many batches to buffer, how many workers.
// Postgres stays the source of truth: anything lost from a channel is still pending in the table.
type background struct {
	claimer   batchClaimer
	processor batchProcessor
	cfg       backgroundConfig
	batches   chan service.Batch
	wake      chan struct{}
}

func newBackground(claimer batchClaimer, processor batchProcessor, cfg backgroundConfig) *background {
	return &background{
		claimer:   claimer,
		processor: processor,
		cfg:       cfg.withDefaults(),
		batches:   make(chan service.Batch, batchQueueSize),
		// Buffer of one: any number of Notify calls between two polls collapse into one wake-up.
		wake: make(chan struct{}, 1),
	}
}

// Notify is called by QuoteService right after a pending update is saved.
// It only asks the dispatcher to poll now instead of waiting for the next tick, and never blocks.
func (b *background) Notify() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// Run blocks until ctx is cancelled and every worker has finished its batch.
func (b *background) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := range b.cfg.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.work(ctx, i+1)
		}()
	}

	b.dispatch(ctx)
	wg.Wait()
}

// dispatch polls until ctx is cancelled. It is the only sender on b.batches, so it closes it,
// which lets the workers drain the channel and exit.
func (b *background) dispatch(ctx context.Context) {
	defer close(b.batches)

	ticker := time.NewTicker(b.cfg.PollInterval)
	defer ticker.Stop()

	for {
		b.claim(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-b.wake:
		}
	}
}

func (b *background) claim(ctx context.Context) {
	// Claim no more than the channel can take right now: a leased update that waits too long
	// in the channel may outlive its lease and be claimed twice.
	free := cap(b.batches) - len(b.batches)
	batches, err := b.claimer.Claim(ctx, min(b.cfg.BatchSize, free))
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("claim pending updates", "err", err)
		}
		return
	}

	for _, batch := range batches {
		if !b.send(ctx, batch) {
			// Shutting down: the rest stay leased in Postgres and return to the queue on their own.
			return
		}
	}
}

func (b *background) send(ctx context.Context, batch service.Batch) bool {
	select {
	case <-ctx.Done():
		return false
	case b.batches <- batch:
	}
	return true
}

func (b *background) work(ctx context.Context, id int) {
	for batch := range b.batches {
		b.handle(ctx, id, batch)
	}
}

// handle keeps a failed or panicking batch from stopping the worker; the batch's updates
// stay leased and return to the queue when the lease expires.
func (b *background) handle(ctx context.Context, id int, batch service.Batch) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in worker", "worker", id, "pair", batch.Pair, "panic", r)
		}
	}()
	if err := b.processor.Process(ctx, batch); err != nil {
		slog.Error("process batch", "worker", id, "pair", batch.Pair, "err", err)
	}
}
