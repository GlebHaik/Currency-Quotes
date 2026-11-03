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
	ID             uuid.UUID
	CurrencyPair   CurrencyPair
	Status         UpdateStatus
	Quote          *Quote
	IdempotencyKey uuid.UUID
	Error          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Attempts       int
}
