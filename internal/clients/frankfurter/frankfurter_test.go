package frankfurter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GlebHaik/currency-quotes/internal/domain"
)

func TestClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/rate/eur/mxn":
			_, _ = w.Write([]byte(`{"date":"2026-10-30","base":"EUR","quote":"MXN","rate":20.955000000000001}`))
		case "/v2/rate/eur/xxx":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"status":422,"message":"invalid currency: XXX"}`))
		case "/v2/rate/eur/usd":
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL + "/", Timeout: time.Second})
	ctx := context.Background()

	rate, err := c.Rate(ctx, domain.MustParseCurrencyPair("EUR/MXN"))
	if err != nil || rate.Price != "20.955000000000001" {
		t.Fatalf("price %q, err %v: the number must not go through float64", rate.Price, err)
	}
	if want := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC); !rate.Date.Equal(want) {
		t.Fatalf("date %v, want %v", rate.Date, want)
	}

	_, err = c.Rate(ctx, domain.MustParseCurrencyPair("EUR/XXX"))
	ferr, ok := errors.AsType[*Error](err)
	if !errors.Is(err, domain.ErrPermanent) || !ok || ferr.Message != "invalid currency: XXX" {
		t.Fatalf("422 must be permanent with the provider message, got %v", err)
	}

	_, err = c.Rate(ctx, domain.MustParseCurrencyPair("EUR/USD"))
	ferr, ok = errors.AsType[*Error](err)
	if errors.Is(err, domain.ErrPermanent) || !ok || ferr.RetryAfter() != 7*time.Second {
		t.Fatalf("429 must be retryable with Retry-After, got %v", err)
	}

	_, err = c.Rate(ctx, domain.MustParseCurrencyPair("EUR/GBP"))
	if err == nil || errors.Is(err, domain.ErrPermanent) {
		t.Fatalf("5xx must be retryable, got %v", err)
	}
}

func TestClientRateLimitIsShared(t *testing.T) {
	eurUSD := domain.MustParseCurrencyPair("EUR/USD")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"date":"2026-10-30","rate":1}`))
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL, Timeout: time.Second, RPS: 20, Burst: 1})

	start := time.Now()
	done := make(chan struct{})
	for range 4 { // like 4 workers sharing one client
		go func() {
			for range 3 {
				_, _ = c.Rate(context.Background(), eurUSD)
			}
			done <- struct{}{}
		}()
	}
	for range 4 {
		<-done
	}
	// 12 calls at 20 RPS with burst 1: the first is free, the other 11 need ≥ 550ms.
	if took := time.Since(start); took < 500*time.Millisecond || calls.Load() != 12 {
		t.Fatalf("%d calls finished in %v: the limit is not enforced", calls.Load(), took)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Rate(ctx, domain.MustParseCurrencyPair("EUR/USD")); err == nil {
		t.Fatal("waiting for a token must stop when ctx is cancelled")
	}
}
