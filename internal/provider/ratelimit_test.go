package provider

import (
	"context"
	"testing"
	"time"
)

type providerFunc func(ctx context.Context, base, quote string) (Rate, error)

func (f providerFunc) Rate(ctx context.Context, base, quote string) (Rate, error) {
	return f(ctx, base, quote)
}

func TestRateLimitedIsShared(t *testing.T) {
	p := NewRateLimited(providerFunc(func(context.Context, string, string) (Rate, error) {
		return Rate{Price: "1"}, nil
	}), 20, 1)

	start := time.Now()
	done := make(chan struct{})
	for range 4 {
		go func() {
			for range 3 {
				_, _ = p.Rate(context.Background(), "EUR", "USD")
			}
			done <- struct{}{}
		}()
	}
	for range 4 {
		<-done
	}
	// 12 calls at 20 RPS with burst 1
	if took := time.Since(start); took < 500*time.Millisecond {
		t.Fatalf("12 calls at 20 RPS finished in %v: the limit is not enforced", took)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Rate(ctx, "EUR", "USD"); err == nil {
		t.Fatal("waiting for a token must stop when ctx is cancelled")
	}
}
