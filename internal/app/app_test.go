package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRequestTimeout(t *testing.T) {
	tests := []struct {
		name         string
		timeout      time.Duration
		wantDeadline bool
	}{
		{name: "sets a deadline on the request context", timeout: 5 * time.Second, wantDeadline: true},
		{name: "zero leaves requests unbounded", timeout: 0, wantDeadline: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var deadline time.Time
			var ok bool
			h := requestTimeout(tt.timeout)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				deadline, ok = r.Context().Deadline()
			}))

			start := time.Now()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

			if ok != tt.wantDeadline {
				t.Fatalf("deadline set = %v, want %v", ok, tt.wantDeadline)
			}
			if ok && (deadline.Before(start) || deadline.After(start.Add(tt.timeout+time.Second))) {
				t.Fatalf("deadline %v is not about %v after the request started", deadline, tt.timeout)
			}
		})
	}
}
