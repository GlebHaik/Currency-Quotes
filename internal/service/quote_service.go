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
	ErrNotFound              = domain.ErrNotFound
	ErrInvalidPair           = errors.New("invalid currency pair")
	ErrInvalidIdempotencyKey = errors.New("invalid idempotency key")
	ErrIdempotencyConflict   = errors.New("idempotency conflict")
)

type Notifier interface {
	Notify()
}

type QuoteService struct {
	repository domain.QuoteRepository
	notifier   Notifier
}

func NewQuoteService(
	repository domain.QuoteRepository,
	notifier Notifier,
) *QuoteService {
	return &QuoteService{
		repository: repository,
		notifier:   notifier,
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

func validateIdempotencyKey(idempotencyKey string) error {
	if len(idempotencyKey) > 255 {
		return ErrInvalidIdempotencyKey
	}
	return nil
}

func (s *QuoteService) RequestUpdate(
	ctx context.Context,
	pair string,
	idempotencyKey string,
) (uuid.UUID, error) {
	if err := validatePair(pair); err != nil {
		return uuid.Nil, err
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return uuid.Nil, err
	}

	updateID := uuid.New()

	update := domain.UpdateOperation{
		ID:             updateID,
		CurrencyPair:   pair,
		Status:         domain.StatusPending,
		CreatedAt:      time.Now().UTC(),
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

// GET /api/v1/quotes/updates/{updateId}
func (s *QuoteService) GetUpdate(
	ctx context.Context,
	id uuid.UUID,
) (domain.UpdateOperation, error) {
	return s.repository.GetUpdate(ctx, id)
}

// GET /api/v1/quotes/latest
func (s *QuoteService) GetLatestQuote(
	ctx context.Context,
	pair string,
) (domain.Quote, error) {
	if err := validatePair(pair); err != nil {
		return domain.Quote{}, err
	}

	return s.repository.GetLatestQuote(ctx, pair)
}
