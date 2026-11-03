package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/GlebHaik/currency-quotes/internal/domain"
	"github.com/GlebHaik/currency-quotes/internal/handler/mocks"
	"github.com/GlebHaik/currency-quotes/internal/service"
)

var _ QuoteService = (*service.QuoteService)(nil)

var (
	updateID   = uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	updatedAt  = time.Date(2026, 10, 30, 8, 30, 0, 0, time.UTC)
	sourceDay  = time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	errUnknown = errors.New("connection refused")
)

type httpCase struct {
	name       string
	req        *http.Request
	setup      func(m *mocks.MockQuoteService)
	wantStatus int
	wantBody   string
}

// run serves each case through route with a fresh mock and checks the status and the JSON body.
// Any service call a case did not expect fails it through the gomock controller.
func run(t *testing.T, cases []httpCase, route func(h *QuoteHandler) http.HandlerFunc) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := mocks.NewMockQuoteService(gomock.NewController(t))
			if tc.setup != nil {
				tc.setup(m)
			}
			rec := httptest.NewRecorder()

			route(NewQuoteHandler(m)).ServeHTTP(rec, tc.req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d; body %s", rec.Code, tc.wantStatus, rec.Body)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("Content-Type %q, want application/json", ct)
			}
			assertJSONEqual(t, rec.Body.String(), tc.wantBody)
		})
	}
}

func assertJSONEqual(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("response is not JSON: %v; body %s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad wantBody in test: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("body\n got: %s\nwant: %s", strings.TrimSpace(got), want)
	}
}

func postUpdate(body, key string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/quotes/updates", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	return req
}

func TestRequestQuoteUpdate(t *testing.T) {
	key := uuid.MustParse("8e3b1c2a-4f5d-4e6a-9b7c-1d2e3f4a5b6c")
	isKey := gomock.Cond(func(k *uuid.UUID) bool { return k != nil && *k == key })
	accepted := `{"status":"pending","updateId":"550e8400-e29b-41d4-a716-446655440000"}`

	run(t, []httpCase{
		{
			name: "accepted without a key",
			req:  postUpdate(`{"currencyPair":"EUR/MXN"}`, ""),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().RequestUpdate(gomock.Any(), "EUR/MXN", gomock.Nil()).Return(updateID, nil)
			},
			wantStatus: http.StatusAccepted,
			wantBody:   accepted,
		},
		{
			name: "key in upper case reaches the service normalised",
			req:  postUpdate(`{"currencyPair":"EUR/MXN"}`, strings.ToUpper(key.String())),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().RequestUpdate(gomock.Any(), "EUR/MXN", isKey).Return(updateID, nil)
			},
			wantStatus: http.StatusAccepted,
			wantBody:   accepted,
		},
		{
			name:       "malformed JSON",
			req:        postUpdate(`{"currencyPair":`, ""),
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"code":"INVALID_BODY","message":"Invalid JSON body"}`,
		},
		{
			name:       "key that is not a UUID",
			req:        postUpdate(`{"currencyPair":"EUR/MXN"}`, "key-1"),
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"code":"INVALID_IDEMPOTENCY_KEY","message":"Idempotency-Key must be a UUID"}`,
		},
		{
			name: "invalid pair",
			req:  postUpdate(`{"currencyPair":"eur/mxn"}`, ""),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().RequestUpdate(gomock.Any(), "eur/mxn", gomock.Nil()).Return(uuid.Nil, service.ErrInvalidPair)
			},
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"code":"INVALID_PAIR","message":"invalid currency pair"}`,
		},
		{
			name: "key reused for another pair",
			req:  postUpdate(`{"currencyPair":"EUR/USD"}`, key.String()),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().RequestUpdate(gomock.Any(), "EUR/USD", isKey).Return(uuid.Nil, service.ErrIdempotencyConflict)
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantBody:   `{"code":"IDEMPOTENCY_KEY_REUSED","message":"idempotency conflict"}`,
		},
		{
			name: "unexpected error does not leak details",
			req:  postUpdate(`{"currencyPair":"EUR/MXN"}`, ""),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().RequestUpdate(gomock.Any(), "EUR/MXN", gomock.Nil()).Return(uuid.Nil, errUnknown)
			},
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"code":"INTERNAL_ERROR","message":"Internal server error"}`,
		},
	}, func(h *QuoteHandler) http.HandlerFunc { return h.RequestQuoteUpdate })
}

func getUpdate(id string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/quotes/updates/"+id, nil)
	req.SetPathValue("updateId", id) // what the router does for /updates/{updateId}
	return req
}

