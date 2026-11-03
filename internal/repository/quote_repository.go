package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/GlebHaik/currency-quotes/internal/domain"
)

type QuoteRepository struct {
	pool *pgxpool.Pool
}

func NewQuoteRepository(pool *pgxpool.Pool) *QuoteRepository {
	return &QuoteRepository{pool: pool}
}

const updateColumns = `id, pair, status, price::text, error, attempts, created_at, updated_at, source_date`

func (r *QuoteRepository) SaveUpdate(ctx context.Context, u domain.UpdateOperation) (domain.UpdateOperation, error) {
	row := r.pool.QueryRow(ctx, `
	INSERT INTO quote_updates (id, pair, status, created_at, idempotency_key)
	VALUES ($1, $2, $3, $4, $5)
	ON CONFLICT (idempotency_key) DO NOTHING
	RETURNING `+updateColumns,
		u.ID, u.CurrencyPair, u.Status, u.CreatedAt, u.IdempotencyKey)

	saved, err := scanUpdate(row)
	if errors.Is(err, pgx.ErrNoRows) {
		row = r.pool.QueryRow(ctx,
			`SELECT `+updateColumns+` FROM quote_updates WHERE idempotency_key = $1`, u.IdempotencyKey)
		saved, err = scanUpdate(row)
	}
	if err != nil {
		return domain.UpdateOperation{}, fmt.Errorf("save quote update: %w", err)
	}
	return saved, nil
}

func (r *QuoteRepository) GetUpdate(ctx context.Context, id uuid.UUID) (domain.UpdateOperation, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+updateColumns+` FROM quote_updates WHERE id = $1`, id)
	u, err := scanUpdate(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.UpdateOperation{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.UpdateOperation{}, fmt.Errorf("get quote update: %w", err)
	}
	return u, nil
}

func (r *QuoteRepository) GetLatestQuote(ctx context.Context, pair string) (domain.Quote, error) {
	q := domain.Quote{CurrencyPair: pair}
	var sourceDate *time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT price::text, updated_at, source_date FROM quotes WHERE pair = $1`, pair).
		Scan(&q.Price, &q.UpdatedAt, &sourceDate)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Quote{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Quote{}, fmt.Errorf("get latest quote: %w", err)
	}
	if sourceDate != nil {
		q.SourceDate = *sourceDate
	}
	return q, nil
}

func (r *QuoteRepository) ClaimPending(ctx context.Context, limit int, lease time.Duration) ([]domain.UpdateOperation, error) {
	rows, err := r.pool.Query(ctx, `
		UPDATE quote_updates
		SET locked_until = now() + make_interval(secs => $2), attempts = attempts + 1
		WHERE id IN (
			SELECT id FROM quote_updates
			WHERE status = 'pending' AND (locked_until IS NULL OR locked_until < now())
			ORDER BY created_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+updateColumns,
		limit, lease.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim pending updates: %w", err)
	}
	updates, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.UpdateOperation, error) {
		return scanUpdate(row)
	})
	if err != nil {
		return nil, fmt.Errorf("claim pending updates: %w", err)
	}
	return updates, nil
}

func (r *QuoteRepository) CompleteUpdates(ctx context.Context, ids []uuid.UUID, q domain.Quote) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE quote_updates
			SET status = 'completed', price = $2::numeric, updated_at = $3, source_date = $4, locked_until = NULL
			WHERE id = ANY($1) AND status = 'pending'`,
			ids, q.Price, q.UpdatedAt, nullDate(q.SourceDate))
		if err != nil {
			return fmt.Errorf("complete quote updates: %w", err)
		}
		return saveQuote(ctx, tx, q)
	})
}

func (r *QuoteRepository) FailUpdates(ctx context.Context, ids []uuid.UUID, reason string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE quote_updates
		SET status = 'failed', error = $2, updated_at = now(), locked_until = NULL
		WHERE id = ANY($1) AND status = 'pending'`,
		ids, reason)
	if err != nil {
		return fmt.Errorf("fail quote updates: %w", err)
	}
	return nil
}

func (r *QuoteRepository) RetryLater(ctx context.Context, ids []uuid.UUID, delay time.Duration) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE quote_updates
		SET locked_until = now() + make_interval(secs => $2)
		WHERE id = ANY($1) AND status = 'pending'`,
		ids, delay.Seconds())
	if err != nil {
		return fmt.Errorf("retry quote updates: %w", err)
	}
	return nil
}

func saveQuote(ctx context.Context, tx pgx.Tx, q domain.Quote) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO quotes (pair, price, updated_at, source_date) VALUES ($1, $2::numeric, $3, $4)
		ON CONFLICT (pair) DO UPDATE
		SET price = EXCLUDED.price, updated_at = EXCLUDED.updated_at, source_date = EXCLUDED.source_date
		WHERE quotes.updated_at < EXCLUDED.updated_at`,
		q.CurrencyPair, q.Price, q.UpdatedAt, nullDate(q.SourceDate))
	if err != nil {
		return fmt.Errorf("upsert quote: %w", err)
	}
	return nil
}

func scanUpdate(row pgx.Row) (domain.UpdateOperation, error) {
	var (
		u          domain.UpdateOperation
		price      *string
		errText    *string
		updatedAt  *time.Time
		sourceDate *time.Time
	)
	if err := row.Scan(&u.ID, &u.CurrencyPair, &u.Status, &price, &errText, &u.Attempts, &u.CreatedAt, &updatedAt, &sourceDate); err != nil {
		return domain.UpdateOperation{}, err
	}
	if errText != nil {
		u.Error = *errText
	}
	if updatedAt != nil {
		u.UpdatedAt = *updatedAt
	}
	if price != nil && updatedAt != nil {
		u.Quote = &domain.Quote{CurrencyPair: u.CurrencyPair, Price: *price, UpdatedAt: *updatedAt}
		if sourceDate != nil {
			u.Quote.SourceDate = *sourceDate
		}
	}
	return u, nil
}

func nullDate(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
