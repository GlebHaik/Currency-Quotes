package domain

import (
	"fmt"
	"strings"
)

// CurrencyPair is a BASE/QUOTE pair of ISO 4217 codes, such as EUR/MXN.
// Its fields are unexported, so every non-zero value comes from ParseCurrencyPair and is valid.
type CurrencyPair struct {
	base, quote string
}

func ParseCurrencyPair(s string) (CurrencyPair, error) {
	base, quote, ok := strings.Cut(s, "/")
	if !ok || !isCurrencyCode(base) || !isCurrencyCode(quote) {
		return CurrencyPair{}, fmt.Errorf("%w: %q", ErrInvalidPair, s)
	}
	return CurrencyPair{base: base, quote: quote}, nil
}

// MustParseCurrencyPair is ParseCurrencyPair for constants and tests; it panics on invalid input.
func MustParseCurrencyPair(s string) CurrencyPair {
	p, err := ParseCurrencyPair(s)
	if err != nil {
		panic(err)
	}
	return p
}

func (p CurrencyPair) BaseCurrency() string  { return p.base }
func (p CurrencyPair) QuoteCurrency() string { return p.quote }
func (p CurrencyPair) String() string        { return p.base + "/" + p.quote }

func isCurrencyCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, ch := range s {
		if ch < 'A' || ch > 'Z' {
			return false
		}
	}
	return true
}
