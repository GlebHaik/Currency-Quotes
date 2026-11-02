package domain

import (
	"context"

	"github.com/google/uuid"
)

type QuoteRepository interface {
	SaveUpdate(ctx context.Context, update UpdateOperation) (UpdateOperation, error)
	GetUpdate(ctx context.Context, id uuid.UUID) (UpdateOperation, error)
	GetLatestQuote(ctx context.Context, pair string) (Quote, error)
}
