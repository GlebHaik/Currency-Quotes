package domain

import "errors"

var (
	ErrNotFound            = errors.New("not found")
	ErrInvalidPair         = errors.New("invalid currency pair")
	ErrIdempotencyConflict = errors.New("idempotency key reused for another currency pair")
	// ErrPermanent marks provider errors that retrying won't fix, such as an unknown currency.
	ErrPermanent = errors.New("permanent provider error")
)
