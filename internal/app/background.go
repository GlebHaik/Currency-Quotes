package app

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/GlebHaik/currency-quotes/internal/service"
)

type batchClaimer interface {
	Claim(ctx context.Context, limit int) ([]service.Batch, error)
}

type batchProcessor interface {
	Process(ctx context.Context, b service.Batch) error
}

type backgroundConfig struct {
	Workers      int
	PollInterval time.Duration
	BatchSize    int
	QueueSize    int
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
	if c.QueueSize <= 0 {
		c.QueueSize = 64
	}
	return c
}

// background runs quote updates outside HTTP requests and owns only the concurrency:
//
//	QuoteService --Notify--> dispatcher --batches--> workers --> QuoteUpdater.Process
//
// Postgres stays the source of truth: anything lost from the channels is still pending in the table.
type background struct {
	claimer   batchClaimer
	processor batchProcessor
	cfg       backgroundConfig
	log       *slog.Logger
	batches   chan service.Batch
	wake      chan struct{}
}

func newBackground(claimer batchClaimer, processor batchProcessor, cfg backgroundConfig, log *slog.Logger) *background {
	cfg = cfg.withDefaults()
	return &background{
		claimer:   claimer,
		processor: processor,
		cfg:       cfg,
		log:       log,
		batches:   make(chan service.Batch, cfg.QueueSize),
		// Buffer of one: any number of Notify calls between two polls collapse into one wake-up.
		wake: make(chan struct{}, 1),
	}
}

// Notify makes the dispatcher poll now instead of on the next tick. It never blocks.
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

// dispatch is the only sender on b.batches, so it closes the channel; the workers then drain it and exit.
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
	free := cap(b.batches) - len(b.batches)
	batches, err := b.claimer.Claim(ctx, min(b.cfg.BatchSize, free))
	if err != nil {
		if ctx.Err() == nil {
			b.log.Error("claim failed", "err", err)
		}
		return
	}

	for _, batch := range batches {
		if !b.send(ctx, batch) {
			return // shutting down: the rest stay leased and return to the queue by themselves
		}
	}
}

func (b *background) send(ctx context.Context, batch service.Batch) bool {
	select {
	case <-ctx.Done():
		return false
	case b.batches <- batch:
		return true
	}
}

func (b *background) work(ctx context.Context, id int) {
	for batch := range b.batches {
		b.handle(ctx, id, batch)
	}
}

func (b *background) handle(ctx context.Context, id int, batch service.Batch) {
	defer func() {
		if r := recover(); r != nil {
			b.log.Error("panic in worker", "worker", id, "pair", batch.Pair.String(), "panic", r,
				"stack", string(debug.Stack()))
		}
	}()
	if err := b.processor.Process(ctx, batch); err != nil {
		b.log.Error("batch outcome not recorded", "worker", id, "pair", batch.Pair.String(), "err", err)
	}
}
