// Package provider fetches exchange rates from external APIs.
package provider

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Rate is one price from the provider
type Rate struct {
	// Price is a decimal string
	Price string
	// Date is the day the price applies to; zero if the provider does not say
	Date time.Time
}

// Provider fetches the current price of base/quote
type Provider interface {
	Rate(ctx context.Context, base, quote string) (Rate, error)
}

// ErrPermanent marks provider errors that retrying will not fix, such as an unknown currency
var ErrPermanent = errors.New("permanent provider error")

type Error struct {
	StatusCode int
	Message    string
	// RetryAfter is the delay the provider asked for (429 Retry-After), zero if none
	RetryAfter time.Duration
	Permanent  bool
}

func (e *Error) Error() string {
	return fmt.Sprintf("provider: %s (HTTP %d)", e.Message, e.StatusCode)
}

func (e *Error) Is(target error) bool {
	return target == ErrPermanent && e.Permanent
}
