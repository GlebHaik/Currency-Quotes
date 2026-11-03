package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/GlebHaik/currency-quotes/internal/app"
	"github.com/GlebHaik/currency-quotes/internal/config"
)

const configPath = "config/config.yaml"

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	return app.Run(ctx, cfg)
}
