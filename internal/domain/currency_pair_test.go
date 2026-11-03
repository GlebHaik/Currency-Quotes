package domain

import (
	"errors"
	"testing"
)

func TestParseCurrencyPair(t *testing.T) {
	tests := []struct {
		in    string
		valid bool
	}{
		{"EUR/MXN", true},
		{"USD/JPY", true},
		{"eur/mxn", false},
		{"EURMXN", false},
		{"EUR/MX", false},
		{"EURO/MXN", false},
		{"EUR/MXN/USD", false},
		{"EUR/", false},
		{"/MXN", false},
		{"EUR/M1N", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			p, err := ParseCurrencyPair(tt.in)
			if !tt.valid {
				if !errors.Is(err, ErrInvalidPair) {
					t.Fatalf("%q must be rejected with ErrInvalidPair, got %v", tt.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%q must be valid, got %v", tt.in, err)
			}
			if p.String() != tt.in || p.BaseCurrency()+"/"+p.QuoteCurrency() != tt.in {
				t.Fatalf("%q parsed into %q (%s, %s)", tt.in, p, p.BaseCurrency(), p.QuoteCurrency())
			}
		})
	}
}
