package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/GlebHaik/currency-quotes/internal/api"
	"github.com/GlebHaik/currency-quotes/internal/domain"
	"github.com/GlebHaik/currency-quotes/internal/service"
)

type QuoteService interface {
	RequestUpdate(ctx context.Context, pair string, idempotencyKey string) (uuid.UUID, error)
	GetUpdate(ctx context.Context, id uuid.UUID) (domain.UpdateOperation, error)
	GetLatestQuote(ctx context.Context, pair string) (domain.Quote, error)
}

type QuoteHandler struct {
	service QuoteService
}

func NewQuoteHandler(s QuoteService) *QuoteHandler {
	return &QuoteHandler{service: s}
}

func (h *QuoteHandler) RequestQuoteUpdate(
	w http.ResponseWriter,
	r *http.Request,
) {
	var request api.UpdateQuoteRequest

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid JSON body")
		return
	}

	updateID, err := h.service.RequestUpdate(
		r.Context(),
		request.CurrencyPair,
		r.Header.Get("Idempotency-Key"),
	)

	if err != nil {
		writeServiceError(w, err)
		return
	}

	response := api.UpdateAcceptedResponse{
		UpdateId: updateID,
		Status:   api.Pending,
	}

	writeJSON(w, http.StatusAccepted, response)
}

func (h *QuoteHandler) GetQuoteUpdate(
	w http.ResponseWriter,
	r *http.Request,
) {
	id, err := uuid.Parse(r.PathValue("updateId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid update ID")
		return
	}

	update, err := h.service.GetUpdate(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	response := api.UpdateStatusResponse{
		UpdateId:     update.ID,
		CurrencyPair: update.CurrencyPair,
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

func (h *QuoteHandler) GetLatestQuote(
	w http.ResponseWriter,
	r *http.Request,
) {
	pair := r.URL.Query().Get("currencyPair")

	quote, err := h.service.GetLatestQuote(r.Context(), pair)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	response := api.Quote{
		CurrencyPair: quote.CurrencyPair,
		Price:        quote.Price,
		UpdatedAt:    quote.UpdatedAt,
		SourceDate:   sourceDate(quote),
	}

	writeJSON(w, http.StatusOK, response)
}

// sourceDate leaves the field out of the response when the provider date is unknown.
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
	writeJSON(w, status, api.ErrorResponse{
		Code:    code,
		Message: message,
	})
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidPair):
		writeError(w, http.StatusBadRequest, "INVALID_PAIR", err.Error())

	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())

	case errors.Is(err, service.ErrInvalidIdempotencyKey):
		writeError(w, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", err.Error())

	case errors.Is(err, service.ErrIdempotencyConflict):
		writeError(w, http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED", err.Error())

	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error")
	}
}
