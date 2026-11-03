package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/GlebHaik/currency-quotes/internal/api"
	"github.com/GlebHaik/currency-quotes/internal/domain"
)

//go:generate go tool mockgen -source=quote_handler.go -destination=mocks/quote_handler.go -package=mocks -typed

type QuoteService interface {
	RequestUpdate(ctx context.Context, pair domain.CurrencyPair, idempotencyKey *uuid.UUID) (uuid.UUID, error)
	GetUpdate(ctx context.Context, id uuid.UUID) (domain.UpdateOperation, error)
	GetLatestQuote(ctx context.Context, pair domain.CurrencyPair) (domain.Quote, error)
}

type QuoteHandler struct {
	service QuoteService
}

func NewQuoteHandler(s QuoteService) *QuoteHandler {
	return &QuoteHandler{service: s}
}

func (h *QuoteHandler) RequestQuoteUpdate(w http.ResponseWriter, r *http.Request) {
	var request api.UpdateQuoteRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid JSON body")
		return
	}

	pair, err := domain.ParseCurrencyPair(request.CurrencyPair)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}

	idempotencyKey, err := parseIdempotencyKey(r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key must be a UUID")
		return
	}

	updateID, err := h.service.RequestUpdate(r.Context(), pair, idempotencyKey)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusAccepted, api.UpdateAcceptedResponse{UpdateId: updateID, Status: api.Pending})
}

func (h *QuoteHandler) GetQuoteUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("updateId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid update ID")
		return
	}

	update, err := h.service.GetUpdate(r.Context(), id)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}

	response := api.UpdateStatusResponse{
		UpdateId:     update.ID,
		CurrencyPair: update.CurrencyPair.String(),
		Status:       api.UpdateStatus(update.Status),
	}
	if update.Quote != nil {
		response.Price = &update.Quote.Price
		response.UpdatedAt = &update.Quote.UpdatedAt
		response.SourceDate = sourceDate(*update.Quote)
	}
	if update.Error != "" {
		response.Error = &update.Error
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *QuoteHandler) GetLatestQuote(w http.ResponseWriter, r *http.Request) {
	pair, err := domain.ParseCurrencyPair(r.URL.Query().Get("currencyPair"))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}

	quote, err := h.service.GetLatestQuote(r.Context(), pair)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, api.Quote{
		CurrencyPair: quote.CurrencyPair.String(),
		Price:        quote.Price,
		UpdatedAt:    quote.UpdatedAt,
		SourceDate:   sourceDate(quote),
	})
}

// parseIdempotencyKey returns nil for an absent header. uuid.Parse normalises case, so one UUID
// in upper and lower case is one key. The nil UUID is rejected: all clients sending it would share it.
func parseIdempotencyKey(header string) (*uuid.UUID, error) {
	if header == "" {
		return nil, nil
	}
	key, err := uuid.Parse(header)
	if err != nil || key == uuid.Nil {
		return nil, errors.New("invalid idempotency key")
	}
	return &key, nil
}

func sourceDate(q domain.Quote) *openapi_types.Date {
	if q.SourceDate.IsZero() {
		return nil
	}
	return &openapi_types.Date{Time: q.SourceDate}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, api.ErrorResponse{Code: code, Message: message})
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
		slog.Warn("request timed out", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusGatewayTimeout, "TIMEOUT", "Request timed out")
	default:
		slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error")
	}
}
