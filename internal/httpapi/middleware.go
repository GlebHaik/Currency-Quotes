package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// accessLog writes one line per request. The request ID also goes back to the client in
// X-Request-Id, so a client's report can be matched with the server's error log.
func accessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
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
			log.Info("http request", "request_id", reqID, "method", r.Method, "path", r.URL.Path,
				"status", status, "bytes", ww.BytesWritten(), "duration_ms", float64(time.Since(start).Microseconds())/1000)
		})
	}
}

// recoverer turns a panic into a logged 500 in the API's error format.
func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler {
					panic(rec) // net/http's way to abort a response; it handles this panic itself
				}
				log.Error("panic in handler", requestAttrs(r, "panic", rec, "stack", string(debug.Stack()))...)
				writeInternalError(w)
			}()
			next.ServeHTTP(w, r)
		})
	}
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
