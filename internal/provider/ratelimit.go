package provider

import (
	"context"
	"sync"
	"time"
)

// RateLimited puts one limiter in front of a provider. All workers share it,
// so the external API sees the pool's total RPS, not the RPS of a single worker.
type RateLimited struct {
	next   Provider
	rps    float64
	burst  int
	tokens float64
	last   time.Time
	mux    *sync.Mutex
}

func NewRateLimited(next Provider, rps float64, burst int) *RateLimited {
	burst = max(burst, 1)
	return &RateLimited{next: next, rps: rps, burst: burst, tokens: float64(burst), last: time.Now(), mux: &sync.Mutex{}}
}

func (p *RateLimited) Rate(ctx context.Context, base, quote string) (Rate, error) {
	if err := p.wait(ctx); err != nil {
		return Rate{}, err
	}
	return p.next.Rate(ctx, base, quote)
}

// wait blocks until a request is allowed or ctx is done
func (p *RateLimited) wait(ctx context.Context) error {
	if p.rps <= 0 {
		return nil
	}

	for {
		p.mux.Lock()
		p.tokens = float64(min(float64(p.burst), p.tokens+time.Since(p.last).Seconds()*p.rps))
		p.last = time.Now()
		if p.tokens >= 1 {
			p.tokens--
			p.mux.Unlock()
			return nil
		}

		lack := 1 - p.tokens
		p.mux.Unlock()
		delay := time.Duration(lack / p.rps * float64(time.Second))
		timer := time.NewTimer(delay)

		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}

}
