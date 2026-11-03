package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
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

var discard = slog.New(slog.DiscardHandler)

func TestRecoverer(t *testing.T) {
	tests := []struct {
		name       string
		next       http.HandlerFunc
		wantStatus int
		wantBody   string
	}{
		{
			name:       "no panic passes the answer through",
			next:       notFound,
			wantStatus: http.StatusNotFound,
			wantBody:   `{"code":"NOT_FOUND","message":"Not found"}`,
		},
		{
			name:       "panic becomes a 500 in the API's format",
			next:       func(http.ResponseWriter, *http.Request) { panic("boom") },
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"code":"INTERNAL_ERROR","message":"Internal server error"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			recoverer(discard)(tt.next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status %d, want %d", rec.Code, tt.wantStatus)
			}
			assertJSONEqual(t, rec.Body.String(), tt.wantBody)
		})
	}
}

func TestRecovererLeavesAbortHandlerToNetHTTP(t *testing.T) {
	defer func() {
		if r := recover(); r != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler re-panicked", r)
		}
	}()
	h := recoverer(discard)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestAccessLogCountsSilentHandlerAsOK(t *testing.T) {
	var logs bytes.Buffer
	h := accessLog(slog.New(slog.NewJSONHandler(&logs, nil)))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	var line struct{ Status int }
	if err := json.Unmarshal(logs.Bytes(), &line); err != nil || line.Status != http.StatusOK {
		t.Fatalf("access log %q, want status 200", logs.String())
	}
}
