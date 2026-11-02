package worker

import (
	"context"
	"time"
)

type Config struct {
	Workers      int
	PollInterval time.Duration
	BatchSize    int
	// LockTTL > channel wait + rate-limit wait + provider timeout, else the lease expires mid-flight and the update is claimed twice.
	LockTTL      time.Duration
	MaxAttempts  int
	RetryBackoff time.Duration
	MaxBackoff   time.Duration
}

func (c Config) withDefaults() Config {
	if c.Workers <= 0 {
		c.Workers = 4
	}
	if c.PollInterval <= 0 {
		c.PollInterval = time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 100
	}
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
	return c
}

// Pipeline: Postgres -> Dispatcher -> channel -> WorkerPool -> CurrencyProvider
type Runner struct {
	dispatcher *Dispatcher
	pool       *WorkerPool
}

func New(store Store, provider CurrencyProvider, queue *JobQueue, cfg Config) *Runner {
	cfg = cfg.withDefaults()
	return &Runner{
		dispatcher: NewDispatcher(store, queue, cfg),
		pool:       NewWorkerPool(queue, provider, store, cfg),
	}
}

func (r *Runner) Run(ctx context.Context) {
	poolDone := make(chan struct{})
	go func() {
		defer close(poolDone)
		r.pool.Run(ctx)
	}()

	r.dispatcher.Run(ctx)
	<-poolDone
}
