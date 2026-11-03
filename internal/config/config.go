package config

import (
	"fmt"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
)

type Config struct {
	HTTP       HTTPConfig       `yaml:"http"`
	Database   DatabaseConfig   `yaml:"database"`
	Provider   ProviderConfig   `yaml:"provider"`
	Updater    UpdaterConfig    `yaml:"updater"`
	Background BackgroundConfig `yaml:"background"`
}

type HTTPConfig struct {
	Port            string        `yaml:"port" env:"HTTP_PORT"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	// RequestTimeout bounds each request, database calls included; 0 means no limit.
	RequestTimeout time.Duration `yaml:"request_timeout" env:"HTTP_REQUEST_TIMEOUT"`
}

type DatabaseConfig struct {
	URL          string `yaml:"url" env:"DATABASE_URL"`
	MaxOpenConns int    `yaml:"max_open_conns" env:"DATABASE_MAX_OPEN_CONNS"`
}

type ProviderConfig struct {
	BaseURL string        `yaml:"base_url" env:"PROVIDER_BASE_URL"`
	Timeout time.Duration `yaml:"timeout"`
	RPS     float64       `yaml:"rps"`
	Burst   int           `yaml:"burst"`
}

type UpdaterConfig struct {
	LockTTL      time.Duration `yaml:"lock_ttl"`
	MaxAttempts  int           `yaml:"max_attempts"`
	RetryBackoff time.Duration `yaml:"retry_backoff"`
	MaxBackoff   time.Duration `yaml:"max_backoff"`
	// FinalizeTimeout bounds the DB write that records a result while the service shuts down.
	FinalizeTimeout time.Duration `yaml:"finalize_timeout"`
}

type BackgroundConfig struct {
	Workers      int           `yaml:"workers" env:"BACKGROUND_WORKERS"`
	PollInterval time.Duration `yaml:"poll_interval"`
	BatchSize    int           `yaml:"batch_size"`
}

func Load(path string) (*Config, error) {
	var cfg Config
	if err := cleanenv.ReadConfig(path, &cfg); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return &cfg, nil
}
