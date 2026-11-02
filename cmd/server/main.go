package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os/signal"
	"strings"
	"syscall"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/GlebHaik/currency-quotes/internal/config"
	"github.com/GlebHaik/currency-quotes/internal/handler"
	"github.com/GlebHaik/currency-quotes/internal/provider"
	"github.com/GlebHaik/currency-quotes/internal/repository"
	"github.com/GlebHaik/currency-quotes/internal/service"
	"github.com/GlebHaik/currency-quotes/internal/worker"
)

const (
	configPath     = "config/config.yaml"
	migrationsPath = "file://migrations"
)

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

	if err := runMigrations(cfg.Database.URL); err != nil {
		return err
	}

	pool, err := NewPool(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()

	repo := repository.NewQuoteRepository(pool)
	queue := worker.NewJobQueue()
	svc := service.NewQuoteService(repo, queue)
	h := handler.NewQuoteHandler(svc)

	rates := provider.NewRateLimited(
		provider.NewFrankfurter(cfg.Provider.BaseURL, cfg.Provider.Timeout),
		cfg.Provider.RPS, cfg.Provider.Burst,
	)
	runner := worker.New(repo, rates, queue, worker.Config{
		Workers:      cfg.Worker.Workers,
		PollInterval: cfg.Worker.PollInterval,
		BatchSize:    cfg.Worker.BatchSize,
		LockTTL:      cfg.Worker.LockTTL,
		MaxAttempts:  cfg.Worker.MaxAttempts,
		RetryBackoff: cfg.Worker.RetryBackoff,
		MaxBackoff:   cfg.Worker.MaxBackoff,
	})

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Post("/api/v1/quotes/updates", h.RequestQuoteUpdate)
	r.Get("/api/v1/quotes/updates/{updateId}", h.GetQuoteUpdate)
	r.Get("/api/v1/quotes/latest", h.GetLatestQuote)

	srv := &http.Server{
		Addr:         ":" + cfg.HTTP.Port,
		Handler:      r,
		ReadTimeout:  cfg.HTTP.ReadTimeout,
		WriteTimeout: cfg.HTTP.WriteTimeout,
	}

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		runner.Run(gctx)
		return nil
	})

	g.Go(func() error {
		slog.Info("http server started", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		<-gctx.Done()
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.HTTP.ShutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	})

	return g.Wait()
}

func runMigrations(databaseURL string) error {
	dsn := "pgx5://" + strings.TrimPrefix(strings.TrimPrefix(databaseURL, "postgres://"), "postgresql://")

	m, err := migrate.New(migrationsPath, dsn)
	if err != nil {
		return fmt.Errorf("init migrations: %w", err)
	}
	defer func() { _, _ = m.Close() }()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

func NewPool(ctx context.Context, cfg config.DatabaseConfig) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if cfg.MaxOpenConns > 0 {
		poolCfg.MaxConns = int32(cfg.MaxOpenConns)
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return pool, nil
}
