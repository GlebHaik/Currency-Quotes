package httpapi

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestParseIdempotencyKey(t *testing.T) {
	const key = "8e3b1c2a-4f5d-4e6a-9b7c-1d2e3f4a5b6c"

	tests := []struct {
		name    string
		header  string
		want    uuid.UUID
		wantErr bool
	}{
		{name: "absent", header: "", wantErr: true},
		{name: "valid", header: key, want: uuid.MustParse(key)},
		{name: "upper case is the same key", header: strings.ToUpper(key), want: uuid.MustParse(key)},
		{name: "not a uuid", header: "key-1", wantErr: true},
		{name: "nil uuid", header: uuid.Nil.String(), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseIdempotencyKey(tt.header)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
