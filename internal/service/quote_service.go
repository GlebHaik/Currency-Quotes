package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/GlebHaik/currency-quotes/internal/domain"
)

var (
	ErrNotFound            = domain.ErrNotFound
	ErrInvalidPair         = errors.New("invalid currency pair")
	ErrIdempotencyConflict = errors.New("idempotency conflict")
)

//go:generate go tool mockgen -source=quote_service.go -destination=mocks/quote_service.go -package=mocks -typed

// Repository is the storage QuoteService needs; *repository.QuoteRepository satisfies it.
type Repository interface {
	SaveUpdate(ctx context.Context, update domain.UpdateOperation) (domain.UpdateOperation, error)
	GetUpdate(ctx context.Context, id uuid.UUID) (domain.UpdateOperation, error)
	GetLatestQuote(ctx context.Context, pair string) (domain.Quote, error)
}

type Notifier interface {
	Notify()
}

type QuoteService struct {
	repository Repository
	notifier   Notifier
	// now is the clock for CreatedAt; tests replace it to get exact times.
	now func() time.Time
}

func NewQuoteService(
	repository Repository,
	notifier Notifier,
) *QuoteService {
	return &QuoteService{
		repository: repository,
		notifier:   notifier,
		now:        time.Now,
	}
}

func validatePair(pair string) error {
	parts := strings.Split(pair, "/")

	if len(parts) != 2 {
		return ErrInvalidPair
	}

	for _, part := range parts {
		if len(part) != 3 {
			return ErrInvalidPair
		}

		for _, ch := range part {
			if ch < 'A' || ch > 'Z' {
				return ErrInvalidPair
			}
		}
	}

	return nil
}

// RequestUpdate stores a pending update and wakes the workers. A nil idempotencyKey means
// the request had none; a repeated key returns the original update without creating a new one.
func (s *QuoteService) RequestUpdate(
	ctx context.Context,
	pair string,
	idempotencyKey *uuid.UUID,
) (uuid.UUID, error) {
	if err := validatePair(pair); err != nil {
		return uuid.Nil, err
	}

	updateID := uuid.New()

	update := domain.UpdateOperation{
		ID:             updateID,
		CurrencyPair:   pair,
		Status:         domain.StatusPending,
		CreatedAt:      s.now().UTC(),
		IdempotencyKey: idempotencyKey,
	}

	u, err := s.repository.SaveUpdate(ctx, update)
	if err != nil {
		return uuid.Nil, fmt.Errorf("save update: %w", err)
	}

	isRepeat := u.ID != updateID

	if isRepeat && u.CurrencyPair != pair {
		return uuid.Nil, ErrIdempotencyConflict
	}

	if isRepeat && u.CurrencyPair == pair {
		return u.ID, nil
	}

	s.notifier.Notify()
	return updateID, nil
}

// GetUpdate returns an update by its ID, with the price once it is completed.
func (s *QuoteService) GetUpdate(
	ctx context.Context,
	id uuid.UUID,
) (domain.UpdateOperation, error) {
	return s.repository.GetUpdate(ctx, id)
}

// GetLatestQuote returns the most recent price known for pair.
func (s *QuoteService) GetLatestQuote(
	ctx context.Context,
	pair string,
) (domain.Quote, error) {
	if err := validatePair(pair); err != nil {
		return domain.Quote{}, err
	}

	return s.repository.GetLatestQuote(ctx, pair)
}