func TestGetQuoteUpdate(t *testing.T) {
	op := func(status domain.UpdateStatus) domain.UpdateOperation {
		return domain.UpdateOperation{ID: updateID, CurrencyPair: "EUR/MXN", Status: status}
	}
	completed := op(domain.StatusCompleted)
	completed.Quote = &domain.Quote{CurrencyPair: "EUR/MXN", Price: "20.955000", UpdatedAt: updatedAt, SourceDate: sourceDay}
	failed := op(domain.StatusFailed)
	failed.Error = "frankfurter: invalid currency: XXX (HTTP 422)"

	run(t, []httpCase{
		{
			name:       "completed carries price, update time and source date",
			req:        getUpdate(updateID.String()),
			setup:      func(m *mocks.MockQuoteService) { m.EXPECT().GetUpdate(gomock.Any(), updateID).Return(completed, nil) },
			wantStatus: http.StatusOK,
			wantBody: `{"updateId":"550e8400-e29b-41d4-a716-446655440000","currencyPair":"EUR/MXN","status":"completed",
				"price":"20.955000","updatedAt":"2026-10-30T08:30:00Z","sourceDate":"2026-10-30"}`,
		},
		{
			name: "pending has no price fields",
			req:  getUpdate(updateID.String()),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().GetUpdate(gomock.Any(), updateID).Return(op(domain.StatusPending), nil)
			},
			wantStatus: http.StatusOK,
			wantBody:   `{"updateId":"550e8400-e29b-41d4-a716-446655440000","currencyPair":"EUR/MXN","status":"pending"}`,
		},
		{
			name:       "failed carries the error",
			req:        getUpdate(updateID.String()),
			setup:      func(m *mocks.MockQuoteService) { m.EXPECT().GetUpdate(gomock.Any(), updateID).Return(failed, nil) },
			wantStatus: http.StatusOK,
			wantBody: `{"updateId":"550e8400-e29b-41d4-a716-446655440000","currencyPair":"EUR/MXN","status":"failed",
				"error":"frankfurter: invalid currency: XXX (HTTP 422)"}`,
		},
		{
			name:       "ID that is not a UUID",
			req:        getUpdate("42"),
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"code":"INVALID_ID","message":"Invalid update ID"}`,
		},
		{
			name: "unknown ID",
			req:  getUpdate(updateID.String()),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().GetUpdate(gomock.Any(), updateID).Return(domain.UpdateOperation{}, service.ErrNotFound)
			},
			wantStatus: http.StatusNotFound,
			wantBody:   `{"code":"NOT_FOUND","message":"not found"}`,
		},
	}, func(h *QuoteHandler) http.HandlerFunc { return h.GetQuoteUpdate })
}

func getLatest(rawQuery string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/api/v1/quotes/latest?"+rawQuery, nil)
}

func TestGetLatestQuote(t *testing.T) {
	quote := domain.Quote{CurrencyPair: "EUR/MXN", Price: "20.955000", UpdatedAt: updatedAt, SourceDate: sourceDay}
	undated := quote
	undated.SourceDate = time.Time{}

	run(t, []httpCase{
		{
			name: "found, with the pair URL-encoded",
			req:  getLatest("currencyPair=EUR%2FMXN"),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().GetLatestQuote(gomock.Any(), "EUR/MXN").Return(quote, nil)
			},
			wantStatus: http.StatusOK,
			wantBody:   `{"currencyPair":"EUR/MXN","price":"20.955000","updatedAt":"2026-10-30T08:30:00Z","sourceDate":"2026-10-30"}`,
		},
		{
			name: "unknown source date is left out",
			req:  getLatest("currencyPair=EUR%2FMXN"),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().GetLatestQuote(gomock.Any(), "EUR/MXN").Return(undated, nil)
			},
			wantStatus: http.StatusOK,
			wantBody:   `{"currencyPair":"EUR/MXN","price":"20.955000","updatedAt":"2026-10-30T08:30:00Z"}`,
		},
		{
			name: "missing pair is passed on and rejected",
			req:  getLatest(""),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().GetLatestQuote(gomock.Any(), "").Return(domain.Quote{}, service.ErrInvalidPair)
			},
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"code":"INVALID_PAIR","message":"invalid currency pair"}`,
		},
		{
			name: "database too slow for the request deadline",
			req:  getLatest("currencyPair=EUR%2FMXN"),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().GetLatestQuote(gomock.Any(), "EUR/MXN").
					Return(domain.Quote{}, fmt.Errorf("get latest quote: %w", context.DeadlineExceeded))
			},
			wantStatus: http.StatusGatewayTimeout,
			wantBody:   `{"code":"TIMEOUT","message":"Request timed out"}`,
		},
		{
			name: "no quote yet",
			req:  getLatest("currencyPair=EUR%2FMXN"),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().GetLatestQuote(gomock.Any(), "EUR/MXN").Return(domain.Quote{}, service.ErrNotFound)
			},
			wantStatus: http.StatusNotFound,
			wantBody:   `{"code":"NOT_FOUND","message":"not found"}`,
		},
	}, func(h *QuoteHandler) http.HandlerFunc { return h.GetLatestQuote })
}
