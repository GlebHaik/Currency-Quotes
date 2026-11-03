package httpapi

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
	"github.com/GlebHaik/currency-quotes/internal/httpapi/mocks"
	"github.com/GlebHaik/currency-quotes/internal/service"
)

var _ QuoteService = (*service.QuoteService)(nil)

var (
	eurMXN     = domain.MustParseCurrencyPair("EUR/MXN")
	eurUSD     = domain.MustParseCurrencyPair("EUR/USD")
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
	// wantBody is JSON, or empty for an answer without a body.
	wantBody    string
	wantHeaders map[string]string
}

// run serves each case through route with a fresh mock and checks the status, headers and body.
// Any service call a case did not expect fails it through the gomock controller.
func run(t *testing.T, cases []httpCase, route func(h *quoteHandler) http.HandlerFunc) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := mocks.NewMockQuoteService(gomock.NewController(t))
			if tc.setup != nil {
				tc.setup(m)
			}
			rec := httptest.NewRecorder()

			route(newQuoteHandler(m, discard)).ServeHTTP(rec, tc.req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d; body %s", rec.Code, tc.wantStatus, rec.Body)
			}
			for name, want := range tc.wantHeaders {
				if got := rec.Header().Get(name); got != want {
					t.Fatalf("%s %q, want %q", name, got, want)
				}
			}
			if tc.wantBody == "" {
				if rec.Body.Len() != 0 {
					t.Fatalf("body %q, want none", rec.Body)
				}
				return
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
	k := key.String()
	accepted := `{"status":"pending","updateId":"550e8400-e29b-41d4-a716-446655440000"}`
	badKey := `{"code":"INVALID_IDEMPOTENCY_KEY","message":"Idempotency-Key header is required and must be a UUID"}`

	run(t, []httpCase{
		{
			name: "accepted",
			req:  postUpdate(`{"currencyPair":"EUR/MXN"}`, k),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().RequestUpdate(gomock.Any(), eurMXN, key).Return(updateID, nil)
			},
			wantStatus:  http.StatusAccepted,
			wantBody:    accepted,
			wantHeaders: map[string]string{"Location": "/api/v1/quotes/updates/550e8400-e29b-41d4-a716-446655440000"},
		},
		{
			name: "key in upper case reaches the service normalised",
			req:  postUpdate(`{"currencyPair":"EUR/MXN"}`, strings.ToUpper(k)),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().RequestUpdate(gomock.Any(), eurMXN, key).Return(updateID, nil)
			},
			wantStatus: http.StatusAccepted,
			wantBody:   accepted,
		},
		{
			name:       "missing key never reaches the service",
			req:        postUpdate(`{"currencyPair":"EUR/MXN"}`, ""),
			wantStatus: http.StatusBadRequest,
			wantBody:   badKey,
		},
		{
			name:       "key that is not a UUID",
			req:        postUpdate(`{"currencyPair":"EUR/MXN"}`, "key-1"),
			wantStatus: http.StatusBadRequest,
			wantBody:   badKey,
		},
		{
			name:       "malformed JSON",
			req:        postUpdate(`{"currencyPair":`, k),
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"code":"INVALID_BODY","message":"Invalid JSON body"}`,
		},
		{
			name:       "unknown field, as the spec forbids additional properties",
			req:        postUpdate(`{"currencyPair":"EUR/MXN","pair":"EUR/USD"}`, k),
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"code":"INVALID_BODY","message":"Invalid JSON body"}`,
		},
		{
			name:       "body over the limit is not read to the end",
			req:        postUpdate(`{"currencyPair":"`+strings.Repeat("A", maxBodyBytes)+`"}`, k),
			wantStatus: http.StatusRequestEntityTooLarge,
			wantBody:   `{"code":"BODY_TOO_LARGE","message":"Request body is too large"}`,
		},
		{
			name:       "invalid pair never reaches the service",
			req:        postUpdate(`{"currencyPair":"eur/mxn"}`, k),
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"code":"INVALID_PAIR","message":"Currency pair must look like EUR/MXN"}`,
		},
		{
			name: "key reused for another pair",
			req:  postUpdate(`{"currencyPair":"EUR/USD"}`, k),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().RequestUpdate(gomock.Any(), eurUSD, key).Return(uuid.Nil, domain.ErrIdempotencyConflict)
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantBody:   `{"code":"IDEMPOTENCY_KEY_REUSED","message":"Idempotency-Key was already used for another currency pair"}`,
		},
		{
			name: "client gone: no body and no error",
			req:  postUpdate(`{"currencyPair":"EUR/MXN"}`, k),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().RequestUpdate(gomock.Any(), eurMXN, key).
					Return(uuid.Nil, fmt.Errorf("save update: %w", context.Canceled))
			},
			wantStatus: statusClientClosedRequest,
		},
		{
			name: "unexpected error does not leak details",
			req:  postUpdate(`{"currencyPair":"EUR/MXN"}`, k),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().RequestUpdate(gomock.Any(), eurMXN, key).Return(uuid.Nil, errUnknown)
			},
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"code":"INTERNAL_ERROR","message":"Internal server error"}`,
		},
	}, func(h *quoteHandler) http.HandlerFunc { return h.RequestQuoteUpdate })
}

