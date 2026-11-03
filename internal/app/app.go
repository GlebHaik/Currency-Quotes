package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/GlebHaik/currency-quotes/internal/clients/frankfurter"
	"github.com/GlebHaik/currency-quotes/internal/config"
	"github.com/GlebHaik/currency-quotes/internal/handler"
	"github.com/GlebHaik/currency-quotes/internal/repository"
	"github.com/GlebHaik/currency-quotes/internal/service"
)

// Run applies migrations and serves HTTP and background updates until ctx is cancelled,
// then shuts both down gracefully.
func Run(ctx context.Context, cfg *config.Config) error {
	if err := runMigrations(cfg.Database); err != nil {
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
	}, slog.Default())
	bg := newBackground(updater, updater, backgroundConfig{
		Workers:      cfg.Background.Workers,
		PollInterval: cfg.Background.PollInterval,
		BatchSize:    cfg.Background.BatchSize,
		QueueSize:    cfg.Background.QueueSize,
	})
	svc := service.NewQuoteService(repo, bg)
	srv := newHTTPServer(cfg.HTTP, handler.NewQuoteHandler(svc))

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		bg.Run(gctx)
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

func newHTTPServer(cfg config.HTTPConfig, h *handler.QuoteHandler) *http.Server {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(requestTimeout(cfg.RequestTimeout))
	r.Post("/api/v1/quotes/updates", h.RequestQuoteUpdate)
	r.Get("/api/v1/quotes/updates/{updateId}", h.GetQuoteUpdate)
	r.Get("/api/v1/quotes/latest", h.GetLatestQuote)

	return &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}
}

// requestTimeout puts a deadline on every request, so a hung database call is cancelled instead
// of holding a pool connection. Unlike chi's middleware.Timeout it writes no response itself:
// the handler sees context.DeadlineExceeded and answers 504 in the usual JSON format.
func requestTimeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if d <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func runMigrations(cfg config.DatabaseConfig) error {
	// golang-migrate picks its driver by URL scheme; pgx5:// is the pgx v5 one.
	dsn := "pgx5://" + strings.TrimPrefix(strings.TrimPrefix(cfg.URL, "postgres://"), "postgresql://")

	m, err := migrate.New("file://"+cfg.MigrationsDir, dsn)
	if err != nil {
		return fmt.Errorf("init migrations: %w", err)
	}
	defer func() { _, _ = m.Close() }()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
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
