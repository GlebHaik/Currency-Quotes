package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouterErrorsAndAccessLog(t *testing.T) {
	var logs bytes.Buffer
	// The service is never reached: every case fails in the router.
	router := NewRouter(nil, slog.New(slog.NewJSONHandler(&logs, nil)), 0)

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
