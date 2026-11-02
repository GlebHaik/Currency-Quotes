package domain

import (
	"time"

	"github.com/google/uuid"
)

type UpdateOperation struct {
	ID             uuid.UUID
	CurrencyPair   string
	Status         UpdateStatus
	Quote          *Quote
	IdempotencyKey string
	Error          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Attempts       int
}
