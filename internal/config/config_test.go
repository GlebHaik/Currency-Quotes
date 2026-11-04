package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		lockTTL time.Duration
		timeout time.Duration
		wantErr bool
	}{
		{name: "lease outlasts the provider call", lockTTL: 30 * time.Second, timeout: 5 * time.Second},
		{name: "zero lease means the updater's default", lockTTL: 0, timeout: 5 * time.Second},
		{name: "lease equal to the provider timeout", lockTTL: 5 * time.Second, timeout: 5 * time.Second, wantErr: true},
		{name: "lease shorter than the provider timeout", lockTTL: 3 * time.Second, timeout: 5 * time.Second, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Updater:  UpdaterConfig{LockTTL: tt.lockTTL},
				Provider: ProviderConfig{Timeout: tt.timeout},
			}
			if err := cfg.validate(); (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	t.Run("the shipped config is valid", func(t *testing.T) {
		if _, err := Load("../../config/config.yaml"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("an invalid config is rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		yaml := "updater:\n  lock_ttl: 3s\nprovider:\n  timeout: 5s\n"
		if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}

		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "lock_ttl") {
			t.Fatalf("err = %v, want one naming lock_ttl", err)
		}
	})
}
