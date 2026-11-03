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

// maxBodyBytes is far above any valid request, which is a few dozen bytes.
const maxBodyBytes = 1 << 10

func (h *QuoteHandler) RequestQuoteUpdate(w http.ResponseWriter, r *http.Request) {
	var request api.UpdateQuoteRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&request); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "Request body is too large")
			return
		}
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

	w.Header().Set("Location", r.URL.JoinPath(updateID.String()).Path)
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
