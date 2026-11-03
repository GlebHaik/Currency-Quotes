package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/GlebHaik/currency-quotes/internal/api"
	"github.com/GlebHaik/currency-quotes/internal/domain"
)

// statusClientClosedRequest is nginx's code for a client that left before the answer:
// nobody reads it, it only marks the access log line.
const statusClientClosedRequest = 499

// NotFound answers unknown routes in the API's error format.
func NotFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "NOT_FOUND", "Not found")
}

// MethodNotAllowed answers a known route called with the wrong method; the router sets Allow.
func MethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
}

// Recoverer turns a panic into a logged 500 in the API's error format.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec) // net/http's way to abort a response; it handles this panic itself
			}
			slog.Error("panic in handler", requestAttrs(r, "panic", rec, "stack", string(debug.Stack()))...)
			writeInternalError(w)
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, api.ErrorResponse{Code: code, Message: message})
}

func writeInternalError(w http.ResponseWriter) {
	writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error")
}

// writeServiceError answers with fixed messages: err may be wrapped with internal details.
func writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidPair):
		writeError(w, http.StatusBadRequest, "INVALID_PAIR", "Currency pair must look like EUR/MXN")
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Not found")
	case errors.Is(err, domain.ErrIdempotencyConflict):
		writeError(w, http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED",
			"Idempotency-Key was already used for another currency pair")
	case errors.Is(err, context.DeadlineExceeded):
		slog.Warn("request timed out", requestAttrs(r, "err", err)...)
		writeError(w, http.StatusGatewayTimeout, "TIMEOUT", "Request timed out")
	case errors.Is(err, context.Canceled):
		// The client is gone: not a server failure, and there is no one to answer.
		w.WriteHeader(statusClientClosedRequest)
	default:
		slog.Error("request failed", requestAttrs(r, "err", err)...)
		writeInternalError(w)
	}
}

// requestAttrs prefixes attrs with the request ID, which ties a log line to its access log line.
func requestAttrs(r *http.Request, attrs ...any) []any {
	return append([]any{"request_id", middleware.GetReqID(r.Context()), "method", r.Method, "path", r.URL.Path}, attrs...)
}
