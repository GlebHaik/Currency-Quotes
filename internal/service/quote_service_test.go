package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/GlebHaik/currency-quotes/internal/domain"
	"github.com/GlebHaik/currency-quotes/internal/repository"
	"github.com/GlebHaik/currency-quotes/internal/service/mocks"
)

var _ Repository = (*repository.QuoteRepository)(nil)

var (
	testNow = time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	eurMXN  = domain.MustParseCurrencyPair("EUR/MXN")
	eurUSD  = domain.MustParseCurrencyPair("EUR/USD")
)

type serviceMocks struct {
	repo     *mocks.MockRepository
	notifier *mocks.MockNotifier
}

func newTestService(t *testing.T) (*QuoteService, serviceMocks) {
	ctrl := gomock.NewController(t)
	m := serviceMocks{repo: mocks.NewMockRepository(ctrl), notifier: mocks.NewMockNotifier(ctrl)}
	s := NewQuoteService(m.repo, m.notifier)
	s.now = func() time.Time { return testNow }
	return s, m
}

func TestQuoteService_RequestUpdate(t *testing.T) {
	key := uuid.New()
	existing := domain.UpdateOperation{ID: uuid.New(), CurrencyPair: eurMXN, Status: domain.StatusCompleted, IdempotencyKey: &key}
	errDB := errors.New("connection refused")

	// inserted makes SaveUpdate behave like a fresh insert: it returns what it was given.
	inserted := func(_ context.Context, u domain.UpdateOperation) (domain.UpdateOperation, error) { return u, nil }
	pending := func(pair domain.CurrencyPair, key *uuid.UUID) gomock.Matcher {
		return gomock.Cond(func(u domain.UpdateOperation) bool {
			sameKey := (u.IdempotencyKey == nil && key == nil) ||
				(u.IdempotencyKey != nil && key != nil && *u.IdempotencyKey == *key)
			return u.ID != uuid.Nil && u.CurrencyPair == pair && u.Status == domain.StatusPending &&
				u.CreatedAt.Equal(testNow) && sameKey
		})
	}

	tests := []struct {
		name  string
		pair  domain.CurrencyPair
		key   *uuid.UUID
		setup func(m serviceMocks)
		// wantExisting: the original update's ID comes back instead of a new one.
		wantExisting bool
		wantErr      error
	}{
		{
			name: "new update without a key wakes the updater",
			pair: eurMXN,
			setup: func(m serviceMocks) {
				m.repo.EXPECT().SaveUpdate(gomock.Any(), pending(eurMXN, nil)).DoAndReturn(inserted)
				m.notifier.EXPECT().Notify()
			},
		},
		{
			name: "new update with a key stores the key",
			pair: eurMXN,
			key:  &key,
			setup: func(m serviceMocks) {
				m.repo.EXPECT().SaveUpdate(gomock.Any(), pending(eurMXN, &key)).DoAndReturn(inserted)
				m.notifier.EXPECT().Notify()
			},
		},
		{
			name: "retry with the same key and pair returns the original update and wakes nobody",
			pair: eurMXN,
			key:  &key,
			setup: func(m serviceMocks) {
				m.repo.EXPECT().SaveUpdate(gomock.Any(), pending(eurMXN, &key)).Return(existing, nil)
			},
			wantExisting: true,
		},
		{
			name: "same key with another pair is a conflict",
			pair: eurUSD,
			key:  &key,
			setup: func(m serviceMocks) {
				m.repo.EXPECT().SaveUpdate(gomock.Any(), pending(eurUSD, &key)).Return(existing, nil)
			},
			wantErr: domain.ErrIdempotencyConflict,
		},
		{
			name: "storage error is returned and wakes nobody",
			pair: eurMXN,
			setup: func(m serviceMocks) {
				m.repo.EXPECT().SaveUpdate(gomock.Any(), gomock.Any()).Return(domain.UpdateOperation{}, errDB)
			},
			wantErr: errDB,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, m := newTestService(t)
			tt.setup(m)

			got, err := s.RequestUpdate(context.Background(), tt.pair, tt.key)

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) || got != uuid.Nil {
					t.Fatalf("got (%s, %v), want (nil UUID, %v)", got, err, tt.wantErr)
				}
			case err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantExisting && got != existing.ID:
				t.Fatalf("got %s, want the original update %s", got, existing.ID)
			case !tt.wantExisting && (got == uuid.Nil || got == existing.ID):
				t.Fatalf("got %s, want a new update ID", got)
			}
		})
	}
}

func TestQuoteService_GetUpdate(t *testing.T) {
	op := domain.UpdateOperation{ID: uuid.New(), CurrencyPair: eurMXN, Status: domain.StatusPending}

	tests := []struct {
		name    string
		setup   func(m serviceMocks)
		want    domain.UpdateOperation
		wantErr error
	}{
		{
			name:  "found",
			setup: func(m serviceMocks) { m.repo.EXPECT().GetUpdate(gomock.Any(), op.ID).Return(op, nil) },
			want:  op,
		},
		{
			name: "not found",
			setup: func(m serviceMocks) {
				m.repo.EXPECT().GetUpdate(gomock.Any(), op.ID).Return(domain.UpdateOperation{}, domain.ErrNotFound)
			},
			wantErr: domain.ErrNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, m := newTestService(t)
			tt.setup(m)

			got, err := s.GetUpdate(context.Background(), op.ID)
			if !errors.Is(err, tt.wantErr) || got.ID != tt.want.ID {
				t.Fatalf("got (%+v, %v), want (%+v, %v)", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestQuoteService_GetLatestQuote(t *testing.T) {
	quote := domain.Quote{CurrencyPair: eurMXN, Price: "20.955000"}

	tests := []struct {
		name    string
		setup   func(m serviceMocks)
		want    domain.Quote
		wantErr error
	}{
		{
			name:  "found",
			setup: func(m serviceMocks) { m.repo.EXPECT().GetLatestQuote(gomock.Any(), eurMXN).Return(quote, nil) },
			want:  quote,
		},
		{
			name: "not found",
			setup: func(m serviceMocks) {
				m.repo.EXPECT().GetLatestQuote(gomock.Any(), eurMXN).Return(domain.Quote{}, domain.ErrNotFound)
			},
			wantErr: domain.ErrNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, m := newTestService(t)
			tt.setup(m)

			got, err := s.GetLatestQuote(context.Background(), eurMXN)
			if !errors.Is(err, tt.wantErr) || got != tt.want {
				t.Fatalf("got (%+v, %v), want (%+v, %v)", got, err, tt.want, tt.wantErr)
			}
		})
	}
}
