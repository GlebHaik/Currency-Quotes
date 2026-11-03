package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouterFallbacks(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.Handler
		wantStatus int
		wantBody   string
	}{
		{
			name:       "unknown route",
			handler:    http.HandlerFunc(notFound),
			wantStatus: http.StatusNotFound,
			wantBody:   `{"code":"NOT_FOUND","message":"Not found"}`,
		},
		{
			name:       "wrong method",
			handler:    http.HandlerFunc(methodNotAllowed),
			wantStatus: http.StatusMethodNotAllowed,
			wantBody:   `{"code":"METHOD_NOT_ALLOWED","message":"Method not allowed"}`,
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
