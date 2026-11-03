package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// accessLog writes one line per request. The request ID also goes back to the client in
// X-Request-Id, so a client's report can be matched with the server's error log.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := middleware.GetReqID(r.Context())
		w.Header().Set(middleware.RequestIDHeader, reqID)

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		status := ww.Status()
		if status == 0 {
			status = http.StatusOK // the handler wrote nothing
		}
		slog.Info("http request", "request_id", reqID, "method", r.Method, "path", r.URL.Path,
			"status", status, "bytes", ww.BytesWritten(), "duration_ms", float64(time.Since(start).Microseconds())/1000)
	})
}

// requestTimeout puts a deadline on every request, so a hung database call is cancelled instead
// of holding a pool connection. Unlike chi's middleware.Timeout it writes no response itself:
// the handler sees context.DeadlineExceeded and answers 504 in the usual JSON format.
func requestTimeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if d <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// allowMethods sets the Allow header a 405 must carry; chi drops it once a custom
// MethodNotAllowed handler is set.
func allowMethods(routes chi.Routes, next http.HandlerFunc) http.HandlerFunc {
	methods := []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions}

	return func(w http.ResponseWriter, r *http.Request) {
		for _, m := range methods {
			if routes.Match(chi.NewRouteContext(), m, r.URL.Path) {
				w.Header().Add("Allow", m)
			}
		}
		next(w, r)
	}
}
