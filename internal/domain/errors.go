package domain

import "errors"

var (
	ErrNotFound = errors.New("not found")
	// ErrPermanent marks provider errors that retrying will not fix, such as an unknown currency.
	ErrPermanent = errors.New("permanent provider error")
)
