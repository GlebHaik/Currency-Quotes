package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFrankfurter(t *testing.T) {
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
	p := NewFrankfurter(srv.URL+"/", time.Second)
	ctx := context.Background()

	rate, err := p.Rate(ctx, "EUR", "MXN")
	if err != nil || rate.Price != "20.955000000000001" {
		t.Fatalf("price %q, err %v: the number must not go through float64", rate.Price, err)
	}
	if want := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC); !rate.Date.Equal(want) {
		t.Fatalf("date %v, want %v", rate.Date, want)
	}

	_, err = p.Rate(ctx, "EUR", "XXX")
	var perr *Error
	if !errors.Is(err, ErrPermanent) || !errors.As(err, &perr) || perr.Message != "invalid currency: XXX" {
		t.Fatalf("422 must be permanent with the provider message, got %v", err)
	}

	_, err = p.Rate(ctx, "EUR", "USD")
	if errors.Is(err, ErrPermanent) || !errors.As(err, &perr) || perr.RetryAfter != 7*time.Second {
		t.Fatalf("429 must be retryable with Retry-After, got %v", err)
	}

	_, err = p.Rate(ctx, "EUR", "GBP")
	if err == nil || errors.Is(err, ErrPermanent) {
		t.Fatalf("5xx must be retryable, got %v", err)
	}
}
