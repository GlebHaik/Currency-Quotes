package domain

import "time"

type Quote struct {
	CurrencyPair string
	Price        string
	UpdatedAt    time.Time
	// SourceDate is the date the provider's rate applies to; zero if unknown.
	SourceDate time.Time
}

// Rate is a price as the provider reports it, before the worker turns it into a Quote.
type Rate struct {
	// Price is a decimal string, never a float.
	Price string
	// Date is the day the price applies to; zero if the provider does not say.
	Date time.Time
}
