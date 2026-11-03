package domain

import (
	"time"

	"github.com/google/uuid"
)

type UpdateStatus string

const (
	StatusPending   UpdateStatus = "pending"
	StatusCompleted UpdateStatus = "completed"
	StatusFailed    UpdateStatus = "failed"
)

type UpdateOperation struct {
	ID           uuid.UUID
	CurrencyPair string
	Status       UpdateStatus
	Quote        *Quote
	// IdempotencyKey is the client's Idempotency-Key; nil when the request had none.
	IdempotencyKey *uuid.UUID
	Error          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Attempts       int
}
