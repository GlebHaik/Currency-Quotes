package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestErrorResponses(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.Handler
		wantStatus int
		wantBody   string
	}{
		{
			name:       "unknown route",
			handler:    http.HandlerFunc(NotFound),
			wantStatus: http.StatusNotFound,
			wantBody:   `{"code":"NOT_FOUND","message":"Not found"}`,
		},
		{
			name:       "wrong method",
			handler:    http.HandlerFunc(MethodNotAllowed),
			wantStatus: http.StatusMethodNotAllowed,
			wantBody:   `{"code":"METHOD_NOT_ALLOWED","message":"Method not allowed"}`,
		},
		{
			name:       "no panic passes the answer through",
			handler:    Recoverer(http.HandlerFunc(NotFound)),
			wantStatus: http.StatusNotFound,
			wantBody:   `{"code":"NOT_FOUND","message":"Not found"}`,
		},
		{
			name:       "panic becomes a 500 in the API's format",
			handler:    Recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })),
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"code":"INTERNAL_ERROR","message":"Internal server error"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tt.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

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
	h := Recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}
