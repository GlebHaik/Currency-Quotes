package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"syscall"

	"github.com/GlebHaik/currency-quotes/internal/app"
	"github.com/GlebHaik/currency-quotes/internal/config"
)

func main() {
	configPath := flag.String("config", "config/config.yaml", "path to the YAML config")
	flag.Parse()

	if err := run(*configPath); err != nil {
		log.Fatal(err)
	}
}

func run(configPath string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	return app.Run(ctx, cfg)
}
