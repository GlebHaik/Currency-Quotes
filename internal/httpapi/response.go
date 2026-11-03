package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/GlebHaik/currency-quotes/internal/api"
	"github.com/GlebHaik/currency-quotes/internal/domain"
)

// statusClientClosedRequest is nginx's code for a client that left before the answer:
// nobody reads it, it only marks the access log line.
const statusClientClosedRequest = 499

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

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "NOT_FOUND", "Not found")
}

func methodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed")
}

// writeDomainError maps domain and context errors to HTTP answers with fixed messages:
// err may be wrapped with internal details.
func (h *quoteHandler) writeDomainError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidPair):
		writeError(w, http.StatusBadRequest, "INVALID_PAIR", "Currency pair must look like EUR/MXN")
	case errors.Is(err, domain.ErrNotFound):
		notFound(w, r)
	case errors.Is(err, domain.ErrIdempotencyConflict):
		writeError(w, http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED",
			"Idempotency-Key was already used for another currency pair")
	case errors.Is(err, context.DeadlineExceeded):
		h.log.Warn("request timed out", requestAttrs(r, "err", err)...)
		writeError(w, http.StatusGatewayTimeout, "TIMEOUT", "Request timed out")
	case errors.Is(err, context.Canceled):
		// The client is gone: not a server failure, and there is no one to answer.
		w.WriteHeader(statusClientClosedRequest)
	default:
		h.log.Error("request failed", requestAttrs(r, "err", err)...)
		writeInternalError(w)
	}
}

// requestAttrs prefixes attrs with the request ID, which ties a log line to its access log line.
func requestAttrs(r *http.Request, attrs ...any) []any {
	return append([]any{"request_id", middleware.GetReqID(r.Context()), "method", r.Method, "path", r.URL.Path}, attrs...)
}