func getUpdate(id string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/quotes/updates/"+id, nil)
	req.SetPathValue("updateId", id) // what the router does for /updates/{updateId}
	return req
}

func TestGetQuoteUpdate(t *testing.T) {
	op := func(status domain.UpdateStatus) domain.UpdateOperation {
		return domain.UpdateOperation{ID: updateID, CurrencyPair: eurMXN, Status: status}
	}
	completed := op(domain.StatusCompleted)
	completed.Quote = &domain.Quote{CurrencyPair: eurMXN, Price: "20.955000", UpdatedAt: updatedAt, SourceDate: sourceDay}
	failed := op(domain.StatusFailed)
	failed.Error = "currency pair is not supported by the provider"

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
				"error":"currency pair is not supported by the provider"}`,
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
				m.EXPECT().GetUpdate(gomock.Any(), updateID).Return(domain.UpdateOperation{}, domain.ErrNotFound)
			},
			wantStatus: http.StatusNotFound,
			wantBody:   `{"code":"NOT_FOUND","message":"Not found"}`,
		},
	}, func(h *quoteHandler) http.HandlerFunc { return h.GetQuoteUpdate })
}

func getLatest(rawQuery string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/api/v1/quotes/latest?"+rawQuery, nil)
}

func TestGetLatestQuote(t *testing.T) {
	quote := domain.Quote{CurrencyPair: eurMXN, Price: "20.955000", UpdatedAt: updatedAt, SourceDate: sourceDay}
	undated := quote
	undated.SourceDate = time.Time{}

	run(t, []httpCase{
		{
			name: "found, with the pair URL-encoded",
			req:  getLatest("currencyPair=EUR%2FMXN"),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().GetLatestQuote(gomock.Any(), eurMXN).Return(quote, nil)
			},
			wantStatus: http.StatusOK,
			wantBody:   `{"currencyPair":"EUR/MXN","price":"20.955000","updatedAt":"2026-10-30T08:30:00Z","sourceDate":"2026-10-30"}`,
		},
		{
			name: "unknown source date is left out",
			req:  getLatest("currencyPair=EUR%2FMXN"),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().GetLatestQuote(gomock.Any(), eurMXN).Return(undated, nil)
			},
			wantStatus: http.StatusOK,
			wantBody:   `{"currencyPair":"EUR/MXN","price":"20.955000","updatedAt":"2026-10-30T08:30:00Z"}`,
		},
		{
			name:       "missing pair never reaches the service",
			req:        getLatest(""),
			wantStatus: http.StatusBadRequest,
			wantBody:   `{"code":"INVALID_PAIR","message":"Currency pair must look like EUR/MXN"}`,
		},
		{
			name: "database too slow for the request deadline",
			req:  getLatest("currencyPair=EUR%2FMXN"),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().GetLatestQuote(gomock.Any(), eurMXN).
					Return(domain.Quote{}, fmt.Errorf("get latest quote: %w", context.DeadlineExceeded))
			},
			wantStatus: http.StatusGatewayTimeout,
			wantBody:   `{"code":"TIMEOUT","message":"Request timed out"}`,
		},
		{
			name: "no quote yet",
			req:  getLatest("currencyPair=EUR%2FMXN"),
			setup: func(m *mocks.MockQuoteService) {
				m.EXPECT().GetLatestQuote(gomock.Any(), eurMXN).Return(domain.Quote{}, domain.ErrNotFound)
			},
			wantStatus: http.StatusNotFound,
			wantBody:   `{"code":"NOT_FOUND","message":"Not found"}`,
		},
	}, func(h *quoteHandler) http.HandlerFunc { return h.GetLatestQuote })
}
