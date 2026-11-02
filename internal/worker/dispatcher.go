package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/GlebHaik/currency-quotes/internal/domain"
)

// Dispatcher moves pending updates from Postgres into the jobs channel.
// It is the only sender to that channel.
type Dispatcher struct {
	store Store
	queue *JobQueue
	cfg   Config
}

func NewDispatcher(store Store, queue *JobQueue, cfg Config) *Dispatcher {
	return &Dispatcher{store: store, queue: queue, cfg: cfg}
}

func (d *Dispatcher) Run(ctx context.Context) {
	defer close(d.queue.jobs)

	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()

	for {
		d.dispatch(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-d.queue.wake:
		}
	}
}

func (d *Dispatcher) dispatch(ctx context.Context) {
	free := cap(d.queue.jobs) - len(d.queue.jobs)
	limit := min(d.cfg.BatchSize, free)
	if limit <= 0 {
		return
	}

	updates, err := d.store.ClaimPending(ctx, limit, d.cfg.LockTTL)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("claim pending updates", "err", err)
		}
		return
	}

	for _, job := range buildJobs(updates) {
		if !d.send(ctx, job) {
			// Shutting down: the rest stay leased in Postgres and return to the queue
			return
		}
	}
}

func buildJobs(updates []domain.UpdateOperation) []Job {
	jobs := make([]Job, 0, len(updates))
	pairs := make(map[string]int)
	for _, u := range updates {
		i, ok := pairs[u.CurrencyPair]
		if !ok {
			i = len(jobs)
			pairs[u.CurrencyPair] = i
			jobs = append(jobs, Job{Pair: u.CurrencyPair})
		}
		jobs[i].IDs = append(jobs[i].IDs, u.ID.String())
		jobs[i].Attempts = max(jobs[i].Attempts, u.Attempts)
	}
	return jobs
}

func (d *Dispatcher) send(ctx context.Context, job Job) bool {
	select {
	case <-ctx.Done():
		return false
	case d.queue.jobs <- job:
	}
	return true
}
