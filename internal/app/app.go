package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/GlebHaik/currency-quotes/internal/clients/frankfurter"
	"github.com/GlebHaik/currency-quotes/internal/config"
	"github.com/GlebHaik/currency-quotes/internal/httpapi"
	"github.com/GlebHaik/currency-quotes/internal/repository"
	"github.com/GlebHaik/currency-quotes/internal/service"
)

func Run(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	if err := runMigrations(cfg.Database, log); err != nil {
		return err
	}

	pool, err := newPool(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()

	repo := repository.NewQuoteRepository(pool)
	rates := frankfurter.New(frankfurter.Config{
		BaseURL: cfg.Provider.BaseURL,
		Timeout: cfg.Provider.Timeout,
		RPS:     cfg.Provider.RPS,
		Burst:   cfg.Provider.Burst,
	})
	updater := service.NewQuoteUpdater(repo, rates, service.UpdaterConfig{
		LockTTL:         cfg.Updater.LockTTL,
		MaxAttempts:     cfg.Updater.MaxAttempts,
		RetryBackoff:    cfg.Updater.RetryBackoff,
		MaxBackoff:      cfg.Updater.MaxBackoff,
		FinalizeTimeout: cfg.Updater.FinalizeTimeout,
	}, log)
	bg := newBackground(updater, updater, backgroundConfig{
		Workers:      cfg.Background.Workers,
		PollInterval: cfg.Background.PollInterval,
		BatchSize:    cfg.Background.BatchSize,
		QueueSize:    cfg.Background.QueueSize,
	}, log)
	svc := service.NewQuoteService(repo, bg)
	srv := newHTTPServer(cfg.HTTP, httpapi.NewRouter(svc, log, cfg.HTTP.RequestTimeout))

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		bg.Run(gctx)
		log.Info("background processing stopped")
		return nil
	})

	g.Go(func() error {
		log.Info("http server started", "addr", srv.Addr, "workers", cfg.Background.Workers,
			"provider", cfg.Provider.BaseURL, "provider_rps", cfg.Provider.RPS)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		<-gctx.Done()
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.HTTP.ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http shutdown: %w", err)
		}
		log.Info("http server stopped")
		return nil
	})

	return g.Wait()
}

func newHTTPServer(cfg config.HTTPConfig, h http.Handler) *http.Server {
	return &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      h,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}
}

func runMigrations(cfg config.DatabaseConfig, log *slog.Logger) error {
	dsn := "pgx5://" + strings.TrimPrefix(strings.TrimPrefix(cfg.URL, "postgres://"), "postgresql://")

	m, err := migrate.New("file://"+cfg.MigrationsDir, dsn)
	if err != nil {
		return fmt.Errorf("init migrations: %w", err)
	}
	defer func() { _, _ = m.Close() }()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	version, _, _ := m.Version()
	log.Info("migrations applied", "version", version)
	return nil
}

func newPool(ctx context.Context, cfg config.DatabaseConfig) (*pgxpool.Pool, error) {
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
