package domain

import "time"

type Quote struct {
	CurrencyPair string
	Price        string
	UpdatedAt    time.Time
	// SourceDate is the date the provider's rate applies to; zero if unknown.
	SourceDate time.Time
}
