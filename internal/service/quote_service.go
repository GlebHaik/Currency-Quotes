package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/GlebHaik/currency-quotes/internal/domain"
)

//go:generate go tool mockgen -source=quote_service.go -destination=mocks/quote_service.go -package=mocks -typed

type Repository interface {
	SaveUpdate(ctx context.Context, update domain.UpdateOperation) (domain.UpdateOperation, error)
	GetUpdate(ctx context.Context, id uuid.UUID) (domain.UpdateOperation, error)
	GetLatestQuote(ctx context.Context, pair domain.CurrencyPair) (domain.Quote, error)
}

// Notifier wakes the background updater. It is only a hint: the update is already stored,
// and the updater polls the table anyway.
type Notifier interface {
	Notify()
}

type QuoteService struct {
	repository Repository
	notifier   Notifier
	now        func() time.Time // replaced in tests
}

func NewQuoteService(repository Repository, notifier Notifier) *QuoteService {
	return &QuoteService{repository: repository, notifier: notifier, now: time.Now}
}

// RequestUpdate stores a pending update and returns its ID. A retry with the same idempotency key
// returns the original update instead of creating a new one.
func (s *QuoteService) RequestUpdate(ctx context.Context, pair domain.CurrencyPair, idempotencyKey uuid.UUID) (uuid.UUID, error) {
	updateID := uuid.New()
	saved, err := s.repository.SaveUpdate(ctx, domain.UpdateOperation{
		ID:             updateID,
		CurrencyPair:   pair,
		Status:         domain.StatusPending,
		CreatedAt:      s.now().UTC(),
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return uuid.Nil, err
	}

	if saved.ID != updateID { // the key was already used
		if saved.CurrencyPair != pair {
			return uuid.Nil, domain.ErrIdempotencyConflict
		}
		return saved.ID, nil
	}

	s.notifier.Notify()
	return updateID, nil
}

func (s *QuoteService) GetUpdate(ctx context.Context, id uuid.UUID) (domain.UpdateOperation, error) {
	return s.repository.GetUpdate(ctx, id)
}

func (s *QuoteService) GetLatestQuote(ctx context.Context, pair domain.CurrencyPair) (domain.Quote, error) {
	return s.repository.GetLatestQuote(ctx, pair)
}
