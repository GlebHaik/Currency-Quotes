package config

import (
	"fmt"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
)

type Config struct {
	HTTP     HTTPConfig     `yaml:"http"`
	Database DatabaseConfig `yaml:"database"`
	Provider ProviderConfig `yaml:"provider"`
	Worker   WorkerConfig   `yaml:"worker"`
}

type HTTPConfig struct {
	Port            string        `yaml:"port" env:"HTTP_PORT"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

type DatabaseConfig struct {
	URL          string `yaml:"url" env:"DATABASE_URL"`
	MaxOpenConns int    `yaml:"max_open_conns" env:"DATABASE_MAX_OPEN_CONNS"`
	MaxIdleConns int    `yaml:"max_idle_conns" env:"DATABASE_MAX_IDLE_CONNS"`
}

type ProviderConfig struct {
	BaseURL            string        `yaml:"base_url" env:"PROVIDER_BASE_URL"`
	Timeout            time.Duration `yaml:"timeout"`
	RPS                float64       `yaml:"rps"`
	Burst              int           `yaml:"burst"`
	MaxPairsPerRequest int           `yaml:"max_pairs_per_request"`
}

type WorkerConfig struct {
	Workers      int           `yaml:"workers" env:"WORKER_COUNT"`
	PollInterval time.Duration `yaml:"poll_interval"`
	BatchSize    int           `yaml:"batch_size"`
	LockTTL      time.Duration `yaml:"lock_ttl"`
	MaxAttempts  int           `yaml:"max_attempts"`
	RetryBackoff time.Duration `yaml:"retry_backoff"`
	MaxBackoff   time.Duration `yaml:"max_backoff"`
}

func Load(path string) (*Config, error) {
	var cfg Config
	if err := cleanenv.ReadConfig(path, &cfg); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return &cfg, nil
}
