package app

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GlebHaik/currency-quotes/internal/config"
	"github.com/GlebHaik/currency-quotes/internal/handler"
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

func TestRouterErrorsAndAccessLog(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// The service is never reached: every case fails in the router.
	router := newRouter(config.HTTPConfig{}, handler.NewQuoteHandler(nil))

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantCode   string
		wantAllow  string
	}{
		{name: "unknown route", method: http.MethodGet, path: "/nope", wantStatus: http.StatusNotFound, wantCode: "NOT_FOUND"},
		{
			name: "wrong method lists the allowed ones", method: http.MethodDelete, path: "/api/v1/quotes/updates",
			wantStatus: http.StatusMethodNotAllowed, wantCode: "METHOD_NOT_ALLOWED", wantAllow: "POST",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs.Reset()
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status %d, want %d", rec.Code, tt.wantStatus)
			}
			var body struct{ Code string }
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != tt.wantCode {
				t.Fatalf("body %q, want JSON with code %s", rec.Body, tt.wantCode)
			}
			if got := rec.Header().Get("Allow"); got != tt.wantAllow {
				t.Fatalf("Allow %q, want %q", got, tt.wantAllow)
			}

			reqID := rec.Header().Get("X-Request-Id")
			if reqID == "" {
				t.Fatal("no X-Request-Id in the response")
			}
			var line struct {
				Msg       string
				RequestID string `json:"request_id"`
				Status    int
			}
			if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
				t.Fatalf("access log %q: %v", logs.String(), err)
			}
			if line.Msg != "http request" || line.RequestID != reqID || line.Status != tt.wantStatus {
				t.Fatalf("access log %+v, want request %s with status %d", line, reqID, tt.wantStatus)
			}
		})
	}
}
