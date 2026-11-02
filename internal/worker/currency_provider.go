package worker

import (
	"context"

	"github.com/GlebHaik/currency-quotes/internal/provider"
)

// CurrencyProvider is what the pool needs from an exchange-rate source.
// provider.Frankfurter and provider.RateLimited satisfy it.
type CurrencyProvider interface {
	Rate(ctx context.Context, base, quote string) (provider.Rate, error)
}
