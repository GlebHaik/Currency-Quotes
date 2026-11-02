package worker

import (
	"context"
	"time"

	"github.com/GlebHaik/currency-quotes/internal/domain"
)

// Store is the part of repository that worker needs; *repository.QuoteRepository satisfies it.
type Store interface {
	ClaimPending(ctx context.Context, limit int, lease time.Duration) ([]domain.UpdateOperation, error)
	CompleteUpdates(ctx context.Context, ids []string, q domain.Quote) error
	FailUpdates(ctx context.Context, ids []string, reason string) error
	RetryLater(ctx context.Context, ids []string, delay time.Duration) error
}
